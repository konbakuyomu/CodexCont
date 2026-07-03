# Implementation Plan

1. Load applicable Trellis specs and current task artifacts.
2. Add Plus metadata fields:
   - include `key_alias` alongside `key_id`, `key_name`, `preview`, `source`.
3. Add executor identity helper:
   - merge safe fields from `AuthID`, `AuthMetadata`, `AuthAttributes`, and
     `Metadata`;
   - if CPA does not pass Plus metadata, derive native id/preview from the
     inbound Authorization hash and read CPAMP alias from `cpamp_alias_db_path`;
   - use it in `summaryMonitor.Start` and `saveFoldSummary`;
   - persist `key_id` from normalized identity.
4. Update Executor admin UI:
   - add helpers for protection labels, key display, and short request ids;
   - render request column as key/status label with short id subline;
   - expand details with safe identity fields.
5. Update Plus user feed behavior:
   - keep current-key-only filtering;
   - ensure executor DB empty-current-key case is not an error;
   - update empty state text in user HTML.
6. Add/adjust tests in both plugin packages.
7. Run local tests:
   - `go test ./... -count=1 -timeout=120s` in both plugin dirs;
   - `git diff --check`.
8. Build Linux artifacts using existing WSL Go:
   - no Go download;
   - `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared`.
9. Deploy to SJC if local validation is green:
   - backup current plugin `.so` files;
   - upload changed `.so` files;
   - restart only CPA;
   - verify admin/user pages and public boundaries.
10. Update spec if new contracts were learned, commit, archive, and record
    journal.

## Completion Evidence

- Windows tests passed:
  - `go test ./... -count=1 -timeout=120s` in
    `cpa_codexcont_executor_plugin/go`
  - `go test ./... -count=1 -timeout=120s` in
    `cpa_key_policy_plus_plugin/go`
- WSL tests passed with existing Go:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`
- Linux build evidence:
  - executor SHA256
    `14763ebbb43e2505aae381e0970e09c2a07bc913d424d8e4313e7170c0383919`
  - Plus SHA256
    `993eef2f68889e13879cb7bcb493135cb41b7d48caa1ce77dc7dcf4a57f35d84`
- SJC deployment:
  - final backup:
    `/opt/codex-stacks/backups/codexcont-identity-feed-v4-20260704-074046`
  - added executor `cpamp_alias_db_path:
    /CLIProxyAPI/cpamp-data/usage.sqlite`
  - restarted only `cpa`
- Live smoke:
  - `/v1/responses` with an enabled native key returned HTTP 200.
  - latest executor summary carried `native_81197850_d80b6d`,
    `kuma的官key`, and `protected_clean`.
  - Plus `/user/api/codexcont` for that same current key returned the new
    current-key record from `codexcont_executor_store`.
  - public admin/plugin paths on `cpa.konbakuyomu.us` returned 404.
