# CPA CodexCont Executor Plugin

`cpa-codexcont-executor` is an executor-only CPA plugin that replaces the old
Docker-hosted CodexCont sidecar for streaming Responses continuation
protection.

It does not own the ordinary user portal. `cpa-key-policy-plus` remains the
owner of `https://cpa-usage.konbakuyomu.us/`, user login, quotas, RPM, and usage
APIs.

## Local Test

```powershell
cd D:\Dev\20_Software\23_Reference\llm-gateway\CodexCont\cpa_codexcont_executor_plugin\go
go test ./...
```

## Linux Build

```bash
cd cpa_codexcont_executor_plugin/go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-codexcont-executor.so .
```

## Minimal CPA Config

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-codexcont-executor:
      enabled: true
      route_enabled: false
      state_db_path: /CLIProxyAPI/plugin-state/cpa-codexcont-executor/executor.sqlite
      fail_mode: fallback
```

`route_enabled: false` leaves CPA's normal upstream route untouched. Turn it on
only after local and server executor folding validation passes.
