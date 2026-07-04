# Design

## Architecture

The SJC upgrade governor remains host-side and label-driven:

```text
cron 09:30
  -> sjc-auto-upgrade-governor.sh
     -> capacity preflight
     -> upgrade-all.sh --groups docker-app groups
     -> host adapters: docker apt, compose plugin, x-ui
     -> daily-update-report.py post-run summary
```

Docker containers continue to opt in through compose labels:

- `diun.enable=true`
- `autoupgrade.enable=true`
- `autoupgrade.group=<group>`
- optional `autoupgrade.health-url`
- optional `autoupgrade.healthcheck-sec`
- optional `autoupgrade.min-disk-gb`

## Docker Upgrade Contract

- `upgrade-container.sh` is the single-container executor.
- It reads compose directory and service from Docker Compose labels.
- It must not create persistent service backups in normal operation.
- It tags the old image as `<image-base>:rollback` only after a new digest is pulled and before recreate.
- Success deletes the rollback tag and prunes unreferenced images with `docker image prune -f`.
- Failure retags rollback back to the service image reference, recreates the service, and reports failure.
- Dry-run validates labels, compose config, disk, and health without pulling or changing state.

## Host Adapter Contract

Host adapters are independent commands invoked by the SJC orchestrator:

- `docker-apt`: updates Docker packages from the existing apt source, records old/new versions, validates Docker daemon and critical containers after upgrade.
- `docker-compose-plugin`: follows the same apt path for `docker-compose-plugin`, validates `docker compose version` and compose config on known roots.
- `x-ui`: verifies latest release, temporarily preserves old `/usr/local/x-ui/x-ui`, replaces only when a versioned artifact and restart path are available, validates systemd and version, and restores old binary on failure.

Adapter statuses are normalized to:

- `current`
- `upgraded`
- `rolled-back`
- `adapter-blocked`
- `failed`

If an adapter cannot prove rollback or health validation, it must return `adapter-blocked` and skip mutation.

## Capacity And Cleanup

The orchestrator performs a preflight using `sjc-capacity-guard.py --dry-run --no-telegram` and direct `df -h /`.

When space is below the upgrade gate:

- Prefer explicit obsolete backup binaries under `/opt/codex-stacks/backups`.
- Delete only individually named files, never directories or globs.
- Do not touch SQLite DBs, WAL/SHM files, Docker volumes, Redis AOF, Postgres data, live compose roots, or active container files.

## Reporting

`daily-update-report.py` remains read-only. It reports:

- Docker update state.
- Host adapter state.
- Capacity state.
- Upgrade failures and rollbacks.

It must not perform cleanup or upgrades. The orchestrator owns mutation.
