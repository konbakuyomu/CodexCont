# LAX Auto-Upgrade and Capacity Contracts

## Scenario: LAX main-business unattended upgrade governor

### 1. Scope / Trigger
- Trigger this spec when changing LAX host-side upgrade scripts, Docker compose autoupgrade labels, daily update reporting, capacity guard integration, or Docker apt adapter behavior.
- LAX is reached through SJC + WireGuard with `ssh lax-via-sjc`; public direct SSH failure is expected.
- LAX has a larger root disk than SJC, so its disk gate and capacity thresholds differ from SJC.

### 2. Signatures
- Cron entry:
  `30 9 * * * /usr/local/bin/lax-auto-upgrade-governor.sh >> /var/log/lax-auto-upgrade-governor.log 2>&1`
- Docker app upgrade:
  `/usr/local/bin/upgrade-container.sh [--dry-run] <container_name>`
- Batch upgrade:
  `/usr/local/bin/upgrade-all.sh [--dry-run] [--group <autoupgrade.group>]`
- Host adapter:
  `/usr/local/bin/lax-host-adapter-docker-apt.sh [--dry-run]`
- Daily report:
  `/usr/local/bin/daily-update-report.py --host-label LAX [--dry-run] [--no-telegram]`
- Adapter state file:
  `/var/lib/lax-auto-upgrade-governor/state.json`

### 3. Contracts
- Docker app enrollment is label-driven. A service must opt in with `autoupgrade.enable=true`; grouping uses `autoupgrade.group=lax-apps`.
- Eligible LAX app services should use `autoupgrade.strategy=rollback-tag-only` and `autoupgrade.min-disk-gb=20`.
- `upgrade-container.sh` must not create persistent thick backups. It may tag the old image as `<image-base>:rollback` during an upgrade attempt, but success must remove that tag.
- Dry-run must validate container existence, labels, compose config, disk gate, and health without pulling images or changing runtime state.
- Host adapter statuses are `current`, `upgraded`, `rolled-back`, `adapter-blocked`, or `failed`.
- The Docker apt adapter may mutate only when rollback package versions are available and Docker health validation is defined.
- Daily report is read-only. It may read adapter state, Docker remote digests, and capacity snapshots, but must not upgrade, clean, delete, or restart anything.
- 1Panel remains report-only in v1. It is an auxiliary management layer on LAX, not the source of truth for production ingress.
- OpenWebUI remains notify-only because local custom static UI history makes generic silent upgrades unsafe.
- Secrets must never be printed or written to task artifacts.

### 4. Validation & Error Matrix
- Free space below the configured gate -> skip upgrade and report disk-low; do not broaden cleanup automatically.
- Compose labels missing -> skip and report missing compose metadata.
- Image reference is digest-only -> skip because rollback tag cannot be created safely.
- Pull fails -> leave running container unchanged and report pull failure.
- Health gate fails after recreate -> retag rollback image to the service image, recreate, and report `rolled-back` or failure.
- Host package old versions are unavailable in apt metadata -> adapter returns `adapter-blocked`.
- Daily report sees Docker Engine / Compose updates but no adapter state -> report `adapter-blocked`, not permanent manual-only work.
- LAX direct SSH failure is not a rollout failure if `ssh lax-via-sjc` succeeds.

### 5. Good/Base/Bad Cases
- Good: a labeled LAX app has a new digest, host free space is above 20G, the old image is tagged as rollback, the service recreates healthy, and the rollback tag is removed.
- Good: Docker Engine and Compose update through apt, Docker validates with `docker version`, `docker compose version`, `docker ps`, and critical containers running; adapter state records `upgraded`.
- Base: a service is current by digest; upgrade exits without recreating the container.
- Base: 1Panel and OpenWebUI are not auto-upgraded and remain report-only.
- Bad: adding a separate whitelist file in addition to compose labels.
- Bad: running `docker system prune`, `docker image prune -af`, volume deletion, recursive deletion, package purge/remove/autoremove, or database cleanup to make room for upgrades.
- Bad: daily report performs cleanup or upgrades.

### 6. Tests Required
- Shell syntax: `bash -n` for every installed shell script.
- Python syntax: `python3 -m py_compile /usr/local/bin/daily-update-report.py`.
- Dry-run: `upgrade-container.sh --dry-run <service>` and `upgrade-all.sh --dry-run --group lax-apps` must not pull or recreate.
- Compose validation: `docker compose -f <compose-file> config` for every changed compose root.
- Docker app validation: health URL or Docker health/running state after upgrade.
- Host adapter validation: versions before/after, `systemctl is-active docker`, `docker version`, `docker compose version`, `docker ps`, and critical service health.
- Reporting validation: `daily-update-report.py --host-label LAX --dry-run --no-telegram` shows adapter statuses and does not list Docker Engine / Compose as manual-only chores after successful rollout.
