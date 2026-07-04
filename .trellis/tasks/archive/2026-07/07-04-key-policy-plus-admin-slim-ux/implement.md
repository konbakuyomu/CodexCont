# CPA Key Policy+ Admin Slim UX Implementation Plan

## Steps

1. Start the Trellis task after artifacts are written.
2. Update `admin.html` CSS:
   - replace metric cards with compact strip styles;
   - simplify table widths and remove quota mini-card dependence;
   - add detail summary, quota grid, model summary, and button feedback styles.
3. Update `admin.html` JS rendering:
   - add helpers for enabled/quota/RPM/model summaries and missing prices;
   - simplify `renderKeyList()`;
   - simplify `renderDetail()` and quota layout;
   - add reusable async button feedback for save, refresh, model reload, and
     modal actions.
4. Update Go HTML tests to assert the slim UX contract and removed noisy
   markers.
5. Run local tests and diff check.
6. Build Linux amd64 plugin, record SHA, deploy to SJC, restart only CPA, and
   run smoke checks.
7. Archive task and record journal after successful validation.

## Validation Commands

- `go test ./... -count=1 -timeout=120s` in `cpa_key_policy_plus_plugin/go`
- `git diff --check`
- Linux plugin build:
  `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared`

## Rollback

- Restore the previous remote `cpa-key-policy-plus.so` backup and restart CPA.
- No database migration is involved.

## Deployment Evidence

- Work commit: `05be2af fix(cpa): simplify key policy admin UX`.
- Linux amd64 plugin SHA256:
  `f2a3f36c8314ceecee7177c49845dd9830d61e942f85b186a4e83c4aaf1b0303`.
- Remote backup:
  `/opt/codex-stacks/backups/key-policy-slim-ux-20260704-130238/cpa-key-policy-plus.so.before`.
- SJC `cpa` restarted successfully and loaded `cpa-key-policy-plus.so`.
- Internal admin HTML contains `套用 CPA 发现模型`, `当前 Key`, `需补策略`,
  `quota-editor`, `is-busy`, and `允许全部模型`.
- Internal admin HTML no longer contains `CPA 原生`, `已继承`, `已计价`,
  `quota-mini`, `mini-grid`, or `用当前发现模型替换`.
- Internal model API returned `19` models from `source=cpa_registry` with no
  warnings.
- Public plugin admin paths returned `404`; `cpa-usage.konbakuyomu.us` returned
  `200`.
- Authenticated `gpt-5.5` streaming `/v1/responses` returned HTTP `200` and
  ended with `response.completed`.
