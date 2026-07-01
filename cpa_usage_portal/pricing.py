"""Per-key local cost projection using CPA Key Policy prices."""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .key_policy import ModelPrice

PER_MILLION = 1_000_000.0


@dataclass(frozen=True)
class ModelTokens:
    input_tokens: int = 0
    output_tokens: int = 0
    cached_tokens: int = 0
    cache_read_tokens: int = 0
    cache_creation_tokens: int = 0


def _number(value: Any) -> float:
    try:
        return float(value or 0)
    except (TypeError, ValueError):
        return 0.0


def _int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def tokens_from_row(row: dict[str, Any]) -> ModelTokens:
    return ModelTokens(
        input_tokens=_int(row.get("input_tokens") or row.get("inputTokens")),
        output_tokens=_int(row.get("output_tokens") or row.get("outputTokens")),
        cached_tokens=_int(row.get("cached_tokens") or row.get("cachedTokens")),
        cache_read_tokens=_int(row.get("cache_read_tokens") or row.get("cacheReadTokens")),
        cache_creation_tokens=_int(row.get("cache_creation_tokens") or row.get("cacheCreationTokens")),
    )


def price_for_model(
    price_book: dict[str, ModelPrice],
    *model_names: Any,
) -> tuple[str, ModelPrice] | None:
    candidates = [str(item or "").strip() for item in model_names if str(item or "").strip()]
    for candidate in candidates:
        if candidate in price_book:
            return candidate, price_book[candidate]
    lower_index = {name.lower(): (name, price) for name, price in price_book.items()}
    for candidate in candidates:
        found = lower_index.get(candidate.lower())
        if found is not None:
            return found
    return None


def cost_for_tokens(price: ModelPrice, tokens: ModelTokens, *, model: str = "", service_tier: str = "") -> float:
    input_tokens = max(tokens.input_tokens, 0)
    output_tokens = max(tokens.output_tokens, 0)
    cached_tokens = max(tokens.cached_tokens, 0)
    cache_read_tokens = max(tokens.cache_read_tokens, 0)
    cache_creation_tokens = max(tokens.cache_creation_tokens, 0)
    prompt_tokens = max(input_tokens - cached_tokens, 0)
    cache_read_price = price.cache_read_per_million or price.input_per_million
    cache_creation_price = price.cache_creation_per_million or price.input_per_million
    cost = (
        prompt_tokens * price.input_per_million / PER_MILLION
        + output_tokens * price.output_per_million / PER_MILLION
        + cached_tokens * (price.cache_read_per_million or price.input_per_million) / PER_MILLION
        + cache_read_tokens * cache_read_price / PER_MILLION
        + cache_creation_tokens * cache_creation_price / PER_MILLION
    )
    return cost * service_tier_multiplier(model or price.model, service_tier)


def cost_for_row(
    row: dict[str, Any],
    price_book: dict[str, ModelPrice],
    *,
    model_fields: tuple[str, ...] = ("model", "resolved_model", "requested_model"),
) -> tuple[float, str] | None:
    matched = price_for_model(price_book, *(row.get(field) for field in model_fields))
    if matched is None:
        return None
    model, price = matched
    return cost_for_tokens(price, tokens_from_row(row), model=model, service_tier=str(row.get("service_tier") or "")), model


def service_tier_multiplier(model_name: str, service_tier: str) -> float:
    tier = str(service_tier or "").strip().lower()
    if tier not in {"priority", "fast"}:
        return 1.0
    model = str(model_name or "").strip().lower()
    if _is_family(model, "gpt-5.5"):
        return 2.5
    if _is_family(model, "gpt-5.4-mini"):
        return 2.0
    if _is_family(model, "gpt-5.4"):
        return 2.0
    if _is_family(model, "gpt-5.3-codex"):
        return 2.0
    return 1.0


def _is_family(model: str, family: str) -> bool:
    return model == family or model.startswith(family + "-")


def apply_key_policy_pricing(data: dict[str, Any], price_book: dict[str, ModelPrice]) -> dict[str, Any]:
    if not price_book:
        return data
    projected = dict(data)
    model_stats = [dict(row) for row in projected.get("model_stats") or [] if isinstance(row, dict)]
    cost_by_model: dict[str, float] = {}
    unpriced: set[str] = set()
    for row in model_stats:
        model = str(row.get("model") or "").strip()
        priced = cost_for_row(row, price_book, model_fields=("model",))
        if priced is None:
            if model:
                unpriced.add(model)
            continue
        cost, matched_model = priced
        row["cost"] = cost
        row["cost_source"] = "key_policy"
        cost_by_model[model or matched_model] = cost_by_model.get(model or matched_model, 0.0) + cost

    model_share = [dict(row) for row in projected.get("model_share") or [] if isinstance(row, dict)]
    for row in model_share:
        model = str(row.get("model") or "").strip()
        if model in cost_by_model:
            row["cost"] = cost_by_model[model]
            row["cost_source"] = "key_policy"
            continue
        priced = cost_for_row(row, price_book, model_fields=("model",))
        if priced is None:
            if model:
                unpriced.add(model)
            continue
        cost, _ = priced
        row["cost"] = cost
        row["cost_source"] = "key_policy"
        cost_by_model[model] = cost_by_model.get(model, 0.0) + cost

    if model_stats:
        total_cost = sum(cost_by_model.values())
    else:
        total_cost = sum(_number(row.get("cost")) for row in model_share)
    summary = dict(projected.get("summary") or {})
    summary["total_cost"] = total_cost
    summary["cost_source"] = "key_policy"
    summary["unpriced_models"] = sorted(unpriced)

    projected["summary"] = summary
    projected["model_share"] = model_share
    projected["model_stats"] = model_stats
    return projected


def apply_event_pricing(events: list[dict[str, Any]], price_book: dict[str, ModelPrice]) -> list[dict[str, Any]]:
    if not price_book:
        return events
    projected: list[dict[str, Any]] = []
    for event in events:
        row = dict(event)
        priced = cost_for_row(row, price_book, model_fields=("model", "requested_model"))
        if priced is not None:
            cost, matched_model = priced
            row["cost"] = cost
            row["cost_source"] = "key_policy"
            row["price_model"] = matched_model
        projected.append(row)
    return projected
