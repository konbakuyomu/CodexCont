# Implementation Plan

## Ordered Steps

1. Record the current LAX state: disk, Docker versions, package candidates, cron, scripts, labels, compose roots, daily report, and health endpoints.
2. Stage local task artifacts and companion backend spec for LAX auto-upgrade contracts.
3. Install updated LAX scripts with timestamped backups:
   - `upgrade-container.sh`
   - `upgrade-all.sh`
   - `lax-auto-upgrade-governor.sh`
   - `lax-host-adapter-docker-apt.sh`
   - `daily-update-report.py`
4. Update eligible LAX compose labels to `group=lax-apps`, `strategy=rollback-tag-only`, `min-disk-gb=20`.
5. Replace the `09:30` cron entry with the LAX governor; keep daily report and capacity guard cron.
6. Run static validation and dry-runs.
7. Run eligible Docker app upgrades serially through `upgrade-all.sh --group lax-apps`.
8. Run Docker apt adapter dry-run; run real adapter only if rollback versions and health checks are available.
9. Run final daily report and LAX/SJC jump login checks.
10. Record evidence, update acceptance criteria, and archive the task.

## Validation Commands

```bash
bash -n /usr/local/bin/upgrade-container.sh
bash -n /usr/local/bin/upgrade-all.sh
bash -n /usr/local/bin/lax-auto-upgrade-governor.sh
bash -n /usr/local/bin/lax-host-adapter-docker-apt.sh
python3 -m py_compile /usr/local/bin/daily-update-report.py
/usr/local/bin/lax-capacity-guard.py --dry-run --no-telegram
/usr/local/bin/daily-update-report.py --host-label LAX --dry-run --no-telegram
/usr/local/bin/upgrade-all.sh --dry-run --group lax-apps
/usr/local/bin/lax-auto-upgrade-governor.sh --dry-run --group lax-apps
/usr/local/bin/lax-host-adapter-docker-apt.sh --dry-run
docker version --format '{{.Server.Version}}'
docker compose version --short
systemctl is-active docker
docker ps --format '{{.Names}}\t{{.Status}}\t{{.Image}}'
```

## Rollback Points

- Restore timestamped backups under `/usr/local/bin/*.bak-lax-autogov-*`.
- Restore old cron from the before snapshot.
- For app upgrade failures, rely on `:rollback` tag restoration in `upgrade-container.sh`.
- For Docker apt failures, adapter attempts apt downgrade only when old versions are available.

## Safety Notes

- Do not print Telegram tokens, API keys, cookies, request bodies, subscription URLs, private keys, or 1Panel entrance details.
- Do not recursively delete anything.
- Do not run `docker system prune`, `docker image prune -af`, volume prune, package purge/remove/autoremove, or database cleanup.
