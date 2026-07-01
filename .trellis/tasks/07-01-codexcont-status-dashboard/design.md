# Design

## Architecture

Dashboard functionality lives inside the existing CodexCont Starlette app. CPA remains an official upstream service and is not forked or extended for this first version.

Production data path remains unchanged:

`Codex -> cpa.konbakuyomu.us/v1/responses -> Caddy -> CodexCont -> CPA -> OpenAI`

Admin view path:

`Browser -> Cloudflare Access -> cpa-admin.konbakuyomu.us/codexcont/ -> cpa-admin-proxy -> codexcont:8787/admin/`

## Backend Contracts

- `GET /admin/healthz` returns `{ "ok": true }` plus process uptime.
- `GET /admin/status` returns process metrics, recent counters, upstream health, and a safe config summary.
- `GET /admin/logs?limit=N` returns the newest redacted in-memory log events.
- `GET /admin/logs/stream` uses `text/event-stream`; new events are emitted as JSON `data:` frames.
- `GET /admin/` serves the static dashboard. Static assets can be embedded or served under `/admin/static/...`; no Node build is required.

## Metrics And Logs

- Add a small diagnostics module responsible for:
  - monotonic service start time and uptime
  - bounded ring buffer
  - subscriber queues for SSE
  - request counters and active request tracking
  - continuation/truncation/failure counters
  - log redaction for sensitive-looking strings and headers
- Instrument these points:
  - request accepted / passthrough / fold start
  - round decision with reasoning token count and continuation decision
  - continuation opened
  - request finished cleanly or failed
  - upstream health probe result
- Use generated request IDs for dashboard correlation only; do not expose upstream tokens or reasoning payloads.

## Frontend

- Single static operational dashboard, not a landing page.
- Compact top band: CodexCont health, CPA health, active requests, SSE connection status.
- Metrics grid: total requests, continuations, truncation hits, failures, last continuation.
- Live log table: timestamp, level, event, request id, model, round, reason, message.
- Controls: level/event filter, pause, autoscroll, clear local view, reconnect state.
- Styling is plain CSS with restrained colors, max 8px card radius, stable dimensions, responsive grid, and no decorative gradient/orb background.

## Deployment Contract

- Public API host `cpa.konbakuyomu.us` must not expose `/admin` or `/codexcont`.
- Existing `cpa-admin-proxy` should route `/codexcont/` to `codexcont:8787/admin/` with path stripping; all other paths continue to CPA management.
- Cloudflare Access remains the outer auth layer. CodexCont admin routes do not implement their own login for v1.
- Server rollout must back up current CodexCont stack and `cpa-admin-proxy` config before changes.
