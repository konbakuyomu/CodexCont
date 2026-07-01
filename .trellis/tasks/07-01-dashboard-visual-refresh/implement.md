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

## Follow-up: Refresh Resiliency And Live-State Feedback

Problem reported on 2026-07-01:

- After leaving `cpa-usage.konbakuyomu.us` or the CodexCont dashboard idle and
  returning later, clicking the toolbar refresh button sometimes did not show
  the newest requests. A full browser reload did recover the page.
- The refresh action had weak/no visible progress feedback.
- The realtime connection chip looked static, so it was hard to tell whether
  the page was alive, reconnecting, or stale.

Root cause:

- The pages treated an existing `EventSource` object as usable browser state
  even after a long idle/background period. Browser tab throttling, network
  suspension, or Cloudflare/proxy idle behavior can leave the frontend with a
  stale object or a slow in-flight fetch.
- Manual refresh reloaded some JSON snapshots, but did not force a fresh SSE
  connection. It also did not guard against a late older fetch overwriting a
  newer refresh result.

Implemented fix:

- `cpa_usage_portal/static/dashboard.html`:
  - GET API calls now use `cache: "no-store"` plus a cache-busting query value.
  - Dashboard refreshes use a sequence counter, so late stale responses cannot
    overwrite newer data.
  - Manual refresh forces a fresh `/api/events/stream` `EventSource`.
  - `visibilitychange`, `pageshow`, and stale `focus` revive the dashboard by
    reconnecting SSE and reloading current snapshots.
  - The refresh button shows a spinner/busy label and completion pulse.
  - The realtime chip pulses in connected, reconnecting, and disconnected
    states.
- `middleware/dashboard.html`:
  - `status`, `requests`, and `logs` snapshot fetches use no-store/cache-bust
    and independent sequence guards.
  - Manual refresh reloads all snapshots and force-reconnects `logs/stream`.
  - Foreground resume handlers rebuild SSE and reload the dashboard.
  - Refresh and realtime state animations match the usage portal.
- Regression smoke checks were added to both dashboard route tests to ensure
  forced stream reconnect, foreground resume, and animation hooks stay present.

Validation:

- `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` -> 40/40 checks
  passed.
- `.venv\Scripts\python.exe tests\test_middleware.py` -> 146/146 checks
  passed.
- `.venv\Scripts\python.exe -m compileall cpa_usage_portal run_usage_portal.py middleware run.py`
  -> passed.
- Node parsed the inline scripts from both HTML files successfully:
  `cpa_usage_portal/static/dashboard.html: js parse ok` and
  `middleware/dashboard.html: js parse ok`.

Server rollout on SJC:

- Preflight after the previous dashboard refresh rollout: `/` was 9.6G total,
  8.8G used, about 749M available, 93% used.
- Safe Key Policy projection confirmed real prices are stored under
  `models[]` entries. `QQ专用` has 5 priced models plus daily/weekly limits
  `100 / 500`; `kuma专用` has 5 priced models but no daily/weekly limits.
- Root-only backup path:
  `/root/codex-backups/dashboard-pricing-refresh-20260701-225349/`.
  Backed up changed server files before upload; `pricing.py` was new, so no old
  file existed to back up.
- Uploaded only changed custom files:
  `cpa_usage_portal/app.py`, `cpa_usage_portal/cpamp.py`,
  `cpa_usage_portal/key_policy.py`, `cpa_usage_portal/pricing.py`,
  `cpa_usage_portal/static/dashboard.html`, and `middleware/dashboard.html`.
- Rebuilt/restarted only `cpa-usage-portal` and `codexcont` for the main
  rollout. A final one-line safety patch to all-zero price handling restarted
  only `cpa-usage-portal`.
- `cpa`, `cpamp`, and `caddy-edge` kept their prior uptime; CPA, CPAMP, and
  CPA Key Policy official artifacts were not modified.

Server validation:

- `cpa-usage-portal` health inside the container returned
  `{"ok":true,"key_policy_state":true,"cpamp":true}`.
- The public usage page returned `200` and contains `CPA 用量自助页`,
  `用量限额`, and `scheduleFollowUpRefreshes`.
- A temporary server-side session for `QQ专用` showed `priced_model_count=5`,
  limits `daily_usd=100.0`, `weekly_usd=500.0`, and nonzero
  `summary.total_cost=0.907675` with `cost_source=key_policy`.
- A temporary server-side session for `kuma专用` showed `priced_model_count=5`,
  null daily/weekly limits, and nonzero Key Policy-derived usage cost. This
  confirms its prior missing limits were configuration state, not a page bug.
- CodexCont internal admin page includes `scheduleRequestFollowUp`, and public
  `https://cpa.konbakuyomu.us/codexcont/` remained `404`.
- Final disk state remained tight but stable: 9.6G total, 8.8G used, about
  745M available, 93% used. No Docker prune or broad deletion was used.

## Follow-up: Key Policy Pricing, Limits, And Faster Status Convergence

Problem reported on 2026-07-01:

- The Key Policy admin page had per-model prices configured for user keys, but
  the self-service usage portal still showed `$0.0000`.
- The self-service page did not make a user's daily and weekly USD limits
  visible enough.
- Both custom dashboards could still feel stale: a request row could remain
  `processing` or the realtime state could lag until a full browser refresh.

Root cause:

- The real Key Policy state stores model aliases and prices as structured
  entries under `models[]`, with fields such as
  `input_price_per_million`, `output_price_per_million`, and
  `cache_read_price_per_million`. The portal only understood top-level
  `model_prices`, so it neither displayed clean model names nor saw the
  configured prices.
- CPAMP's own model price book did not have these custom Codex aliases, so
  CPAMP analytics legitimately returned zero cost. The user portal needed to
  overlay per-key prices from Key Policy instead of trusting CPAMP cost fields.
- Realtime events are not enough as durable UI state after tab idle or while a
  request is still in progress; the pages needed more follow-up snapshot pulls.

Implemented fix:

- `cpa_usage_portal/key_policy.py`:
  - parses structured `models[]` entries into clean aliases;
  - parses per-model Key Policy prices from the real field names;
  - returns safe pricing metadata and daily/weekly limits in `/api/me`.
- `cpa_usage_portal/pricing.py`:
  - computes local per-key costs from Key Policy prices;
  - separates input, output, cached input, cache read, and cache creation token
    buckets;
  - mirrors CPAMP's `priority` / `fast` service-tier multiplier for Codex
    model families.
- `cpa_usage_portal/app.py`:
  - requests CPAMP `model_stats` for `/api/usage`;
  - overlays `summary.total_cost`, `model_share[].cost`, and event `cost`
    with Key Policy pricing;
  - marks computed rows with `cost_source: key_policy`.
- `cpa_usage_portal/static/dashboard.html`:
  - shows daily and weekly USD limits directly in the metric row;
  - shows pricing source / missing-price hints in the cost card;
  - optimistically merges realtime event rows, then schedules delayed snapshot
    refreshes so aggregate data can catch up.
- `middleware/dashboard.html`:
  - refreshes status counters after request SSE updates;
  - follows `processing` request rows with short delayed `/admin/requests`
    reloads;
  - reduces regular request snapshot polling from 15 seconds to 5 seconds.

Validation:

- `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` -> 48/48 checks
  passed.
- `.venv\Scripts\python.exe tests\test_middleware.py` -> 147/147 checks
  passed.
- `.venv\Scripts\python.exe -m compileall cpa_usage_portal run_usage_portal.py middleware run.py`
  -> passed.
- Node parsed the inline scripts from both HTML files successfully:
  `cpa_usage_portal/static/dashboard.html: js parse ok` and
  `middleware/dashboard.html: js parse ok`.
