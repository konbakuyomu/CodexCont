# Design

## Root Cause

The executor default config currently injects aliases from visible Codex
models `gpt-5.5` and `gpt-5.4` to `gpt-5.3-codex-spark`. Codex App sees model
metadata for the larger visible model, sends a large context, and the smaller
Spark upstream rejects it with a context-window error. The executor then treats
the host stream read error as an opaque upstream stream failure and emits a
generic incomplete response.

## Model Routing

- Change executor defaults so `UpstreamModelAliases` is empty.
- Keep `upstream_model` as a global force override.
- Keep `upstream_model_aliases` as an explicit per-model override, without
  merging hidden defaults back in during `Normalize`.
- Continue to filter Spark-incompatible built-in tools only when the explicitly
  resolved upstream model is Spark.

## Stream Error Translation

- Add a small OpenAI-compatible error parser for stream read errors. It should
  recognize JSON bodies shaped like `{"error":{...}}`, top-level error events,
  and response failed events when available.
- If the parsed error looks like context-window exhaustion, emit a
  `response.failed` SSE with `response.error.code=context_too_large` or the
  upstream code, and preserve a short upstream message.
- For non-parseable read errors, keep existing incomplete upstream behavior.
- Save safe diagnostics fields for parsed upstream error type/code/message
  without storing request/response bodies.

## Compatibility

- No official CPA/CPAMP source changes.
- No user portal ownership change.
- Existing production configs with explicit aliases still work, but SJC config
  must remove the old Spark aliases as part of rollout.
- Existing summaries remain historical evidence and do not need migration.
