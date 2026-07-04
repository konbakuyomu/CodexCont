# Implementation Plan

## Steps

1. Extend Plus `safeCodexSummary` with the approved safe fields.
2. Update `assets/user.html`:
   - render key name and preview in protection detail;
   - render start/update/end times;
   - add the rounds detail card;
   - prefer `updated_at || started_at` in the table time column.
3. Add Go tests for:
   - safe projection includes new safe fields and rounds;
   - user HTML contains the rounds rendering hook and updated timestamp order.
4. Run:
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_key_policy_plus_plugin/go`;
   - `git diff --check`.
5. Build Linux `cpa-key-policy-plus.so` with the existing WSL Go 1.22.6
   toolchain.
6. Deploy only Plus plugin to SJC, back up the previous `.so`, restart only
   `cpa`, and validate:
   - current-key `/user/api/codexcont` includes new fields;
   - `cpa-usage` HTML contains rounds rendering;
   - public plugin/admin paths remain `404`.

## Rollback

Restore the backed-up `cpa-key-policy-plus.so` and restart `cpa`. No schema
changes are involved.
