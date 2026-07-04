# Implementation Plan

1. [x] Add CPAMP price book types/loaders in Plus backend.
2. [x] Add cost calculation helper with CPAMP-compatible service-tier multipliers.
3. [x] Cache live CPAMP price snapshots in Plus settings and expose pricing status in admin/user responses.
4. [x] Change `usageHandle` and current-month event recalculation to use the global CPAMP price book.
5. [x] Simplify admin HTML/JS to remove price editing and show pricing source/missing-price status.
6. [x] Add/update Go tests for loader fallback, multiplier cost, usage handling, recalculation, and admin UI text.
7. [x] Run `go test ./... -count=1 -timeout=120s` in `cpa_key_policy_plus_plugin/go`.
8. [x] Run `git diff --check`.

See `evidence.md` for command output summary, Linux ABI build path, and SHA256.

## Rollback Notes

The main rollback point is the deployed `cpa-key-policy-plus.so`. Keep DB changes additive and settings-based so the old plugin can still read existing rows.
