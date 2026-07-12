# CPA CodexCont Executor Plugin

`cpa-codexcont-executor` is an executor-only CPA plugin that replaces the old
Docker-hosted CodexCont sidecar for streaming Responses continuation
protection.

It does not own the ordinary user portal. `cpa-key-policy-plus` remains the
owner of `https://cpa-usage.konbakuyomu.us/`, user login, quotas, RPM, and usage
APIs.

It does own the read-only CPAMP-side `CodexCont Executor` monitor. That page is
for rolling protection summaries and executor health only; it must not expose
user login, key editing, quota editing, raw request/response bodies, or
encrypted reasoning.

The plugin declares non-exclusive `frontend_auth_provider=true` and
`usage_plugin=true` only so CPA/CPAMP registers its resource menu.
`frontend_auth.authenticate` always returns unauthenticated and `usage.handle`
is intentionally a no-op; billing, quota, RPM, and user usage remain owned by
`cpa-key-policy-plus`.

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

## Production Route Policy (GPT-5.5 / GPT-5.6 only)

When `route_enabled: true`, leave `route_policy.mode` unset to keep the legacy
protect-all behavior. For production continuation folding limited to the GPT-5.5
and GPT-5.6 families:

```yaml
plugins:
  configs:
    cpa-codexcont-executor:
      enabled: true
      route_enabled: true
      fail_mode: fallback
      route_policy:
        mode: protect_selected
        protected_models:
          - gpt-5.5      # also matches gpt-5.5-pro / gpt-5.5-2026-04-23
          - gpt-5.6*     # gpt-5.6, gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, ...
        # Optional canary: one test key skips CodexCont and uses plain CPA path.
        # bypass_key_aliases:
        #   - baseline-cpa-only
```

Matching rules for `protected_models` / `bypass_models`:

- exact ID (case-insensitive): `gpt-5.5`
- trailing wildcard: `gpt-5.6*` matches any model with that prefix
- family prefix: `gpt-5.5` matches `gpt-5.5-pro` / dated snapshots, but not `gpt-5.4`

Non-selected models (for example `gpt-5.4`, `gpt-5.4-mini`, `grok-4.5`) bypass
the executor and use CPA's normal upstream path. `route_enabled: false` remains
the global kill switch.

Both `plugin.register` and `plugin.reconfigure` must return the full plugin
registration object. CPA decodes reconfigure through the same metadata and
capability path; a lightweight acknowledgement causes CPA to drop the plugin
from the active resource snapshot.

## CPAMP Monitor

The plugin registers a read-only admin resource:

```text
/v0/resource/plugins/cpa-codexcont-executor/admin
```

The page polls the plugin's safe status and summary endpoints and is intended
to replace Governor's CodexCont realtime monitoring role during cleanup.
