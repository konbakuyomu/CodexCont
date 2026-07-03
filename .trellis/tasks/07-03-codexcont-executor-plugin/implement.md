# Implementation Plan

1. Reuse the existing Governor plugin scaffolding as the base for a dedicated
   executor-only plugin, but remove user-portal ownership from the executor
   concept and naming.
2. Add a Go continuation package with the old Python sidecar behavior:
   truncation math, SSE parse/serialize helpers, request payload rebuild,
   usage aggregation, terminal reconstruction, and summary projection.
3. Wire `model.route` and `executor.execute_stream` so the route switch handles
   only streaming Responses requests and falls back cleanly when disabled.
4. Persist safe summaries in the executor store and add a read-only Plus
   summary bridge from `cpa-key-policy-plus` to the executor store path.
5. Update docs/specs to state that `cpa-usage.konbakuyomu.us` is Plus-owned and
   executor-only plugins replace only the Docker CodexCont sidecar.
6. Add focused Go tests for executor folding and Plus summary degradation.
7. Run:
   - `go test ./...` in the executor plugin package.
   - `go test ./...` in `cpa_key_policy_plus_plugin/go`.
   - `git diff --check`.

## Risk Points

- Stream folding must emit exactly one terminal event and keep downstream
  sequence numbers monotonic.
- Tentative output from truncated rounds must never leak before a continuation
  decision.
- Summary bridging must fail soft so user quota and usage APIs are unaffected.
- The plugin must not accidentally register `cpa-usage` user resources.

## Execution Evidence

- Added `cpa_codexcont_executor_plugin` as a separate executor-only CPA plugin.
- Added Go folding coverage for:
  - two-round auto continuation,
  - missing encrypted reasoning,
  - upstream EOF,
  - continuation open error,
  - `max_continue`,
  - monotonic sequence numbers and reconstructed metadata.
- Added plugin registration/route-switch tests confirming the executor plugin
  does not expose user resources or usage-portal ownership.
- Added Plus `codex_summary_db_path` read-only bridge and tests proving
  executor summaries are filtered by the current key.
- Validation:
  - `go test ./...` in `cpa_codexcont_executor_plugin/go`: passed.
  - `go test ./...` in `cpa_key_policy_plus_plugin/go`: passed.
  - `git diff --check`: passed with only CRLF conversion warnings.
