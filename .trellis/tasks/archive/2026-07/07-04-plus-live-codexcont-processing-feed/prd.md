# Plus live CodexCont processing feed

## Goal

Make `cpa-usage.konbakuyomu.us` show the same real-time CodexCont protection
lifecycle as the CPAMP `CodexCont Executor` admin page, while still limiting the
user page to the currently logged-in key.

## Requirements

- The Plus user page `思维链保护` tab must show current-key `processing`
  records while a request is still streaming, not only completed terminal
  records.
- CPAMP admin may continue to show all keys; the Plus user page must not leak
  other keys or unknown-identity records.
- Executor summaries stored for Plus must include only safe key identity:
  key id/name/alias/preview/source, never raw keys, full hashes, headers,
  OAuth tokens, request bodies, response bodies, or encrypted reasoning.
- A processing placeholder must transition cleanly to the terminal summary on
  success, and to a failed terminal summary on executor/host failure.
- Stale processing rows from crashes/restarts must not remain active forever in
  the user page.
- Existing `/user/api/codexcont` contract and CPAMP admin page routes remain
  compatible.

## Acceptance Criteria

- [x] New current-key requests appear as `处理中` in `cpa-usage` before they
      complete.
- [x] After completion, the user page row changes from `processing` to the
      final protection state without showing a duplicate placeholder.
- [x] Failed executor requests become visible as failed terminal rows rather
      than disappearing or staying stuck in processing.
- [x] Other-key processing rows do not appear for the logged-in user.
- [x] Stale processing rows are filtered or downgraded so the active counter is
      not misleading after a restart.
- [x] Go tests cover executor persistence and Plus filtering.

## Notes

- Evidence: CPAMP admin `/summaries` combines `summaryMonitor.Start(...)`
  in-memory rows with SQLite summaries; Plus `/user/api/codexcont` reads the
  Executor SQLite bridge through `RecentCodexSummariesFromSQLite(...)`.
- Root cause: Executor currently persists summaries only in `saveFoldSummary`
  after `FoldStream` returns, so Plus can only see terminal rows.
- Out of scope: changing the public/admin route boundary, exposing all-key data
  to users, or changing quota/key policy behavior.
- Local validation:
  - `go test ./... -count=1 -timeout=120s` passed in
    `cpa_codexcont_executor_plugin/go`.
  - `go test ./... -count=1 -timeout=120s` passed in
    `cpa_key_policy_plus_plugin/go`.
  - `git diff --check` passed.
- Production validation:
  - Deployed `cpa-codexcont-executor.so`
    `9dd795a6ed6b5e0dda33309a5069f3d4429b62b350e0943d91cfc832e343e28d`.
  - Deployed `cpa-key-policy-plus.so`
    `ab71dc26f9d1c1393312281fd867be8a352c54975fa7f5285f73850b30e2e0b2`.
  - Remote backup:
    `/opt/codex-stacks/backups/plus-live-codexcont-processing-feed-20260704-094131`.
  - `https://cpa-usage.konbakuyomu.us/` returned `200`.
  - Public `https://cpa.konbakuyomu.us/v0/resource/plugins/.../admin`
    returned `404` for executor and Plus admin paths.
  - Temporary current-key session for `kuma的官key` showed
    `source=codexcont_executor_store`, `request_count=10`, and
    `processing_count=3` from `/user/api/codexcont`.
