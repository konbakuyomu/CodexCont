# Implementation Checklist

1. Load backend and continuation specs before editing.
2. Update executor config defaults and normalization so aliases are explicit
   only.
3. Update tests that currently expect default Spark routing.
4. Add stream read error parsing and failed-event emission for upstream
   OpenAI-compatible errors.
5. Add tests for context-window stream error translation and ordinary read
   error fallback.
6. Run local executor tests and `git diff --check`.
7. Build Linux plugin with the existing local Linux Go runtime.
8. Back up SJC executor plugin and CPA config, deploy plugin, remove Spark
   aliases from production config, restart only `cpa`.
9. Verify models endpoint, streaming smoke, executor summary model fields, and
   public route boundaries.
10. Archive Trellis task, update journal/spec if the bug teaches a durable
    contract, commit changes, and report concise evidence.

## Validation Commands

```powershell
go test ./... -count=1 -timeout=120s
git diff --check
```

Linux build uses the existing WSL Go runtime when available:

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-codexcont-executor.so .
```

## Rollback

- Restore backed up `cpa-codexcont-executor.so`.
- Restore backed up `/opt/codex-stacks/cpa/config.yaml` if config rollout
  causes an unexpected routing regression.
- Restart only `cpa`.
