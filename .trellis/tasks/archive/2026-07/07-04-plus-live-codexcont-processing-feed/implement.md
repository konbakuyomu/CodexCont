# Implementation Plan

## Steps

1. Add executor store support for deleting a summary row by explicit request id.
2. Persist a safe processing summary when `summaryMonitor.Start` creates one.
3. On successful finish, delete the processing placeholder when the final
   `request_id` differs, then save the final summary.
4. On failure, persist the failed terminal summary for the same processing id.
5. Add Plus-side stale processing filtering for executor SQLite summaries.
6. Add Go tests:
   - executor persists processing rows with safe key identity;
   - executor success removes the processing placeholder;
   - executor failure persists a failed row;
   - Plus shows current-key processing rows;
   - Plus hides other-key and stale processing rows.
7. Run:
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_codexcont_executor_plugin/go`
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_key_policy_plus_plugin/go`
   - `git diff --check`

## Rollout Notes

If deployed to SJC, build both touched plugin artifacts with the existing WSL
Go 1.22.6 toolchain under `_LocalRuntime`, record SHA256, back up the remote
`.so` files, restart only `cpa`, and verify both CPAMP admin and `cpa-usage`
show the same current-key processing lifecycle.

## Rollback

Rollback is replacing the executor and Plus plugin `.so` files with the backup
artifacts and restarting `cpa`. No schema migration is destructive; processing
rows are ordinary summary rows.
