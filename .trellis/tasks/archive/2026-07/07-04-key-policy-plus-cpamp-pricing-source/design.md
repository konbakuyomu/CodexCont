# Design

## Pricing Source

CPA Key Policy+ will add a read-only CPAMP price book loader. The loader reads `model_prices` from configured `cpamp_price_db_path(s)` and falls back to existing `cpamp_alias_db_path(s)` when no price path is configured. Reads use the existing SQLite read-only helper style with `busy_timeout`.

The successful snapshot is cached in Plus `settings` as JSON with a timestamp. Runtime billing prefers the live CPAMP read, then the cached snapshot, then an empty unavailable price book.

## Cost Semantics

Plus will add a shared cost helper equivalent to CPAMP's pricing package:

- price fields map from CPAMP `prompt/completion/cache/cacheRead/cacheCreation` to Plus cost breakdown fields
- cached and fine-grained cache buckets are handled like CPAMP
- `service_tier=priority` and `service_tier=fast` apply multipliers: `gpt-5.5` 2.5x; `gpt-5.4`, `gpt-5.4-mini`, and `gpt-5.3-codex` 2x

`usageHandle` will calculate cost from the CPAMP price book using the visible/requested model and recorded service tier. The cost breakdown source is `cpamp_price_book` for live snapshots, `cpamp_cached_price_book` for cached snapshots, and `cpamp_price_unavailable` when no price exists.

## Historical Recalculation

After a successful price snapshot load, Plus will recalculate current-month `usage_events` rows using stored token counts, model, and service tier. It updates only `cost` and `cost_breakdown_json`; it does not delete events or rewrite identities.

This keeps quota checks consistent without reprocessing older archival history.

## Admin UX

The Plus admin page keeps model allowlists, RPM, enabled state, and quota inputs. Per-key model price editing controls are removed from the ordinary UI. The detail panel shows pricing source and warnings for selected allowlist models that are missing from the CPAMP price book.

Backend save remains backward compatible with old payloads that contain `prices`, but ignores per-key price edits going forward.

