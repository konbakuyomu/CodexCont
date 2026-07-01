"""In-process diagnostics for the CodexCont admin dashboard."""
from __future__ import annotations

import asyncio
import re
import threading
import time
import uuid
from collections import deque
from datetime import UTC, datetime
from typing import Any

_SECRET_KEYWORDS = (
    "authorization",
    "api-key",
    "api_key",
    "apikey",
    "access_token",
    "refresh_token",
    "id_token",
    "tunnel_token",
    "bearer_token",
    "oauth",
    "secret",
    "encrypted_content",
)
_BEARER_RE = re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+")
_KEY_VALUE_RE = re.compile(
    r"(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|secret)"
    r"\s*[:=]\s*['\"]?[^'\"\s,;]+"
)


def utc_now_iso() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


def redact_value(value: Any, *, key: str = "") -> Any:
    """Return a JSON-safe value with obvious secrets removed."""
    key_l = key.lower()
    if key_l == "token" or key_l.endswith("_token") or any(part in key_l for part in _SECRET_KEYWORDS):
        return "[REDACTED]"
    if isinstance(value, str):
        text = _BEARER_RE.sub("Bearer [REDACTED]", value)
        return _KEY_VALUE_RE.sub(lambda m: f"{m.group(1)}=[REDACTED]", text)
    if isinstance(value, dict):
        return {str(k): redact_value(v, key=str(k)) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [redact_value(v) for v in value]
    if isinstance(value, (int, float, bool)) or value is None:
        return value
    return str(value)


class Diagnostics:
    """Small memory-only metrics and event hub.

    This intentionally does not write to disk. It is safe for the small SJC VPS
    and simple enough to keep CodexCont independent from CPA internals.
    """

    def __init__(self, *, max_events: int = 800) -> None:
        self.max_events = max(1, int(max_events))
        self._events: deque[dict[str, Any]] = deque(maxlen=self.max_events)
        self._subscribers: set[asyncio.Queue[dict[str, Any]]] = set()
        self._lock = threading.RLock()
        self._seq = 0
        self._started_wall = utc_now_iso()
        self._started_perf = time.monotonic()
        self._active_ids: set[str] = set()
        self._counters: dict[str, int] = {
            "total_requests": 0,
            "active_requests": 0,
            "folded_requests": 0,
            "passthrough_requests": 0,
            "continuations": 0,
            "truncation_hits": 0,
            "failures": 0,
        }
        self._last_request_at: str | None = None
        self._last_continuation_at: str | None = None
        self._last_error_at: str | None = None
        self._last_error: dict[str, Any] | None = None
        self._request_meta: dict[str, dict[str, Any]] = {}

    def recent(self, *, limit: int | None = None) -> list[dict[str, Any]]:
        with self._lock:
            events = list(self._events)
        if limit is None:
            return events
        limit = max(0, min(int(limit), self.max_events))
        return events[-limit:]

    def subscribe(self) -> asyncio.Queue[dict[str, Any]]:
        queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue(maxsize=200)
        with self._lock:
            self._subscribers.add(queue)
        return queue

    def unsubscribe(self, queue: asyncio.Queue[dict[str, Any]]) -> None:
        with self._lock:
            self._subscribers.discard(queue)

    def record(self, level: str, event: str, message: str = "", **fields: Any) -> dict[str, Any]:
        item = {
            "seq": 0,
            "ts": utc_now_iso(),
            "level": (level or "info").lower(),
            "event": event,
            "message": redact_value(message),
            "fields": redact_value(fields),
        }
        with self._lock:
            self._seq += 1
            item["seq"] = self._seq
            self._events.append(item)
            subscribers = list(self._subscribers)

        for queue in subscribers:
            try:
                queue.put_nowait(item)
            except asyncio.QueueFull:
                try:
                    queue.get_nowait()
                except asyncio.QueueEmpty:
                    pass
                try:
                    queue.put_nowait(item)
                except asyncio.QueueFull:
                    pass
        return item

    def request_started(self, *, path: str, model: str | None = None) -> str:
        request_id = uuid.uuid4().hex[:12]
        now = utc_now_iso()
        with self._lock:
            self._active_ids.add(request_id)
            self._counters["total_requests"] += 1
            self._counters["active_requests"] = len(self._active_ids)
            self._last_request_at = now
            self._request_meta[request_id] = {
                "request_id": request_id,
                "path": path,
                "model": model,
                "started_at": now,
            }
        self.record("info", "request_started", "Responses request received",
                    request_id=request_id, path=path, model=model)
        return request_id

    def request_update(self, request_id: str, **fields: Any) -> None:
        with self._lock:
            meta = self._request_meta.get(request_id)
            if meta is not None:
                meta.update({k: v for k, v in fields.items() if v is not None})

    def mark_fold_start(self, request_id: str, *, model: Any, path: str, upstream_url: str) -> None:
        with self._lock:
            self._counters["folded_requests"] += 1
        self.record("info", "fold_start", "Folded Responses stream started",
                    request_id=request_id, model=model, path=path, upstream_url=upstream_url)

    def mark_passthrough(self, request_id: str, *, reason: str, model: Any) -> None:
        with self._lock:
            self._counters["passthrough_requests"] += 1
        self.record("info", "passthrough", "Request passed through without folding",
                    request_id=request_id, reason=reason, model=model)

    def round_decision(
        self,
        request_id: str,
        *,
        round_no: int,
        reasoning_tokens: int | None,
        n: int | None,
        decision: str,
        buffered: list[str],
        truncation_match: bool,
    ) -> None:
        if truncation_match:
            with self._lock:
                self._counters["truncation_hits"] += 1
        self.record(
            "info",
            "round_decision",
            "Round finished and continuation decision was made",
            request_id=request_id,
            round=round_no,
            reasoning_tokens=reasoning_tokens,
            n=n,
            decision=decision,
            buffered=buffered,
            truncation_match=truncation_match,
        )

    def continuation_opened(self, request_id: str, *, from_round: int, next_round: int, method: str) -> None:
        now = utc_now_iso()
        with self._lock:
            self._counters["continuations"] += 1
            self._last_continuation_at = now
        self.record("info", "continuation_opened", "Opened hidden continuation round",
                    request_id=request_id, from_round=from_round, next_round=next_round, method=method)

    def request_finished(self, request_id: str, *, status: str, stopped_reason: str | None = None) -> None:
        with self._lock:
            self._active_ids.discard(request_id)
            self._counters["active_requests"] = len(self._active_ids)
            self._request_meta.pop(request_id, None)
        self.record("info", "request_finished", "Responses request finished",
                    request_id=request_id, status=status, stopped_reason=stopped_reason)

    def request_failed(self, request_id: str, *, reason: str, detail: Any = None) -> None:
        now = utc_now_iso()
        error = {"request_id": request_id, "reason": reason, "detail": redact_value(detail)}
        with self._lock:
            self._active_ids.discard(request_id)
            self._counters["active_requests"] = len(self._active_ids)
            self._counters["failures"] += 1
            self._last_error_at = now
            self._last_error = error
            self._request_meta.pop(request_id, None)
        self.record("warning", "request_failed", "Responses request failed", **error)

    def health(self) -> dict[str, Any]:
        return {
            "ok": True,
            "started_at": self._started_wall,
            "uptime_seconds": round(time.monotonic() - self._started_perf, 3),
        }

    def snapshot(
        self,
        *,
        upstream: dict[str, Any] | None = None,
        config: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        with self._lock:
            counters = dict(self._counters)
            active = list(self._request_meta.values())
            last_error = dict(self._last_error) if self._last_error else None
            last_request_at = self._last_request_at
            last_continuation_at = self._last_continuation_at
            last_error_at = self._last_error_at
        return {
            **self.health(),
            "counters": counters,
            "active_requests": active,
            "last_request_at": last_request_at,
            "last_continuation_at": last_continuation_at,
            "last_error_at": last_error_at,
            "last_error": last_error,
            "upstream": upstream or {"ok": None, "status": "not_checked"},
            "config": config or {},
        }
