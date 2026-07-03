# Design

## Architecture Boundary

`cpa-key-policy-plus` remains the Key Policy and user-portal authority. The new
executor plugin owns only CodexCont continuation execution for streaming
Responses requests. This avoids conflating `cpa-usage.konbakuyomu.us` with the
sidecar replacement.

The intended cutover path is:

```text
Codex -> Caddy -> CPA
  -> cpa-key-policy-plus frontend auth / RPM / quota
  -> cpa-codexcont-executor model route + executor
  -> CPA host model stream
  -> upstream Responses API
```

## Executor Flow

- `model.route` handles only enabled, streaming Responses-style requests.
- `executor.execute_stream` opens the first upstream stream through the CPA host
  callback, reads upstream SSE chunks, and emits one folded downstream SSE
  stream through `host.stream.emit`.
- The folding state machine mirrors the Python sidecar: forward first
  lifecycle events, stream reasoning items, buffer tentative message/function
  output, inspect terminal usage, continue on `518*n-2` when encrypted reasoning
  is replayable, and emit one reconstructed terminal response.
- Continuation rounds rebuild the request body from the original input plus
  replayed reasoning and a hidden commentary marker. `previous_response_id` is
  dropped because state is carried explicitly.

## Summary Bridge

The executor plugin writes safe request summaries to its own SQLite store.
Plus may optionally read that store for `/user/api/codexcont`, filtered by the
current Key Policy key id. Plus never writes executor state, and executor never
owns Plus user sessions or quota state.

If the summary bridge is unavailable, Plus returns an empty/degraded protection
summary while keeping `/user/api/me`, `/user/api/usage`, and `/user/api/events`
healthy.

## Safety

The executor plugin must not store or return request bodies, response bodies,
Authorization headers, raw keys, cookies, OAuth tokens, or encrypted reasoning.
Only safe counters and status fields are persisted: request id, key id if known,
model, protection status, rounds, reasoning counters, continuation count,
stopped/failure reason, timestamps, and duration.

## Compatibility

`cpa-key-policy-plus` keeps `codexcont_route` deprecated/off. The executor
plugin owns the new route switch, so disabling continuation protection is a
one-setting operation without changing the user portal.
