"""Read-only projection of CPA Key Policy state."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .security import hash_preview, key_policy_hash, normalize_key_hash


def _as_list(value: Any) -> list[Any]:
    if isinstance(value, list):
        return value
    if isinstance(value, tuple):
        return list(value)
    if isinstance(value, str) and value.strip():
        return [item.strip() for item in value.split(",") if item.strip()]
    return []


def _first(raw: dict[str, Any], *names: str) -> Any:
    for name in names:
        if name in raw:
            return raw[name]
    return None


def _float_or_none(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _int_or_none(value: Any) -> int | None:
    if value is None or value == "":
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


@dataclass(frozen=True)
class KeyRecord:
    key_hash: str
    name: str
    enabled: bool
    policy_id: str | None = None
    rpm: int | None = None
    models: list[str] = field(default_factory=list)
    daily_limit_usd: float | None = None
    weekly_limit_usd: float | None = None
    daily_usage_usd: float | None = None
    weekly_usage_usd: float | None = None
    preview: str | None = None
    raw: dict[str, Any] = field(default_factory=dict)

    @property
    def raw_key_hash(self) -> str:
        return normalize_key_hash(self.key_hash)

    @property
    def cpamp_hash(self) -> str:
        if self.policy_id:
            # CPA Key Policy authenticates requests as key.ID. CPA Manager Plus
            # then hashes that principal, not the original cpa_... key.
            return hashlib.sha256(self.policy_id.strip().encode("utf-8")).hexdigest()
        return self.raw_key_hash

    def safe_dict(self) -> dict[str, Any]:
        return {
            "id": self.policy_id or self.name or hash_preview(self.raw_key_hash),
            "name": self.name,
            "enabled": self.enabled,
            "preview": self.preview or hash_preview(self.raw_key_hash),
            "rpm": self.rpm,
            "models": list(self.models),
            "limits": {
                "daily_usd": self.daily_limit_usd,
                "weekly_usd": self.weekly_limit_usd,
            },
            "usage": {
                "daily_usd": self.daily_usage_usd,
                "weekly_usd": self.weekly_usage_usd,
            },
        }


class KeyPolicyState:
    def __init__(self, keys: list[KeyRecord], *, source: Path | None = None) -> None:
        self.keys = keys
        self.source = source
        self._by_raw_hash = {item.raw_key_hash: item for item in keys}
        self._by_cpamp_hash = {item.cpamp_hash: item for item in keys}

    @classmethod
    def load(cls, path: str | Path) -> "KeyPolicyState":
        source = Path(path)
        data = json.loads(source.read_text(encoding="utf-8"))
        keys = [_parse_key(item) for item in _extract_keys(data)]
        return cls([key for key in keys if key is not None], source=source)

    def get(self, key_hash: str) -> KeyRecord | None:
        normalized = normalize_key_hash(key_hash)
        return self._by_raw_hash.get(normalized) or self._by_cpamp_hash.get(normalized)

    def get_by_raw_hash(self, key_hash: str) -> KeyRecord | None:
        return self._by_raw_hash.get(normalize_key_hash(key_hash))

    def get_by_cpamp_hash(self, key_hash: str) -> KeyRecord | None:
        return self._by_cpamp_hash.get(normalize_key_hash(key_hash))

    def enabled_keys(self) -> list[KeyRecord]:
        return [key for key in self.keys if key.enabled]


def _extract_keys(data: Any) -> list[dict[str, Any]]:
    if isinstance(data, list):
        return [item for item in data if isinstance(item, dict)]
    if not isinstance(data, dict):
        return []
    for path in (
        ("keys",),
        ("state", "keys"),
        ("data", "keys"),
        ("config", "keys"),
    ):
        current: Any = data
        for part in path:
            if not isinstance(current, dict):
                current = None
                break
            current = current.get(part)
        if isinstance(current, list):
            return [item for item in current if isinstance(item, dict)]
    return []


def _parse_key(raw: dict[str, Any]) -> KeyRecord | None:
    raw_hash = _first(raw, "key_hash", "keyHash", "hash", "api_key_hash", "apiKeyHash")
    if not isinstance(raw_hash, str) or not raw_hash.strip():
        return None
    try:
        normalized = normalize_key_hash(raw_hash)
    except ValueError:
        return None

    disabled = bool(_first(raw, "disabled", "is_disabled", "isDisabled") or False)
    enabled_raw = _first(raw, "enabled", "is_enabled", "isEnabled")
    enabled = bool(enabled_raw) if enabled_raw is not None else not disabled
    models = _as_list(_first(raw, "models", "allowed_models", "allowedModels", "model_allowlist", "modelAllowlist", "aliases"))
    name = str(_first(raw, "name", "label", "alias", "description") or "").strip()
    if not name:
        name = hash_preview(normalized)
    raw_id = _first(raw, "id", "key_id", "keyId")
    policy_id = str(raw_id).strip() if raw_id is not None else None

    return KeyRecord(
        key_hash=key_policy_hash(normalized),
        name=name,
        enabled=enabled and not disabled,
        policy_id=policy_id or None,
        rpm=_int_or_none(_first(raw, "rpm", "rpm_limit", "rpmLimit", "rpm_per_minute", "rpmPerMinute")),
        models=[str(item) for item in models],
        daily_limit_usd=_float_or_none(_first(raw, "daily_limit_usd", "dailyLimitUsd")),
        weekly_limit_usd=_float_or_none(_first(raw, "weekly_limit_usd", "weeklyLimitUsd")),
        daily_usage_usd=_float_or_none(_first(raw, "daily_usage_usd", "dailyUsageUsd")),
        weekly_usage_usd=_float_or_none(_first(raw, "weekly_usage_usd", "weeklyUsageUsd")),
        preview=str(_first(raw, "preview", "key_preview", "keyPreview") or "").strip() or None,
        raw=dict(raw),
    )


def has_model_prices(record: KeyRecord) -> bool:
    prices = _first(record.raw, "model_prices", "modelPrices", "prices")
    if isinstance(prices, dict) and prices:
        return True
    if isinstance(prices, list) and prices:
        return True
    return False
