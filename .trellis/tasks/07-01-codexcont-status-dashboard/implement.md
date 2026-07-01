# Implementation Plan

## 1. Task And Spec Prep

- Confirm clean Git state and current Trellis task.
- Read applicable Trellis specs before code edits.
- Keep sensitive deployment values out of Trellis artifacts.

## 2. Backend Diagnostics

- Add a diagnostics module with a bounded in-memory ring buffer, metrics snapshot, subscriber queues, and redaction helpers.
- Add admin routes to the Starlette app.
- Add lightweight instrumentation in `handle_responses` and `fold_stream` for lifecycle and continuation events.
- Add upstream health probe from `/admin/status` using the configured CPA upstream base.

## 3. Frontend

- Add static dashboard HTML/CSS/JS served by CodexCont.
- Use EventSource for `/admin/logs/stream` and fetch `/admin/status` periodically.
- Include filters, pause/autoscroll controls, local clear, connection state, and responsive layout.

## 4. Local Validation

- Add/extend tests for diagnostics ring buffer, redaction, SSE stream, admin routes, and current middleware behavior.
- Run the existing test suite.
- Use Playwright or a local browser smoke to capture desktop and mobile dashboard states.

## 5. Server Rollout

- Back up current `/opt/codex-stacks/codexcont` and `/opt/codex-stacks/cpa-admin-tunnel` config files to a root-only timestamped path.
- Rebuild/restart only the CodexCont stack; do not prune Docker.
- Update `cpa-admin-proxy` so `/codexcont/` proxies to `codexcont:8787/admin/`.
- Verify public API host still blocks admin paths.
- Verify `cpa-admin.konbakuyomu.us/codexcont/` loads through Cloudflare Access.

## 6. Acceptance Evidence

- Record local test results, server route checks, and dashboard live-log proof in `implement.md`.
- Specifically record whether this active Codex conversation or another real `/v1/responses` request appears in live logs.
- Commit code and Trellis artifacts with narrow staging.

## Rollback

- Restore backed-up CodexCont stack files and restart `codexcont`.
- Restore backed-up `cpa-admin-proxy` config if `/codexcont/` routing breaks CPA management access.
- Caddy public API routing should not need rollback if it remains unchanged.

## Execution Evidence

### Local Implementation

- Added `middleware.diagnostics` as the single in-memory owner for metrics, ring buffer events, subscribers, and redaction.
- Added read-only admin routes and static dashboard under `/admin/`.
- Instrumented `/v1/responses` request start, passthrough/fold start, round decision, continuation open, finish, and failure events.
- Added `[admin].max_log_events = 800` default config.
- Updated README and README_zh with dashboard usage and project layout.

### Local Validation

- `.venv\Scripts\python.exe -m py_compile middleware\app.py middleware\admin.py middleware\diagnostics.py middleware\proxy.py middleware\config.py tests\test_middleware.py`: passed.
- `.venv\Scripts\python.exe tests\test_middleware.py`: `123/123 checks passed`.
- `git diff --check`: passed.
- Playwright via Node REPL + system Edge validated `http://127.0.0.1:8797/admin/`:
  - desktop 1440px: title `CodexCont Dashboard`, SSE `Live`, status cards `3`, metric cards `4`, horizontal overflow `false`.
  - mobile 390px: horizontal overflow `false`, log table rendered, local invalid `/v1/responses` event appeared in the log table.

### Server Rollout

- Backup path: `/root/codexcont-dashboard-backups/20260701T080223Z`.
- Disk before rollout: `/dev/sda1` around `8.7G used / 832-833M available / 92%`.
- Uploaded updated CodexCont middleware files to `/opt/codex-stacks/codexcont/app/middleware/`.
- Added production `[admin] max_log_events = 800` to `/opt/codex-stacks/codexcont/config.toml`.
- Updated `/opt/codex-stacks/cpa-admin-tunnel/Caddyfile` so:
  - `/codexcont/` routes to `codexcont:8787/admin/`.
  - all other paths continue to `cpa:8317`.
- `docker exec cpa-admin-proxy caddy validate --config /etc/caddy/Caddyfile`: valid.
- Rebuilt/restarted only `codexcont`; restarted only `cpa-admin-proxy`.
- No Docker prune or bulk filesystem deletion was used.

### Server Validation

- Local admin proxy:
  - `http://127.0.0.1:8327/codexcont/`: HTTP `200`.
  - `http://127.0.0.1:8327/codexcont/status`: HTTP `200`; CPA upstream health `200`, `log_retention=800`.
  - `http://127.0.0.1:8327/management.html`: HTTP `200`, preserving CPA management access.
- Cloudflare Access:
  - `https://cpa-admin.konbakuyomu.us/codexcont/`: HTTP `302` to Cloudflare Access login when unauthenticated.
  - `https://cpa-admin.konbakuyomu.us/management.html`: HTTP `302` to Cloudflare Access login when unauthenticated.
- Public API domain:
  - `https://cpa.konbakuyomu.us/healthz`: HTTP `200`.
  - `https://cpa.konbakuyomu.us/admin/`: HTTP `404`.
  - `https://cpa.konbakuyomu.us/codexcont/`: HTTP `404`.
  - `https://cpa.konbakuyomu.us/management.html`: HTTP `404`.
- Live-log proof:
  - Current real Codex conversation traffic appeared as `fold_start`, `round_decision`, and `request_finished` for `model=gpt-5.5`.
  - After the redaction fix, `round_decision` preserved numeric `reasoning_tokens` such as `281`.
  - A controlled public invalid JSON request to `/v1/responses` returned HTTP `400` and appeared as `request_failed reason=invalid_json_body`.
