#!/usr/bin/env python3
"""Offline tests for the CPA usage self-service portal."""
from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from starlette.testclient import TestClient

from cpa_usage_portal.app import create_app
from cpa_usage_portal.budget import suggest_equal_budget
from cpa_usage_portal.config import PortalConfig
from cpa_usage_portal.key_policy import KeyPolicyState
from cpa_usage_portal.redaction import redact, safe_event
from cpa_usage_portal.retention import enforce_usage_retention
from cpa_usage_portal.security import (
    hash_preview,
    key_policy_hash,
    normalize_key_hash,
    sha256_hex,
    sign_session,
    verify_session,
)


_RESULTS: list[tuple[str, bool, str]] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    _RESULTS.append((name, bool(cond), detail))


class FakeCPAMP:
    def __init__(self, expected_hash: str, other_hash: str) -> None:
        self.expected_hash = expected_hash
        self.other_hash = other_hash
        self.seen_hashes: list[str] = []

    async def health(self):
        return {"ok": True}

    async def analytics(self, *, api_key_hash, window, include_events=False,
                        event_limit=100, before_ms=None, before_id=None):
        self.seen_hashes.append(api_key_hash)
        data = {
            "summary": {
                "total_calls": 2,
                "success_calls": 1,
                "failure_calls": 1,
                "success_rate": 0.5,
                "total_tokens": 300,
                "reasoning_tokens": 100,
                "total_cost": 0.0123,
            },
            "timeline": [],
            "model_share": [{"model": "gpt-5.5", "calls": 2, "tokens": 300, "cost": 0.0123}],
            "api_key_stats": [
                {"api_key_hash": api_key_hash, "calls": 2, "total_tokens": 300},
                {"api_key_hash": self.other_hash, "calls": 99, "total_tokens": 999},
            ],
        }
        if include_events:
            data["events"] = {
                "items": [
                    {
                        "event_hash": "evt-a",
                        "request_id": "req-a",
                        "timestamp_ms": 1800000000000,
                        "api_key_hash": api_key_hash,
                        "model": "gpt-5.5",
                        "failed": False,
                        "total_tokens": 200,
                        "reasoning_tokens": 80,
                        "latency_ms": 1234,
                        "cost": 0.01,
                    },
                    {
                        "event_hash": "evt-b",
                        "request_id": "req-b",
                        "timestamp_ms": 1800000001000,
                        "api_key_hash": self.other_hash,
                        "model": "gpt-5.5",
                        "failed": True,
                        "fail_summary": "Authorization: Bearer should-not-leak",
                    },
                ],
                "has_more": False,
                "total_count": 2,
            }
        return data


def write_state(path: Path, raw_key: str, disabled_key: str) -> tuple[str, str, str]:
    key_hash = sha256_hex(raw_key)
    cpamp_hash = sha256_hex("alice-key")
    disabled_hash = sha256_hex(disabled_key)
    path.write_text(
        json.dumps(
            {
                "keys": [
                    {
                        "id": "alice-key",
                        "key_hash": key_policy_hash(key_hash),
                        "name": "Alice",
                        "enabled": True,
                        "rpm": 12,
                        "models": ["gpt-5.5"],
                        "daily_limit_usd": 5,
                        "weekly_limit_usd": 30,
                        "daily_usage_usd": 0.5,
                        "model_prices": {"gpt-5.5": {"input": 1}},
                    },
                    {
                        "id": "disabled-key",
                        "key_hash": key_policy_hash(disabled_hash),
                        "name": "Disabled",
                        "enabled": False,
                        "model_prices": {"gpt-5.5": {"input": 1}},
                    },
                ]
            },
            ensure_ascii=False,
        ),
        encoding="utf-8",
    )
    return key_hash, cpamp_hash, disabled_hash


def test_hash_and_session() -> None:
    raw = " cpa_test_key "
    hex_hash = sha256_hex(raw)
    check("sha256 trims raw key", hex_hash == sha256_hex(raw.strip()))
    check("normalize strips sha256 prefix", normalize_key_hash(key_policy_hash(hex_hash)) == hex_hash)
    check("hash preview hides middle", hash_preview(hex_hash).startswith(hex_hash[:8]))

    token = sign_session({"key_hash": hex_hash, "key_name": "Alice"}, "secret", ttl_seconds=60)
    payload = verify_session(token, "secret")
    check("session verifies", payload is not None and payload.get("key_hash") == hex_hash)
    check("session rejects wrong secret", verify_session(token, "other") is None)


def test_key_policy_and_budget(tmp: Path) -> None:
    key_hash, cpamp_hash, _ = write_state(tmp / "state.json", "cpa_live", "cpa_disabled")
    state = KeyPolicyState.load(tmp / "state.json")
    record = state.get(key_hash)
    check("key policy finds key", record is not None and record.name == "Alice")
    check("key policy validates raw key hash", state.get_by_raw_hash(key_hash) is record)
    check("key policy cpamp hash uses policy id",
          record is not None and record.cpamp_hash == cpamp_hash)
    check("key policy finds cpamp hash", state.get_by_cpamp_hash(cpamp_hash) is record)
    check("key policy safe dict hides full hash",
          record is not None and key_hash not in json.dumps(record.safe_dict()))

    suggestion = suggest_equal_budget(state.enabled_keys(), total_daily_usd=10, total_weekly_usd=70)
    check("budget enabled count", suggestion.enabled_key_count == 1)
    check("budget daily assigned", suggestion.per_key_daily_usd == 10)
    check("budget patch uses policy hash", suggestion.patches[0]["key_hash"].startswith("sha256:"))

    bad_state = KeyPolicyState([record.__class__(**{**record.__dict__, "raw": {}})])
    try:
        suggest_equal_budget(bad_state.enabled_keys(), total_daily_usd=1)
        blocked = False
    except ValueError:
        blocked = True
    check("budget blocks missing prices", blocked)


def test_redaction_and_safe_event() -> None:
    expected = sha256_hex("cpa_live")
    other = sha256_hex("cpa_other")
    redacted = redact("Authorization: Bearer abc123 and api_key=secret")
    check("redact bearer", "abc123" not in redacted and "[REDACTED]" in redacted, redacted)
    safe = safe_event(
        {
            "api_key_hash": expected,
            "event_hash": "evt",
            "fail_summary": "access_token=secret",
            "failed": True,
        },
        expected_hash=expected,
    )
    check("safe event accepts own hash", safe is not None)
    check("safe event redacts failure", safe is not None and "secret" not in safe.get("failure", ""))
    check("safe event has short failure brief",
          safe is not None and safe.get("failure_brief") == "access_token=[REDACTED]",
          str(safe))
    header_blob = json.dumps({
        "Cf-Cache-Status": "DYNAMIC",
        "Set-Cookie": "secret-cookie",
        "Strict-Transport-Security": "max-age=31536000",
        "X-Codex-Plan-Type": "pro",
    })
    clean = safe_event(
        {
            "api_key_hash": expected,
            "failed": False,
            "fail_summary": header_blob,
        },
        expected_hash=expected,
    )
    check("safe event drops success header blob",
          clean is not None and clean.get("failure") == "" and clean.get("failure_brief") == "",
          str(clean))
    failed = safe_event(
        {
            "api_key_hash": expected,
            "failed": True,
            "fail_status_code": 502,
            "fail_summary": header_blob,
        },
        expected_hash=expected,
    )
    check("safe event summarizes failed header blob",
          failed is not None and failed.get("failure_brief") == "Upstream response headers omitted; inspect status code and quota fields.",
          str(failed))
    long_failure = safe_event(
        {
            "api_key_hash": expected,
            "failed": True,
            "fail_summary": "x" * 1000,
        },
        expected_hash=expected,
    )
    check("safe event bounds failure detail",
          long_failure is not None and len(long_failure.get("failure", "")) <= 600,
          str(long_failure))
    check("safe event rejects other hash",
          safe_event({"api_key_hash": other}, expected_hash=expected) is None)


def test_retention(tmp: Path) -> None:
    db = tmp / "usage.sqlite"
    conn = sqlite3.connect(db)
    conn.execute("create table usage_events (id integer primary key autoincrement, timestamp_ms integer not null)")
    conn.execute("insert into usage_events(timestamp_ms) values (?)", (1_000,))
    conn.execute("insert into usage_events(timestamp_ms) values (?)", (10_000_000_000,))
    conn.commit()
    conn.close()
    result = enforce_usage_retention(db, keep_days=7, batch_size=1, now_ms_value=10_000_000_000)
    verify = sqlite3.connect(db)
    try:
        count = verify.execute("select count(*) from usage_events").fetchone()[0]
    finally:
        verify.close()
    check("retention deletes old rows", result.deleted == 1 and count == 1, str(result))


def test_app_routes(tmp: Path) -> None:
    key_hash, cpamp_hash, other_raw_hash = write_state(tmp / "state.json", "cpa_live", "cpa_disabled")
    other_hash = sha256_hex("other-policy-id")
    cfg = PortalConfig(
        key_policy_state_path=str(tmp / "state.json"),
        cpamp_admin_key="cpamp_test",
        session_secret="session_secret",
        cookie_secure=False,
    )
    with TestClient(create_app(cfg)) as client:
        fake = FakeCPAMP(cpamp_hash, other_hash)
        client.app.state.cpamp = fake

        health = client.get("/healthz")
        check("portal healthz ok", health.status_code == 200 and health.json().get("ok") is True)

        bad = client.post("/api/session", json={"api_key": "nope"})
        check("portal rejects unknown key", bad.status_code == 401)

        login = client.post("/api/session", json={"api_key": "cpa_live"})
        check("portal login ok", login.status_code == 200, login.text)
        cookie = login.headers.get("set-cookie", "")
        check("portal cookie httponly", "HttpOnly" in cookie, cookie)
        check("portal cookie does not contain raw key", "cpa_live" not in cookie, cookie)

        me = client.get("/api/me")
        check("portal me ok", me.status_code == 200 and me.json()["me"]["name"] == "Alice", me.text)
        usage = client.get("/api/usage?range=24h")
        usage_body = usage.json()
        check("portal usage ok", usage.status_code == 200 and usage_body["summary"]["total_calls"] == 2)
        check("portal usage stat hides cpamp hash", cpamp_hash not in json.dumps(usage_body), str(usage_body))
        check("portal usage stats reject other hash",
              len(usage_body["api_key_stats"]) == 1 and usage_body["api_key_stats"][0]["calls"] == 2,
              str(usage_body))
        events = client.get("/api/events?limit=100")
        body = events.json()
        check("portal filters events to own key", len(body["events"]) == 1, str(body))
        check("portal never returns full raw api hash", key_hash not in json.dumps(body), str(body))
        check("portal does not use disabled raw hash", other_raw_hash not in json.dumps(body), str(body))
        check("portal cpamp filter used policy-id hash",
              fake.seen_hashes and all(h == cpamp_hash for h in fake.seen_hashes))


def main() -> None:
    test_hash_and_session()
    with tempfile.TemporaryDirectory() as d:
        test_key_policy_and_budget(Path(d))
    test_redaction_and_safe_event()
    with tempfile.TemporaryDirectory() as d:
        test_retention(Path(d))
    with tempfile.TemporaryDirectory() as d:
        test_app_routes(Path(d))

    passed = sum(1 for _, ok, _ in _RESULTS if ok)
    for name, ok, detail in _RESULTS:
        mark = "PASS" if ok else "FAIL"
        line = f"[{mark}] {name}"
        if not ok and detail:
            line += f"  -- {detail}"
        print(line)
    print(f"\n{passed}/{len(_RESULTS)} checks passed")
    sys.exit(0 if passed == len(_RESULTS) else 1)


if __name__ == "__main__":
    main()
