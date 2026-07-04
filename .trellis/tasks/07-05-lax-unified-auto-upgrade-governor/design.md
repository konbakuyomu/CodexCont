# Design

## Architecture

LAX adopts the SJC governor shape with LAX-specific state paths and thresholds:

```text
cron 09:30
  -> lax-auto-upgrade-governor.sh
     -> lax-capacity-guard.py --dry-run --no-telegram
     -> upgrade-all.sh --group lax-apps
     -> lax-host-adapter-docker-apt.sh
     -> daily-update-report.py --host-label LAX --dry-run --no-telegram
```

Docker app enrollment remains label-driven:

- `diun.enable=true`
- `autoupgrade.enable=true`
- `autoupgrade.group=lax-apps`
- `autoupgrade.strategy=rollback-tag-only`
- `autoupgrade.min-disk-gb=20`
- optional `autoupgrade.health-url`
- optional `autoupgrade.healthcheck-sec`

## Docker Upgrade Contract

- `/usr/local/bin/upgrade-container.sh` is the single-container executor.
- It resolves compose root and service from Docker Compose labels.
- Dry-run validates labels, compose config, disk gate, and health without pulling or changing state.
- Real mode pulls, compares digest, tags the old image as `<image-base>:rollback`, recreates one service, validates health, then removes the rollback tag after success.
- Failure retags rollback to the service image reference and recreates the service.
- Success may run `docker image prune -f`; it must not run `docker image prune -af` or `docker system prune`.

## Host Adapter Contract

LAX v1 has one mutating host adapter:

- `/usr/local/bin/lax-host-adapter-docker-apt.sh [--dry-run]`
- Packages: `docker-ce`, `docker-ce-cli`, `docker-compose-plugin`, `docker-buildx-plugin`, `containerd.io`.
- Adapter statuses: `current`, `upgraded`, `rolled-back`, `adapter-blocked`, `failed`.
- It may mutate only when old apt package versions remain available for rollback and post-upgrade Docker validation is defined.
- It validates Docker daemon, Compose, `docker ps`, and critical LAX containers.
- State is written to `/var/lib/lax-auto-upgrade-governor/state.json`.

1Panel is intentionally not a v1 adapter. It remains report-only because LAX uses it as an auxiliary management layer and live systemd status was inactive during planning.

## Reporting

`daily-update-report.py` remains read-only. For `--host-label LAX`, Docker Engine and Compose read adapter status from `/var/lib/lax-auto-upgrade-governor/state.json`. Missing adapter state is reported as `adapter-blocked`, not as permanent manual-only work.

## Rollback

- Remote script backups are copied to timestamped single files before replacement.
- Docker app rollback uses temporary `:rollback` tags.
- Docker apt rollback uses `apt-get install --allow-downgrades` only when old versions are still in apt metadata.
- If validation fails and rollback does not validate, the adapter records `failed` and leaves detailed logs for manual recovery.
