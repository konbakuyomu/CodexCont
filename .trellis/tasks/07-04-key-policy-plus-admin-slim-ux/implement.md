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
