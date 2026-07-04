# Evidence

## Local

- `go test ./... -count=1 -timeout=120s` passed in
  `cpa_codexcont_executor_plugin/go`.
- `git diff --check` passed.
- Linux build used existing Go runtime:
  `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go`.
- New artifact:
  `D:/Dev/20_Software/_LocalRuntime/CodexCont/build/codex-app-upstream-error-context-window/cpa-codexcont-executor.so`
- New artifact SHA256:
  `9f5f557101e058e1546b2818abfe0a9926e03cfa49286bac8e928ef007ac9dbe`.

## Production

- Backup path:
  `/opt/codex-stacks/backups/codex-app-upstream-error-context-window-20260704-090651`.
- Previous executor artifact SHA256:
  `14763ebbb43e2505aae381e0970e09c2a07bc913d424d8e4313e7170c0383919`.
- Deployed executor artifact SHA256:
  `9f5f557101e058e1546b2818abfe0a9926e03cfa49286bac8e928ef007ac9dbe`.
- CPA restart loaded and registered `cpa-codexcont-executor` and
  `cpa-key-policy-plus`.
- Production executor config no longer includes `upstream_model_aliases` for
  `gpt-5.5` or `gpt-5.4`.
- `/v1/models?client_version=codex-app` returned `gpt-5.5`.
- Live streaming `/v1/responses` with `gpt-5.5` returned `LIVE_OK`.
- Latest executor summary after the smoke request:
  `requested_model=gpt-5.5`, `model=gpt-5.5`, `body_model=gpt-5.5`,
  `protection=protected_clean`, `status=completed`.
- Public boundaries returned 404 for:
  `/v0/resource/plugins/cpa-codexcont-executor/admin`,
  `/plugins/cpa-codexcont-executor/status`, and `/v0/management`.
