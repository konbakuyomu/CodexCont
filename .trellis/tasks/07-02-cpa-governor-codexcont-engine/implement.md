# CPA Governor plugin and CodexCont engine implementation plan

## Steps

1. Inspect current Python middleware/usage portal and CPA plugin examples for reusable logic.
2. Add CodexCont engine API routes and safe summary projection tests.
3. Add a `cpa_governor_plugin` Go module with core packages for hashing, quotas, pricing, storage, safe projection, admin/user JSON handlers, and plugin registration skeleton.
4. Implement Governor UI resources as static HTML served by the plugin.
5. Add local tests for Python engine and Go core logic.
6. Build or document the Linux plugin artifact path; prefer server-side build if local cross-build is unavailable.
7. Prepare deployment files/scripts that do not modify official CPA/CPAMP/Key Policy images.
8. Deploy cautiously on SJC: backup, check disk, upload changed files/artifacts, restart only CodexCont/CPA as needed.
9. Validate public/admin/user routes, CodexCont engine health, key visibility, usage projection, and path blocking.
10. Record results and update README/specs with the new architecture and residual risks.

## Validation commands

- `python -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`
- `python tests/test_middleware.py`
- `python tests/test_cpa_usage_portal.py`
- `go test ./...` inside `cpa_governor_plugin/go`
- Playwright screenshots for admin/user pages if a local server or deployed route is available.
- Server: `df -h /`, `docker ps`, `curl` health checks, authenticated test request, and public path blocking checks.

## Risk controls

- Do not use Docker prune or recursive deletes.
- Do not print or commit API keys/OAuth tokens/management keys.
- Keep old production path until Governor protected executor path is verified.
- Any server-side source/build artifacts must be small and backed up first.
- If plugin host callback constraints block full request execution, ship passive/admin/user/engine improvements and document the remaining cutover gate.

## Execution record

- Local tests passed on 2026-07-02:
  - `go test ./...` in `cpa_governor_plugin/go`
  - `.venv\Scripts\python.exe -m compileall middleware cpa_usage_portal run.py run_usage_portal.py`
  - `.venv\Scripts\python.exe tests\test_middleware.py` (`163/163`)
  - `.venv\Scripts\python.exe tests\test_cpa_usage_portal.py` (`84/84`)
- Linux plugin artifact:
  - Built from WSL with a temporary Go 1.22.6 linux/amd64 toolchain.
  - Local/remote SHA256: `7f4e31cab8c214f8985c7a6a7fabbd90a559bd3b206e65bcac28f21a9a8a75d3`.
  - Verified as ELF 64-bit x86-64 shared object.
- SJC deployment:
  - Backed up sensitive/runtime files to `/root/cpa-governor-backup-20260702-055926`.
  - Additional Caddy public-edge backup before switching the user portal route: `/root/caddy-Caddyfile-before-governor-user-20260702-061604`.
  - Uploaded CodexCont `middleware/app.py`, `middleware/engine.py`, and Governor plugin `.so`.
  - Added `plugins.configs.cpa-governor` in CPA config with `exclusive_auth: false` and `codexcont_route: false`.
  - Added admin proxy routes:
    - `https://cpa-admin.konbakuyomu.us/governor/`
    - `https://cpa-admin.konbakuyomu.us/governor-user/`
  - Switched `https://cpa-usage.konbakuyomu.us/` from the old Python usage portal to Governor's user page, exposing only the user resource/API surface.
  - Rebuilt only `codexcont`; restarted `cpa`, `cpa-admin-proxy`; reloaded `caddy-edge`. No Docker prune and no broad deletion.
- Production validation:
  - `https://cpa.konbakuyomu.us/healthz` returned `200`.
  - Authenticated `https://cpa.konbakuyomu.us/v1/models` returned `200`.
  - Authenticated real `https://cpa.konbakuyomu.us/v1/responses` returned `200`, `status=completed`, `model=gpt-5.5`.
  - CPA logs showed Governor loaded and registered from `/CLIProxyAPI/plugins/linux/amd64/cpa-governor.so`.
  - Governor admin API via `127.0.0.1:8327/governor/api/keys` returned `ok=true`, `keys=3`, CodexCont `health_ok=true`, `route=false`.
  - Public `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-governor/admin`, `/admin/requests`, `/codexcont/`, `/governor/` returned `404`.
  - Public `https://cpa-usage.konbakuyomu.us/` returned the Governor user page; `/admin/`, `/usage-admin/`, and `/v0/resource/plugins/cpa-governor/admin` returned `404`.
  - User API without session returned `401`; invalid key login returned `401 {"error":"invalid_api_key","ok":false}`.
  - Final root disk state remained tight but usable: about `638M` free on `/`.
- Playwright validation:
  - Access-protected admin route redirected to Cloudflare Access as expected in an unauthenticated browser.
  - Local SSH tunnel to admin proxy showed Governor admin page, Key management, request details, and CodexCont tabs render.
  - Mobile 390px viewport initially exposed compressed table columns; fixed by giving tables a mobile minimum width inside an overflowed panel.
  - Re-validated mobile: Key table no longer collapses into vertical text; user page opens on `https://cpa-usage.konbakuyomu.us/`.

## Residual risk / next cutover gate

- Production remains in safe passive mode:
  - `codexcont_enabled: true`
  - `codexcont_route: false`
  - `exclusive_auth: false`
- Real `/v1/responses` traffic still uses the already-proven Caddy front split:
  `cpa.konbakuyomu.us/v1/responses -> codexcont:8787 -> cpa:8317`.
- Governor is now the unified UI/observability surface, but it is not yet the exclusive quota enforcer or the executor-level 516 continuation owner. Full cutover requires CPA host-model callback continuation validation with a test key before changing `codexcont_route` or removing the Caddy front split.
