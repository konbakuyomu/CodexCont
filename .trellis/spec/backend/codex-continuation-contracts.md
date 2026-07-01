# Codex Continuation and CPA Integration Contracts

## Scenario: Responses continuation middleware and CPA integration

### 1. Scope / Trigger
- Trigger this spec whenever work touches `/v1/responses` request handling, Codex encrypted reasoning replay, 516-style truncation detection, CPA integration, or proxy/egress deployment for Codex traffic.
- This is cross-layer work: request decoding, upstream SSE control flow, auth routing, Docker/Caddy networking, and downstream response compatibility all affect correctness.
- The SJC migration proved that replacing sub2api with CPA removes the old non-passthrough production path, but does not by itself solve the 516 continuation problem.

### 2. Signatures
- HTTP endpoint: `POST /v1/responses`
- Request headers:
  - `Content-Encoding: zstd` is accepted by the middleware and decoded before JSON parsing.
  - `Content-Encoding` must not be forwarded after decode because the forwarded body is plain JSON.
  - `Responses-API-Base` may override the upstream Responses base according to config mode.
- Continuation detector:
  - `reasoning_tokens == 518 * n - 2`
  - examples: `516`, `1034`, `1552`, `2070`, `2588`
- Production CPA route:
  - Public base URL: `https://cpa.konbakuyomu.us/`
  - CPA local bind: `127.0.0.1:8317`
  - Required production Codex egress: `socks5://172.19.0.1:1082`

### 3. Contracts
- A continuation round is allowed only when the terminal upstream response has the truncation-token fingerprint and the completed output includes replayable encrypted reasoning.
- Hidden continuation rounds must preserve the same selected OAuth account and proxy route. Do not rotate to another account inside one folded response; encrypted reasoning may be account-bound.
- Downstream clients must see one logical Responses stream, even if the middleware or CPA opens multiple upstream rounds.
- Reasoning items may be forwarded as reasoning, but tentative message/function-call output from a truncated round must stay buffered and must be discarded if a continuation round is opened.
- Final usage must distinguish agent-facing logical usage from billed upstream usage. Keep per-round metadata redacted; never log OAuth tokens, API keys, or encrypted reasoning payloads.
- CPA's ordinary stream chunk plugin surface is not enough for this feature because it can mutate/drop chunks after executor output, but cannot own upstream retry/continuation with the same auth context. A durable CPA-native implementation belongs in the Codex executor or in a new executor-level supervisor API.

### 4. Validation & Error Matrix
- Unsupported request `Content-Encoding` -> return `400` with an explicit decode error.
- Invalid zstd body -> return `400`; log content type, encoding, and byte length, but not body content.
- Truncation fingerprint without encrypted reasoning -> do not continue; flush the natural terminal response.
- Upstream EOF before terminal event -> emit an incomplete terminal response and do not leak buffered tentative text/tool calls.
- Continuation cap reached -> stop and report the cap reason in metadata.
- CPA egress route not reachable from the CPA container -> fail deployment validation; attach CPA to the required Docker network rather than falling back to direct egress.

### 5. Good/Base/Bad Cases
- Good: Round 1 ends with `reasoning_tokens=516`, has encrypted reasoning, and tentative text. The middleware replays reasoning plus a hidden marker into Round 2, discards Round 1 text, and downstream receives only the folded final answer.
- Base: A normal upstream response has no truncation fingerprint. The middleware acts as a transparent stream proxy.
- Bad: A downstream plugin sees `response.completed` and tries to start another upstream request after chunks have already been emitted. This leaks partial output and cannot guarantee same-auth replay.

### 6. Tests Required
- Unit: detector accepts `518 * n - 2` values and rejects adjacent values.
- Unit: zstd request bodies decode before JSON parsing, and `Content-Encoding` is dropped from forwarded headers.
- Stream fixture: truncated round followed by clean round produces one created event, one terminal event, monotonic sequence numbers, two reasoning items, and only the final clean answer.
- Stream fixture: truncated function call is discarded; clean function call is flushed.
- Stream fixture: EOF without terminal event emits `response.incomplete` and does not leak buffered text.
- Integration: CPA production smoke must verify `/healthz`, authenticated `/v1/models`, authenticated `/v1/responses`, and egress evidence through `172.19.0.1:1082`.

### 7. Wrong vs Correct

#### Wrong
```text
client -> CPA ordinary StreamChunkInterceptor plugin -> open ad hoc second request
```

This is too late in the pipeline: the plugin only sees downstream-bound chunks and cannot safely reuse the executor-selected Codex auth/proxy context.

#### Correct
```text
client -> continuation supervisor -> Codex executor same-auth upstream rounds -> folded downstream stream
```

The continuation owner must sit at executor level, where it can inspect raw upstream SSE, preserve auth/proxy identity, replay encrypted reasoning, and reconstruct the final logical response.
