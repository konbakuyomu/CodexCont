# Implementation Plan

## Steps

1. Snapshot LAX state: running containers, labels, compose roots, versions, cron, 1Panel real units, and daily report.
2. Update repo spec and task artifacts.
3. Install runtime scripts with timestamped backups:
   - `/usr/local/bin/upgrade-container.sh`
   - `/usr/local/bin/upgrade-all.sh`
   - `/usr/local/bin/lax-auto-upgrade-governor.sh`
   - `/usr/local/bin/lax-host-adapter-docker-apt.sh`
   - `/usr/local/bin/lax-host-adapter-1panel.sh`
   - `/usr/local/bin/daily-update-report.py`
4. Update compose labels and image tags for currently running services only.
5. Recreate only changed services so running labels match compose labels.
6. Run static checks and all dry-runs.
7. Run real rollout serially through the governor.
8. Verify final health, adapter state, daily report, and jump login.
9. Commit, archive task, and record journal.

## Validation Commands

```bash
bash -n /usr/local/bin/upgrade-container.sh
bash -n /usr/local/bin/upgrade-all.sh
bash -n /usr/local/bin/lax-auto-upgrade-governor.sh
bash -n /usr/local/bin/lax-host-adapter-docker-apt.sh
bash -n /usr/local/bin/lax-host-adapter-1panel.sh
python3 -m py_compile /usr/local/bin/daily-update-report.py
/usr/local/bin/lax-capacity-guard.py --dry-run --no-telegram
/usr/local/bin/lax-auto-upgrade-governor.sh --dry-run
/usr/local/bin/upgrade-all.sh --dry-run --group lax-apps
/usr/local/bin/upgrade-all.sh --dry-run --group lax-data
/usr/local/bin/upgrade-all.sh --dry-run --group lax-platform
/usr/local/bin/upgrade-all.sh --dry-run --group lax-network
/usr/local/bin/upgrade-all.sh --dry-run --group lax-observability
/usr/local/bin/lax-host-adapter-1panel.sh --dry-run
/usr/local/bin/daily-update-report.py --host-label LAX --dry-run --no-telegram
```

## Rollback Points

- Timestamped backups under `/usr/local/bin/*.bak-lax-fullcov-*`.
- Timestamped compose backups with suffix `.bak-lax-fullcov-<timestamp>`.
- Container rollback uses temporary `:rollback` image tags.
- Postgres logical dumps are stored root-only under `/var/lib/lax-auto-upgrade-governor/backups/postgres/`.
- 1Panel package/log state is stored under `/var/lib/lax-auto-upgrade-governor/1panel/` and `/var/log/lax-host-adapter-1panel.log`.
