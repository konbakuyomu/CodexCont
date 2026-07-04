# Implementation Plan

## Ordered Steps

1. Capture current SJC state: disk, Docker labels, daily report, upgrade logs, package versions, x-ui status.
2. Identify explicit old backup binary files under `/opt/codex-stacks/backups` that can be removed without touching live state or data files.
3. Delete approved obsolete backup files one explicit path at a time until `/` is at least 1.5G free.
4. Install updated host scripts in a staging path first, then validate with `bash -n`.
5. Replace `/usr/local/bin/upgrade-container.sh` with the low-disk rollback implementation.
6. Add `/usr/local/bin/sjc-auto-upgrade-governor.sh` and host adapter scripts under `/usr/local/bin` or `/etc/auto-upgrade.d/lib`.
7. Update `shared-hy2` compose labels to use a realistic low-disk gate while keeping its separate group.
8. Update cron so `09:30` runs the orchestrator instead of calling `upgrade-all.sh` directly.
9. Dry-run the orchestrator and the specific `cpamp` / `frontier-sub-store` upgrades.
10. Run real Docker upgrades for `cpamp` and `frontier-sub-store`.
11. Run host adapter dry-runs; run real host adapters only when rollback and validation contracts are satisfied.
12. Update daily report classification to surface adapter statuses instead of hard-coded manual-only language.
13. Run full validation and record evidence in this task.

## Validation Commands

```bash
bash -n /usr/local/bin/upgrade-container.sh
bash -n /usr/local/bin/upgrade-all.sh
bash -n /usr/local/bin/sjc-auto-upgrade-governor.sh
/usr/local/bin/sjc-capacity-guard.py --dry-run --no-telegram
/usr/local/bin/daily-update-report.py --host-label SJC --dry-run --no-telegram
/usr/local/bin/upgrade-container.sh --dry-run cpamp
/usr/local/bin/upgrade-container.sh --dry-run frontier-sub-store
docker compose -f /opt/codex-stacks/cpamp/compose.yaml config
docker compose -f /opt/frontier/sub-store/docker-compose.yml config
docker ps --format '{{.Names}}\t{{.Status}}\t{{.Image}}'
docker exec cpamp wget -qO- http://127.0.0.1:18317/health
docker version --format '{{.Server.Version}}'
docker compose version --short
systemctl is-active docker
systemctl is-active x-ui
/usr/local/x-ui/x-ui -v
```

## Rollback Points

- Before replacing host scripts, copy each old script to a timestamped single file.
- Docker service rollback is handled by temporary `:rollback` tags.
- x-ui rollback is handled by a temporary old binary copy until version and systemd validation pass.
- If Docker apt adapter changes fail, verify package manager rollback availability before enabling real adapter execution; otherwise leave it `adapter-blocked`.

## Safety Notes

- No recursive deletes, no `rm -rf`, no `docker system prune`, no Docker volume deletion.
- Use explicit single-file deletes only.
- Do not print Telegram tokens, admin keys, API keys, OAuth data, cookies, request bodies, response bodies, or subscription URLs.
