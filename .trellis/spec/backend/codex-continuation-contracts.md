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

## Scenario: CodexCont admin diagnostics dashboard and request summaries

### 1. Scope / Trigger
- Trigger this spec whenever work touches CodexCont `/admin/*` routes, in-process diagnostics, SSE admin streams, dashboard UI, or production deployment of the CodexCont sidecar behind CPA.
- This is a cross-layer contract: `/v1/responses` lifecycle events feed `Diagnostics`, `Diagnostics` projects request summaries, Starlette exposes JSON/SSE admin APIs, and the static dashboard renders beginner-facing protection status.
- The admin dashboard is observability for the 516 continuation mitigation. It must never become a second control plane that mutates CPA, OAuth accounts, proxy routes, or request payloads.

### 2. Signatures
- Admin routes:
  - `GET /admin/healthz` -> service health and uptime.
  - `GET /admin/status` -> counters, active request metadata, upstream health, and safe config summary.
  - `GET /admin/requests?limit=N` -> recent request-level protection summaries.
  - `GET /admin/logs?limit=N` -> recent redacted diagnostic log events.
  - `GET /admin/logs/stream` -> SSE stream with `event: ready`, `event: request`, and `event: log`.
  - `GET /admin/` -> static dashboard HTML.
- Request summary projection fields:
  - `request_id`, `model`, `path`, `started_at`, `updated_at`, `ended_at`, `duration_ms`
  - `status`, `protection`, `folded`, `passthrough`, `passthrough_reason`
  - `rounds[]`, `latest_round`, `latest_reasoning_tokens`
  - `first_truncation_round`, `first_truncation_reasoning_tokens`, `first_truncation_n`, `first_truncation_decision`, `continuation_count`
  - `truncation_match`, `final_status`, `stopped_reason`, `failure_reason`, `failure_detail`
  - optional safe `key_identity`: `known`, `name`, `id`, `preview`, `source`,
    `enabled`
- Protection values:
  - `protected_clean`, `auto_continued`, `risk_uncontinued`, `passthrough`, `failed`, `incomplete`, `processing`

### 3. Contracts
- `Diagnostics` owns the request-summary projection. The frontend may format labels, but it must not re-derive protection status from raw log event names or ad hoc field parsing.
- Admin data is memory-only. Do not add persistent log files, databases, Redis, or CPA Manager dependencies for dashboard v1/v2 behavior.
- Request summaries and logs must not include request bodies, Authorization headers, API keys, OAuth tokens, encrypted reasoning content, or internal implementation-only fields such as `_started_perf`.
- If CodexCont is configured with a Key Policy state path, request summaries
  may include safe key identity. The resolver must hash a bearer credential
  only long enough to match Key Policy state, then discard the raw key and
  expose only safe name/preview/source fields.
- `event: log` behavior is backward-compatible with the original dashboard stream. Adding request updates must use a separate `event: request` SSE event.
- The beginner-facing dashboard must distinguish "entered CodexCont protection and no continuation was needed" from "516/518n-2 was detected and a hidden continuation round was opened".
- When a continued request ends with a clean final round, `latest_reasoning_tokens` may be below 516. The dashboard must label it as latest-round reasoning and separately display the first 516/518n-2 trigger round from the request summary.
- The dashboard frontend must treat the SSE connection as recoverable browser state, not as a durable data source. Manual refresh and foreground resume (`visibilitychange`, `pageshow`, or stale `focus`) must re-fetch the JSON snapshots with no-store/cache-bust semantics and force-create a new `EventSource`. Late responses from older fetches must not overwrite newer snapshots.
- The refresh action must provide visible busy/completion feedback, and the realtime connection chip must animate in all states (`connected`, `connecting/reconnecting`, and `disconnected`) so an operator can see that the page is alive after returning from an idle tab.
- When an SSE request update is still `processing`, the dashboard should
  immediately refresh status counters and follow up with short delayed
  `/admin/requests` snapshot reloads. Do not rely only on the next long polling
  interval to clear processing rows.
- Production admin access must remain behind `cpa-admin.konbakuyomu.us` plus Cloudflare Access. Public `cpa.konbakuyomu.us` must not expose `/admin/*`, `/codexcont/*`, `/management.html`, or CPA management APIs.
- SJC is a small-disk host. Deployment must prefer uploading changed files plus single-service rebuild/restart; do not use Docker prune or broad filesystem cleanup as part of dashboard rollout.

### 4. Validation & Error Matrix
- Invalid `limit` query on `/admin/requests` or `/admin/logs` -> fall back to safe defaults.
- Request summary retention exceeds configured cap -> discard oldest non-active summaries first.
- Active request summary is returned -> internal monotonic timer fields must be stripped before JSON/SSE output.
- `GET /admin/logs/stream?once=1` -> emits `ready`, recent `request` events, then recent `log` events, then ends.
- Upstream CPA health probe fails -> dashboard reports upstream unhealthy but admin routes still return safely.
- Browser tab is idle/backgrounded and returns later -> dashboard reconnects SSE and reloads snapshots without requiring a full page reload.
- Manual refresh is clicked while a previous fetch is slow -> the latest refresh wins; older fetch results are ignored instead of overwriting the visible table.
- Request row appears as `processing` -> short follow-up reloads update it to a
  terminal state without requiring a full page refresh.
- Public API host exposes any admin path -> deployment validation fails; fix Caddy/admin proxy routing before accepting rollout.
- SJC free space is tight before rebuild -> verify `df -h /` and avoid pulls/prune; if rebuild needs new image layers and space is insufficient, pause rather than cleaning broad data.

### 5. Good/Base/Bad Cases
- Good: A real Codex request appears in `/admin/requests` with `protection=protected_clean` or `auto_continued`, and no raw reasoning content is present.
- Base: An invalid JSON request returns `400` from `/v1/responses` and appears as `protection=failed`, `failure_reason=invalid_json_body`.
- Bad: The dashboard scans log strings like `round_decision` in JavaScript and guesses whether the request was protected. This duplicates backend contract logic and will drift.
- Bad: A production rollout fixes the page but exposes `/admin/requests` on `https://cpa.konbakuyomu.us/`. This leaks operational metadata and violates the public/admin boundary.

### 6. Tests Required
- Unit: request summary projection covers `protected_clean`, `auto_continued`, `risk_uncontinued`, `passthrough`, `failed`, and retention behavior.
- Unit: redaction preserves numeric counters such as `reasoning_tokens` and `total_tokens`, while redacting bearer/API/OAuth/encrypted-content fields.
- Route smoke: `/admin/requests` returns summaries and `/admin/logs/stream?once=1` includes both `event: request` and `event: log`.
- Frontend smoke: desktop and mobile dashboard render without horizontal overflow, and simulated protection states are visibly distinct.
- Frontend smoke: dashboard HTML keeps the manual-refresh reconnect path, foreground-resume handler, and visible refresh/realtime animation hooks.
- Frontend smoke: dashboard HTML keeps processing-request follow-up reloads.
- Production smoke: `cpa-admin.konbakuyomu.us/codexcont/` reaches the dashboard through Cloudflare Access, while public `cpa.konbakuyomu.us/admin/*` and `/codexcont/*` return `404`.

### 7. Wrong vs Correct

#### Wrong
```text
raw log event -> frontend string matching -> protection label
```

This spreads the event contract into JavaScript and makes the beginner-facing status depend on incidental log wording.

#### Correct
```text
/v1/responses lifecycle -> Diagnostics request summary -> /admin/requests + event: request -> dashboard label
```

`Diagnostics` is the single projection owner. The UI renders the explicit `protection` value and keeps raw logs as an advanced troubleshooting view only.

## Scenario: CPA Key Policy, CPAMP, and user usage portal

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_usage_portal/`, CPAMP monitoring
  queries, CPA Key Policy state parsing, per-key quota display, user usage
  events, or production routes for `cpa-usage.konbakuyomu.us`.
- This is a cross-layer contract: Key Policy state authenticates a raw
  `cpa_...` key, CPA records the plugin principal into usage events, CPAMP
  hashes that principal, the portal filters CPAMP analytics, and the frontend
  renders only safe per-user summaries.

### 2. Signatures
- Portal routes:
  - `GET /healthz`
  - `POST /api/session`
  - `DELETE /api/session`
  - `GET /api/me`
  - `GET /api/usage?range=5h|24h|7d|month`
  - `GET /api/events?range=5h|24h|7d|month&limit=N&before=...`
  - `GET /api/events/stream`
  - `GET /admin/`
  - `GET /admin/api/keys`
  - `PUT /admin/api/keys/limits`
  - `PUT /admin/api/keys/{id}/limits`
  - `POST /admin/api/keys/{id}/reset`
  - `GET /admin/api/events?key_id=all|...&range=5h|24h|7d|month`
- Production user route: `https://cpa-usage.konbakuyomu.us/`
- Production local quota admin route:
  `https://cpa-admin.konbakuyomu.us/usage-admin/`
- Production admin route for CPAMP: `https://cpa-admin.konbakuyomu.us/`
- Key Policy state path on SJC:
  `/opt/codex-stacks/cpa/plugin-state/cpa-key-policy-state.json`
- Containers that read Key Policy state must use a mounted in-container path
  such as `/data/plugin-state/cpa-key-policy-state.json`; host paths are not
  valid from inside CodexCont or the usage portal unless explicitly mounted.

### 3. Contracts
- CPA stays on the official image. Do not fork CPA to implement per-key usage
  views.
- CPAMP stays on the official `seakee/cpa-manager-plus:latest` image. Do not
  patch CPAMP source for user self-service behavior; add sidecars or routes
  around it instead.
- CPA Key Policy stays as the official release plugin binary mounted into CPA.
  Updating Key Policy should mean replacing the plugin binary and preserving
  `plugin-state`, not editing CPA source.
- Keep CPA, CPAMP, CodexCont, and `cpa-usage-portal` as separate containers /
  stacks on the shared `cpa_net`. Do not bundle them into one image because
  independent updates are part of the maintenance contract.
- The user portal may mutate only its own local SQLite metadata: 5H/month
  limits, reset watermarks, and audit entries. It may read Key Policy state and
  query CPAMP monitoring, but it must not mutate CPA, OAuth accounts, proxy
  routing, CPAMP source events, or Key Policy records.
- Ordinary users should receive Key Policy `cpa_...` keys. CPA native `sk...`
  keys are compatibility/admin escape hatches and should not be treated as
  self-service user credentials.
- There is no automatic one-to-one binding between native CPA `sk...` keys and
  Key Policy `cpa_...` keys. If a future migration needs such a bridge, design
  an explicit mapping layer and prove it cannot bypass Key Policy limits.
- The CPAMP login key and the CPA management key are different secrets.
  `cpa-admin.konbakuyomu.us/management.html` currently points to CPAMP, so it
  requires the CPAMP admin key. CPA-native management calls use the CPA
  management key through the internal admin proxy path.
- Login validation uses the raw user key only once:
  `sha256(trimmed_raw_cpa_key)` must match Key Policy `key_hash`
  (`sha256:<hex>`). The raw key must not be stored, logged, or returned.
- CPAMP filtering for Key Policy keys must use `sha256(Key Policy id)`, not
  `sha256(raw cpa_... key)`. CPA Key Policy authenticates requests with
  `Principal = key.ID`, and CPAMP hashes CPA's usage-record principal.
- Keep raw-key hash and CPAMP usage hash as separate concepts in code and
  tests. Session cookies may contain safe hash identifiers, but every request
  must re-load Key Policy state and validate the raw-key hash still maps to an
  enabled record.
- User APIs must never return raw API keys, full raw-key hashes, full CPAMP
  usage hashes, OAuth tokens, CPA management keys, CPAMP admin keys, cookies,
  Authorization headers, request bodies, response bodies, or encrypted
  reasoning content.
- CPAMP `api_key_stats` must be projected before returning it to the browser.
  Do not pass CPAMP rows through directly because they may include full
  `api_key_hash` values.
- Key Policy model entries may be structured objects under `models[]`, not only
  strings. The portal must parse clean aliases from `alias` / `model` /
  `target_model` fields instead of rendering dicts as strings.
- Per-key prices are owned by Key Policy. The portal must parse
  `input_price_per_million`, `output_price_per_million`, and
  `cache_read_price_per_million` from each model entry, plus legacy
  top-level `model_prices` forms for compatibility.
- A model entry whose input, output, cache-read, and cache-creation prices are
  all zero is treated as unpriced for safety. Missing prices must not silently
  turn into "free" usage unless a future explicit free-model policy is added.
- CPAMP can legitimately return `cost: 0` when its own global price book lacks
  custom Codex aliases. For self-service user accounting, `/api/usage` and
  `/api/events` should overlay costs using the current key's Key Policy price
  book. Do not interpret CPAMP zero cost as "free" when Key Policy prices are
  configured.
- CPAMP Management API is the source of truth for usage token projection. Its
  public `cached_tokens` field is already the compatibility cached-input bucket
  used by the main CPA/CPAMP dashboard:
  `max(max(cached_tokens, cache_tokens) - cache_read_tokens -
  cache_creation_tokens, 0)`. The portal must treat that field as a real
  OpenAI/Codex cache hit even when fine-grained `cache_read_tokens` and
  `cache_creation_tokens` are both zero.
- Keep CPAMP-compatible cached input separate from fine-grained cache
  read/create fields in user-facing details. For OpenAI/Codex, a large
  `cached_tokens` value with `cache_read_tokens = 0` is normal and must not be
  displayed as "no cache read".
- `/api/me` must expose safe daily/weekly USD limits and a safe pricing
  summary. It must also expose local 5H/month USD limits and reset points when
  the portal SQLite has them. The user dashboard must show 5H, daily, weekly,
  and monthly limits directly, not only as a selected-range hint.
- `usage-admin` bulk limit saves must validate every submitted key id and
  numeric 5H/month value before reporting success. The UI should expose one
  global save action for local limits and keep per-key soft reset actions
  separate, because reset changes the local watermark rather than the limit
  configuration.
- `usage-admin` all-key event mode (`key_id=all`) must merge enabled Key Policy
  keys, attach only a safe key summary (`id`, `name`, `preview`, `enabled`),
  sort newest first, and cap the merged result by the requested limit.
- The usage portal's selected time range controls both `/api/usage` aggregates
  and the visible `/api/events` recent-request table. The page must also render
  the active range label, because 24h and 7d can legitimately return identical
  numbers when all retained usage happened in the last day.
- Supported portal ranges are `5h`, `24h`, `7d`, and `month`. `month` is the
  current Asia/Shanghai calendar month. `5h`, `24h`, and `7d` are rolling
  windows.
- A portal soft reset writes a reset watermark and narrows future CPAMP query
  windows to `max(base_window_start, reset_at_ms)`. It must not delete CPAMP
  rows or rewrite Key Policy's own historical usage display.
- All `/admin/*` portal routes require a proxy-injected admin header from
  `cpa-admin.konbakuyomu.us/usage-admin/`. Public `cpa-usage.konbakuyomu.us`
  must not be able to call these routes successfully.
- Public `cpa.konbakuyomu.us` must continue to block management, plugin,
  admin, CodexCont dashboard, CPAMP, and usage-portal internals.
- The usage portal frontend must not rely on an old `/api/events/stream`
  connection after tab idle. Manual refresh and foreground resume must rebuild
  the `EventSource`, reload `/api/me`, `/api/usage`, and `/api/events` with
  no-store/cache-bust semantics, and keep late fetch responses from replacing
  newer data.
- The refresh button and realtime chip must expose visible state changes:
  refresh shows busy/completion animation, while the realtime chip pulses for
  connected, reconnecting, and disconnected states.
- A realtime usage event should update the visible recent-request table
  immediately and then schedule delayed snapshot refreshes, because CPAMP
  aggregate views may update slightly after the event row appears.
- SJC is disk-constrained. Portal rollouts should upload changed files and
  rebuild only `cpa-usage-portal`; do not use Docker prune or broad deletion.

### 4. Validation & Error Matrix
- Unknown raw API key -> `401 invalid_api_key`.
- Native CPA `sk...` key submitted to the user portal -> `401 invalid_api_key`
  unless it has been explicitly migrated into Key Policy; this is expected.
- Disabled Key Policy record -> `403 api_key_disabled` at login or
  `401 key_not_available` for an existing session.
- Missing or invalid session -> `401 not_authenticated`.
- CPA management key submitted to CPAMP UI -> reject as an invalid admin key;
  use the CPAMP admin key for CPAMP.
- CPAMP rows whose `api_key_hash` does not match the current key's CPAMP usage
  hash -> drop them server-side.
- Browser tab is left idle and reopened -> usage portal reconnects SSE and
  reloads own-key usage/events without a full page reload.
- Manual refresh happens during a slow previous refresh -> the latest refresh
  owns the visible state; stale responses are ignored.
- Key Policy prices exist but CPAMP returns zero cost -> user portal shows
  nonzero estimated cost from Key Policy prices and marks the source as
  `key_policy`.
- `GET /api/events?range=24h` -> recent events are fetched from the 24h window;
  omitting `range` keeps the compatibility default.
- Key Policy daily/weekly USD limits exist -> `/api/me` and the dashboard show
  both values safely.
- Portal local 5H/month limits exist -> `/api/me` and the dashboard show both
  values safely.
- `POST /admin/api/keys/{id}/reset` -> updates only portal reset watermarks;
  CPAMP original rows remain visible in CPAMP itself.
- `GET /admin/api/keys` without the proxy-injected admin header -> `404`.
- `GET /api/usage` must not include the full raw-key hash or full policy-id
  hash anywhere in the JSON response.
- DNS for `cpa-usage.konbakuyomu.us` may be absent while the sidecar and Caddy
  route are ready; verify with explicit host resolution before declaring the
  route broken.

### 5. Good/Base/Bad Cases
- Good: A user logs in with a `cpa_...` key; the portal validates
  `sha256(raw key)` against Key Policy state, then queries CPAMP with
  `sha256(key.id)` and shows only that key's events.
- Good: CPA and CPAMP are updated by pulling their official images while the
  custom user portal is rebuilt separately from this repository.
- Base: A Key Policy key has no events yet. The portal still shows safe key
  metadata and empty usage tables.
- Bad: The portal filters CPAMP with `sha256(raw key)` and shows zero events
  even though CPAMP has usage records for the key id.
- Bad: `/api/usage` returns CPAMP `api_key_stats` unchanged and leaks a full
  `api_key_hash` to the browser.
- Bad: Ordinary users log into CPAMP or create native `sk...` keys for
  themselves. That expands the admin trust boundary and bypasses the intended
  Key Policy user model.

### 6. Tests Required
- Unit: raw key hash validates login while `record.cpamp_hash` equals
  `sha256(policy id)` when `id` is present.
- Unit: CPAMP analytics calls use the policy-id hash, not the raw-key hash.
- Unit: usage/event projections reject another key's hash and do not return
  full hash values.
- Unit: structured Key Policy `models[]` entries produce clean model aliases,
  safe daily/weekly limits, and per-model prices.
- Unit: `/api/usage` and `/api/events` recompute nonzero costs from Key Policy
  prices when CPAMP cost fields are zero.
- Unit: portal local SQLite stores 5H/month limits, applies reset watermarks,
  and closes connections cleanly on Windows.
- Unit: `/admin/*` routes reject requests without the proxy-injected admin
  header and expose safe quota projections when the header is present.
- Unit: redaction covers Authorization, cookies, API keys, tokens, management
  keys, and encrypted reasoning fields while preserving numeric token counters.
- Unit: retention deletes old CPAMP `usage_events` in batches and does not run
  `VACUUM`.
- Frontend smoke: user portal HTML keeps the forced stream reconnect,
  foreground-resume handler, and refresh/realtime animation hooks.
- Production smoke: allowed Key Policy model succeeds, disallowed model is
  rejected, CPAMP records real usage, the portal login succeeds, and
  `/api/events` returns only own events.

### 7. Wrong vs Correct

#### Wrong
```text
user -> native sk... key -> user portal / CPAMP admin
```

Native CPA keys are not the quota-managed user identity in this deployment.

#### Correct
```text
admin -> Key Policy creates cpa_... key -> user uses cpa_... for Codex and usage portal
```

The `cpa_...` key is both the request credential and the self-service usage
credential, while CPAMP remains admin-only.

## Scenario: CPA Governor plugin and CodexCont Engine rollout

### 1. Scope / Trigger
- Trigger this spec whenever work touches `cpa_governor_plugin/`, Governor
  plugin deployment, CPA plugin routing, `cpa-usage.konbakuyomu.us` user
  routing, or CodexCont Engine routes.
- This is cross-layer work: CPA dynamic plugin loading, Key Policy state
  import, SQLite usage state, Caddy public/admin routing, and CodexCont Engine
  health all have to agree.
- Governor is owned by this repository. CPA, CPAMP, and CPA Key Policy remain
  official upstream artifacts and must not be forked for this integration.

### 2. Signatures
- CPA plugin artifact:
  `/CLIProxyAPI/plugins/linux/amd64/cpa-governor.so`.
- CPA plugin config:
  `plugins.configs.cpa-governor` with `enabled`, `priority`,
  `exclusive_auth`, `state_db_path`, `key_policy_state_path`,
  `session_secret`, `codexcont_enabled`, `codexcont_route`,
  `codexcont_url`, and `fail_mode`.
- Admin resource:
  `GET /v0/resource/plugins/cpa-governor/admin`.
- User resource:
  `GET /v0/resource/plugins/cpa-governor/user`.
- Admin proxy convenience routes:
  `https://cpa-admin.konbakuyomu.us/governor/` and
  `https://cpa-admin.konbakuyomu.us/governor-user/`.
- User portal route:
  `https://cpa-usage.konbakuyomu.us/`.
- CodexCont Engine:
  `GET /engine/healthz` and `POST /engine/v1/responses/analyze`.

### 3. Contracts
- Deploy Governor first in passive mode unless a test-key executor cutover has
  already passed:
  `codexcont_enabled: true`, `codexcont_route: false`,
  `exclusive_auth: false`.
- In passive mode, Governor may provide admin/user UI, key visibility, usage
  storage, CodexCont health, and safe request projections, but it must not be
  described as the exclusive quota enforcer or the executor-level 516 owner.
- Public `cpa.konbakuyomu.us` must block `/v0/resource/plugins/*`,
  `/v0/management*`, `/admin*`, `/codexcont*`, `/governor*`, and related
  management paths.
- `cpa-usage.konbakuyomu.us` may expose only the Governor user page and
  `.../user/api/*`; it must return 404 for Governor admin resources and other
  management paths.
- CPA plugin `ResourceRoute` dispatch is GET-only in the current CPA host.
  User self-service APIs under `/v0/resource/plugins/cpa-governor/user/api/*`
  must therefore use GET requests. Session creation passes the raw user key in
  `X-CPA-Governor-Key` (or `X-CPA-User-Key`) so it does not collide with
  CPAMP/management `Authorization` headers on embedded plugin pages. A direct
  route may still accept `Authorization: Bearer <cpa_...>` as a fallback. Do
  not put the key in the URL, and do not implement user-resource mutations as
  POST unless they move behind a management route or another authenticated
  proxy surface.
- User resource responses and plugin HTML must send `Cache-Control: no-store`.
  CPAMP can keep a tab alive across plugin upgrades, so stale HTML/JS must not
  be cached by the browser or an intermediate admin proxy.
- `cpa-admin.konbakuyomu.us/governor/` is protected by Cloudflare Access and
  may route through the local admin proxy to CPA's plugin resource endpoint.
- Do not put CPA management keys, API keys, OAuth tokens, cookies, or
  encrypted reasoning into Caddy rewrites, browser URLs, Trellis docs, or git.
- The Governor user portal must explain key identity clearly: Key Policy
  `cpa_...` full keys are accepted, native CPA `sk...` keys and shortened
  previews are rejected with human-readable messages.
- When updating the plugin binary, record the SHA256 and verify CPA logs show
  the plugin loaded and registered from the platform directory.
- Dense admin/user tables on mobile must keep a stable minimum table width
  inside an overflowed panel. Do not let tables shrink until short fields turn
  vertical.
- On SJC, rebuild/restart only the necessary self-owned service or plugin.
  Do not use Docker prune and do not pull official images as a side effect of
  a Governor-only rollout.

### 4. Validation & Error Matrix
- CPA plugin file missing or wrong architecture -> CPA logs do not show
  `plugin loaded plugin_id=cpa-governor`; deployment is not accepted.
- Governor admin API returns no Key Policy keys -> verify
  `key_policy_state_path` and plugin-state mount before accepting the UI.
- `codexcont_route=false` -> production `/v1/responses` must still pass
  through the existing working route and return a real successful response.
- Public `cpa.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin` returns
  200 -> rollback Caddy public block before accepting the rollout.
- Public `cpa-usage.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin`
  returns 200 -> rollback user-host route before accepting the rollout.
- User API without session -> `401`; invalid CPA user key -> `401
  invalid_api_key`.
- User session with a native `sk...` key -> `401
  native_cpa_key_not_supported` and a message telling the user to use the full
  Key Policy `cpa_...` key.
- User session with a shortened `cpa_...` preview -> `401
  key_preview_not_usable` and a message telling the user to use the full key
  shown at create/rotation time.
- User session with a full but rotated/stale `cpa_...` key -> `401
  invalid_api_key`; validate by hashing the pasted key and comparing it with
  the current Key Policy state before blaming Governor sync.
- Embedded CPAMP plugin page sends a CPAMP management bearer token in
  `Authorization` plus user key in `X-CPA-Governor-Key` -> Governor must use
  the dedicated user-key header and return `200` for a valid current key.
- `POST` to a user resource API -> CPA returns `404` before the plugin; the
  browser UI must call these resource APIs with GET.
- Mobile Playwright snapshot shows table columns narrower than practical text
  width or vertical labels -> add panel overflow/min-width and revalidate.

### 5. Good/Base/Bad Cases
- Good: Governor is loaded by CPA, admin page shows 3 Key Policy keys, user
  page opens on `cpa-usage`, CodexCont health is green, public API admin paths
  return 404, and real `/v1/responses` still succeeds.
- Base: Governor user page opens but no user is logged in. `/user/api/me`
  returns `401 not_authenticated`, and the page waits for a raw `cpa_...` key.
- Base: A Key Policy key is rotated. The old full key is unrecoverable and
  should fail login; only the newly generated full key shown in the rotation
  dialog can match the current `key_hash`.
- Bad: The user portal uses `Authorization` as its only login transport inside
  CPAMP. The admin shell may already use that header, causing valid user keys
  to be interpreted as invalid.
- Bad: Caddy sends public `/v1/responses` to Governor before the executor-level
  continuation path is validated. This can bypass the known-good CodexCont
  fold path.
- Bad: A deployment runs `docker compose up` against the CPA stack with
  `pull_policy: always` during a plugin-only change on the small SJC disk.
  This may pull new layers and introduce unrelated official-image drift.

### 6. Tests Required
- Go unit: key hashing, Key Policy import, quota windows, pricing, redaction,
  store usage events, admin/user handler responses.
- Go unit: user login must prefer `X-CPA-Governor-Key` over `Authorization`,
  read headers case-insensitively, and mark JSON/HTML responses `no-store`.
- Python unit: CodexCont Engine summary projection and route smoke.
- Build verification: linux/amd64 `.so` SHA256 recorded and `file` reports an
  ELF x86-64 shared object compatible with the Debian/glibc CPA image.
- Server smoke: `/healthz`, authenticated `/v1/models`, authenticated
  `/v1/responses`, Governor admin/user pages, user-host 401/404 boundaries,
  public API 404 boundaries, disk free space.
- Playwright: desktop and 390px mobile snapshots for Governor admin and user
  pages; dense tables must be horizontally scrollable instead of vertically
  compressed.

### 7. Wrong vs Correct

#### Wrong
```text
plugin UI added -> route every public path to /v0/resource/plugins/*
```

This exposes internal management resources and bypasses the public/admin host
boundary.

#### Correct
```text
cpa-admin host -> Governor admin resource
cpa-usage host -> Governor user resource only
cpa API host -> official API plus explicit admin/plugin blocks
```

Each hostname exposes only the surface that matches its trust boundary.
