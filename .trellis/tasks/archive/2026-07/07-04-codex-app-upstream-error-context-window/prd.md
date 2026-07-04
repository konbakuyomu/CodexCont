# Codex App upstream error context window

## Goal

Stop Codex App requests from failing with the opaque streaming message
`stream disconnected before completion: Incomplete response returned, reason:
upstream_error` when the real upstream cause is a model context-window error.
The executor should route visible Codex models to matching upstream models by
default, and it should surface actionable upstream errors to the client and
admin monitor.

## Requirements

- Create a production-safe fix in `cpa-codexcont-executor`.
- Default executor routing must no longer silently downgrade visible `gpt-5.5`
  or `gpt-5.4` requests to `gpt-5.3-codex-spark`.
- Explicit `upstream_model` and `upstream_model_aliases` remain supported for
  administrators who intentionally want a mapping.
- When the host stream read path returns an OpenAI-compatible upstream error,
  the executor must preserve the useful error type/code/message in downstream
  SSE instead of collapsing it to only `upstream_error`.
- Unknown stream read failures, EOF before terminal event, and network-level
  interruptions must still be reported as incomplete upstream failures.
- Executor summaries may include safe diagnostics such as requested/upstream
  model, body size, and redacted/truncated upstream error fields, but must not
  store raw keys, full hashes, OAuth tokens, request bodies, response bodies, or
  encrypted reasoning.
- Deploy to SJC only after local tests and Linux plugin build pass.

## Acceptance Criteria

- [ ] Trellis task contains PRD, design, and implementation checklist before
      execution starts.
- [ ] Local executor tests prove default `gpt-5.5`/`gpt-5.4` routing is
      pass-through, while explicit aliases still work.
- [ ] Local executor tests prove context-window stream errors become explicit
      downstream error/failed events, not generic incomplete `upstream_error`.
- [ ] `go test ./... -count=1 -timeout=120s` passes in
      `cpa_codexcont_executor_plugin/go`.
- [ ] `git diff --check` passes.
- [ ] New Linux `cpa-codexcont-executor.so` is built and deployed with backup.
- [ ] SJC production config no longer maps `gpt-5.5`/`gpt-5.4` to Spark unless
      an explicit future admin choice re-adds it.
- [ ] Live streaming `/v1/responses` small request succeeds through
      `cpa -> Plus -> executor -> upstream`.
- [ ] Executor summary for the smoke request shows visible and upstream model
      as `gpt-5.5`, not `gpt-5.3-codex-spark`.
- [ ] Public admin/resource/plugin boundaries remain protected.

## Notes

- Evidence from planning: recent failed executor summaries showed
  `requested_model=gpt-5.5`, `body_model=gpt-5.3-codex-spark`,
  `body_bytes≈510887`, and upstream message `Your input exceeds the context
  window of this model...`.
- Evidence from planning: a direct non-stream CPA `/v1/responses` request using
  `gpt-5.5` returned `200 OK`, so the current OAuth/auth path can execute
  real `gpt-5.5`.
