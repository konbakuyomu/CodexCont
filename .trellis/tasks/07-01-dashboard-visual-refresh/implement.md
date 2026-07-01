# Dashboard Visual Refresh Implementation Plan

## Phase 1: Planning And Specs

1. Record the approved product plan in `prd.md`, `design.md`, and this file.
2. Read backend specs and shared thinking guides before code edits.
3. Start the Trellis task.

## Phase 2: Local Implementation

1. Update `cpa_usage_portal.redaction`:
   - add bounded `failure_brief`;
   - keep `failure` redacted and bounded;
   - strip noisy response-header blobs from table-facing output.
2. Rebuild `cpa_usage_portal/static/dashboard.html`:
   - CPAMP-like dark top toolbar and metric cards;
   - compact model table and recent request table;
   - expandable event detail rows;
   - no long summary column.
3. Rebuild `middleware/dashboard.html`:
   - same dark visual language;
   - recent request protection table remains first-class;
   - advanced logs stay collapsible and lower priority.
4. Update tests for failure summary projection and existing admin smoke.

## Phase 3: Local Validation

1. Run `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py`.
2. Run `.venv\Scripts\python.exe tests\test_middleware.py`.
3. Run `.venv\Scripts\python.exe -m compileall cpa_usage_portal run_usage_portal.py middleware run.py`.
4. Use Playwright screenshots for desktop and 390px mobile when a local server
   can be started without disturbing production.

## Phase 4: Server Rollout

1. Confirm SJC disk and running containers.
2. Create root-only backups of `/opt/codex-stacks/cpa-usage-portal` and
   `/opt/codex-stacks/codexcont`.
3. Upload changed custom files only.
4. Rebuild/restart only `cpa-usage-portal` and `codexcont`.
5. Verify:
   - `https://cpa-usage.konbakuyomu.us/` loads;
   - `https://cpa-admin.konbakuyomu.us/codexcont/` loads;
   - recent real requests still appear;
   - public `https://cpa.konbakuyomu.us` returns `404` for admin/dashboard
     paths.

## Phase 5: Closeout

1. Record screenshots, server evidence, and any known follow-ups.
2. Commit implementation and Trellis task artifacts.

## Implementation Evidence

Local changes:

- `cpa_usage_portal.redaction.safe_event` now adds bounded `failure_brief`,
  caps `failure` at 600 characters, and strips noisy response-header blobs from
  success events.
- `cpa_usage_portal/static/dashboard.html` was rebuilt as a dark, no-sidebar
  operations page. The main request table now shows time, status, model,
  latency, tokens, reasoning, cost, and an expand action only.
- `middleware/dashboard.html` was rebuilt with the same visual language.
  Recent request protection remains the first-screen focus and advanced logs
  are collapsed below the request table.
- CPA, CPAMP, and CPA Key Policy official source/artifacts were not modified.

Local validation on 2026-07-01:

- `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` -> 36/36 checks
  passed.
- `.venv\Scripts\python.exe tests\test_middleware.py` -> 143/143 checks
  passed.
- `.venv\Scripts\python.exe -m compileall cpa_usage_portal run_usage_portal.py middleware run.py`
  -> passed.
- Playwright screenshot review covered desktop and 390px mobile mock data for
  both pages. Screenshots are in `artifacts/usage-desktop.png`,
  `artifacts/usage-mobile.png`, `artifacts/codexcont-desktop.png`, and
  `artifacts/codexcont-mobile.png`. The screenshots show no short-field
  vertical compression or incoherent overlap; mobile tables use horizontal
  scroll inside the table area instead of compressing columns.

Server rollout on SJC:

- Preflight: `/` was 9.6G total, 8.8G used, 759M available, 93% used.
- Root-only backup path:
  `/root/codex-backups/dashboard-visual-refresh-20260701-212946/`.
  Backup tarballs were created with mode `600`.
- Uploaded only changed custom files:
  `cpa_usage_portal/redaction.py`,
  `cpa_usage_portal/static/dashboard.html`, and
  `middleware/dashboard.html`.
- Rebuilt/restarted only `cpa-usage-portal` and `codexcont`.
  `cpa`, `cpamp`, `caddy-edge`, `cpa-admin-proxy`, and
  `cpa-admin-tunnel` kept their prior uptime.

Server validation:

- `cpa-usage-portal` health via internal route returned
  `{"ok":true,"key_policy_state":true,"cpamp":true}`.
- CodexCont `/admin/healthz` via internal route returned `200`.
- Admin proxy internal route `http://127.0.0.1:8327/codexcont/` returned
  `200`.
- Admin proxy SSE route `/codexcont/logs/stream?once=1` returned `200` with
  `ready` and `request` events.
- Public `https://cpa-usage.konbakuyomu.us/` returned `200` and the new page
  contains `CPA 用量自助页`, `最近请求`, and `failure_brief`.
- Public `https://cpa-admin.konbakuyomu.us/codexcont/` returned a Cloudflare
  Access login redirect, preserving the admin boundary.
- Public `https://cpa.konbakuyomu.us/admin/`,
  `https://cpa.konbakuyomu.us/codexcont/`, and
  `https://cpa.konbakuyomu.us/admin/requests` returned `404`.
- CodexCont request summaries returned real recent traffic including
  `protected_clean` and `auto_continued` entries, proving the dashboard data
  path still reflects live Codex traffic.
- Post-rollout disk remained tight but stable: 9.6G total, 8.8G used, 759M
  available, 93% used. No Docker prune or broad filesystem cleanup was used.

Known follow-up:

- The mobile screenshots intentionally show horizontally scrollable tables.
  This is preferred over compressing short columns into vertical text.
