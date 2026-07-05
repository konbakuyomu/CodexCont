# LAX Auto-Upgrade and Capacity Contracts

## Scenario: LAX main-business full-coverage unattended upgrade governor

### 1. Scope / Trigger
- Trigger this spec when changing LAX host-side upgrade scripts, Docker compose autoupgrade labels, daily update reporting, capacity guard integration, Docker apt adapter behavior, data-service adapters, OpenWebUI handling, Mihomo handling, Diun handling, or 1Panel automation.
- LAX is reached through SJC + WireGuard with `ssh lax-via-sjc`; public direct SSH failure is expected.
- LAX has a larger root disk than SJC, so its disk gate and capacity thresholds differ from SJC.
- LAX full coverage means the currently running Docker services plus Docker/Compose and 1Panel. It does not mean stopped projects, historical compose backups, whole-system apt, or kernel upgrades.

### 2. Signatures
- Cron entry:
  `30 9 * * * /usr/local/bin/lax-auto-upgrade-governor.sh >> /var/log/lax-auto-upgrade-governor.log 2>&1`
- Docker app upgrade:
  `/usr/local/bin/upgrade-container.sh [--dry-run] <container_name>`
- Batch upgrade:
  `/usr/local/bin/upgrade-all.sh [--dry-run] [--group <autoupgrade.group>]`
- Host adapter:
  `/usr/local/bin/lax-host-adapter-docker-apt.sh [--dry-run]`
  `/usr/local/bin/lax-host-adapter-1panel.sh [--dry-run]`
- Daily report:
  `/usr/local/bin/daily-update-report.py --host-label LAX [--dry-run] [--no-telegram]`
- Adapter state file:
  `/var/lib/lax-auto-upgrade-governor/state.json`

### 3. Contracts
- Docker enrollment is label-driven. A service must opt in with `autoupgrade.enable=true`; grouping uses `autoupgrade.group=<group>`.
- Covered groups are `lax-apps`, `lax-data`, `lax-platform`, `lax-network`, and `lax-observability`.
- Covered services should use `autoupgrade.strategy=rollback-tag-only`, `autoupgrade.min-disk-gb=20`, and a concrete `autoupgrade.adapter`.
- Adapter names currently used on LAX are `app`, `postgres-same-major`, `redis-same-major`, `openwebui-assets`, `mihomo-config`, and `running-only`.
- `upgrade-container.sh` must not create persistent thick backups. It may tag the old image as `<image-base>:rollback` during an upgrade attempt, but success must remove that tag.
- Dry-run must validate container existence, labels, compose config, disk gate, and health without pulling images or changing runtime state.
- Host adapter statuses are `current`, `upgraded`, `rolled-back`, `adapter-blocked`, or `failed`.
- The Docker apt adapter may mutate only when rollback package versions are available and Docker health validation is defined.
- The 1Panel adapter must not use `1pctl update` as a program upgrade. It may use the official v2 offline package resource path with checksum validation and must preserve the real pre-upgrade service state of `1panel-core` and `1panel-agent`.
- If the public 1Panel release package contains only `install.sh` and no `upgrade.sh`, the adapter must record `adapter-blocked`. Do not treat `install.sh` as an upgrade path because it is an installer with existing-install guards and interactive/configuration branches.
- Postgres and Redis adapters are same-major only. Cross-major migrations are separate tasks.
- OpenWebUI must preserve static assets mounted at `/app/build/static/custom.css` and `/app/build/static/loader.js`.
- Mihomo must validate config with `/mihomo -t -d /root/.config/mihomo`.
- Daily report is read-only. It may read adapter state, Docker remote digests, and capacity snapshots, but must not upgrade, clean, delete, or restart anything.
- Daily report must not leave covered running services as permanent notify-only or manual-only chores after adapters are installed.
- Daily report must use governor adapter state as a fallback for `autoupgrade.enable=true` containers when remote registry digest probing is unavailable. A registry probe failure must not override a recent `current` or `upgraded` adapter state, but `adapter-blocked`, `rolled-back`, and `failed` states must remain visible.
- Secrets must never be printed or written to task artifacts.

### 4. Validation & Error Matrix
- Free space below the configured gate -> skip upgrade and report disk-low; do not broaden cleanup automatically.
- Compose labels missing -> skip and report missing compose metadata.
- Image reference is digest-only -> skip because rollback tag cannot be created safely.
- Postgres/Redis old and desired image majors differ -> skip and report `adapter-blocked`; do not attempt cross-major migration.
- Pull fails -> leave running container unchanged and report pull failure.
- Health gate fails after recreate -> retag rollback image to the service image, recreate, and report `rolled-back` or failure.
- Host package old versions are unavailable in apt metadata -> adapter returns `adapter-blocked`.
- Official 1Panel package or checksum cannot be fetched or verified -> adapter returns `adapter-blocked` or `failed`; do not fake coverage.
- Official 1Panel package verifies but lacks `upgrade.sh` -> adapter returns `adapter-blocked`; do not run `install.sh`.
- Daily report sees Docker Engine / Compose updates but no adapter state -> report `adapter-blocked`, not permanent manual-only work.
- LAX direct SSH failure is not a rollout failure if `ssh lax-via-sjc` succeeds.

### 5. Good/Base/Bad Cases
- Good: a labeled LAX app has a new digest, host free space is above 20G, the old image is tagged as rollback, the service recreates healthy, and the rollback tag is removed.
- Good: a Postgres or Redis container keeps the same major image tag, validates before and after, and dependent apps remain healthy.
- Good: OpenWebUI updates, remains HTTP 200 on `/health`, and static assets remain mounted.
- Good: 1Panel upgrades through verified offline package and `1panel-core` / `1panel-agent` remain in their pre-upgrade active or inactive states.
- Good: Docker Engine and Compose update through apt, Docker validates with `docker version`, `docker compose version`, `docker ps`, and critical containers running; adapter state records `upgraded`.
- Base: a service is current by digest; upgrade exits without recreating the container.
- Bad: adding a separate whitelist file in addition to compose labels.
- Bad: running `docker system prune`, `docker image prune -af`, volume deletion, recursive deletion, package purge/remove/autoremove, or database cleanup to make room for upgrades.
- Bad: daily report performs cleanup or upgrades.

### 6. Tests Required
- Shell syntax: `bash -n` for every installed shell script.
- Python syntax: `python3 -m py_compile /usr/local/bin/daily-update-report.py`.
- Dry-run: `upgrade-container.sh --dry-run <service>` and `upgrade-all.sh --dry-run --group lax-apps` must not pull or recreate.
- Dry-run: all groups must pass: `lax-apps`, `lax-data`, `lax-platform`, `lax-network`, and `lax-observability`.
- Compose validation: `docker compose -f <compose-file> config` for every changed compose root.
- Docker app validation: health URL or Docker health/running state after upgrade.
- Data validation: `pg_isready` for Postgres and `redis-cli ping` for Redis.
- Network validation: `/mihomo -t -d /root/.config/mihomo`.
- Host adapter validation: versions before/after, `systemctl is-active docker`, `docker version`, `docker compose version`, `docker ps`, 1Panel version, 1Panel unit states, and critical service health.
- Reporting validation: `daily-update-report.py --host-label LAX --dry-run --no-telegram` shows adapter statuses and does not list covered services as manual-only chores after successful rollout.

### 7. Wrong vs Correct
#### Wrong
- Seeing a verified 1Panel public release tarball with `install.sh` and running it as the upgrade path.
- Treating `docker buildx imagetools inspect` failure as a Docker service failure after the governor has already recorded `current` or `upgraded`.

#### Correct
- Run only a verified official `upgrade.sh` for 1Panel program upgrades. If the package has no `upgrade.sh`, record `adapter-blocked` and keep the existing 1Panel service state unchanged.
- For `autoupgrade.enable=true` containers, let the daily report fall back to `/var/lib/lax-auto-upgrade-governor/state.json` when remote digest probing is unavailable, while still surfacing adapter failure states.
