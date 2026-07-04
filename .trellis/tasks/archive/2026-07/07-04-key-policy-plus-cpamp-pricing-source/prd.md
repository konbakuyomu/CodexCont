# CPA Usage CPAMP Pricing Source

## Goal

Make CPA Key Policy+ usage billing follow the same CPAMP model-price and Fast/Priority tier cost semantics shown in CPAMP request monitoring, so `https://cpa-usage.konbakuyomu.us/` and Plus quota windows stop undercounting Fast-mode Codex traffic.

## Requirements

- Treat CPAMP `model_prices` as the source of truth for Plus usage cost calculation.
- Stop relying on per-key Plus price books for request cost and quota-window spending.
- Preserve Plus ownership of enabled state, RPM, model allowlists, and 5H/24H/7D/month quota limits.
- Apply CPAMP-compatible `service_tier=priority/fast` multipliers, including `gpt-5.5 = 2.5x`.
- Read CPAMP pricing SQLite in read-only mode with busy timeout; cache the latest successful price snapshot in the Plus main DB.
- Use the cached snapshot when CPAMP pricing is temporarily unavailable; do not fail live requests solely because pricing cannot be refreshed.
- Recalculate current-month Plus usage events after a valid CPAMP price snapshot is available, so quota windows reflect the corrected cost.
- Remove ordinary admin UI editing for per-key model prices; show pricing source/updated status and missing-price warnings instead.
- Keep public APIs and user-page routes compatible.

## Acceptance Criteria

- [ ] `/user/api/usage` and `/user/api/events` show costs aligned with CPAMP for `gpt-5.5 priority/fast`.
- [ ] Plus quota windows use the same corrected costs for 5H/24H/7D/month checks.
- [ ] `usageHandle` no longer needs `KeyRecord.Prices` to calculate request cost.
- [ ] CPAMP DB read failure falls back to the last cached snapshot and surfaces a diagnostic warning.
- [ ] Cold start with no price source/cache keeps requests working but marks cost as unavailable/zero instead of inventing a cost.
- [ ] Plus admin UI no longer exposes per-key price editing controls.
- [ ] `go test ./... -count=1 -timeout=120s` passes in `cpa_key_policy_plus_plugin/go`.
- [ ] `git diff --check` passes.

## Notes

- "Official billing" in this task means CPAMP local estimation semantics, not upstream provider invoices.
- Do not modify CPA/CPAMP public APIs for this task.
