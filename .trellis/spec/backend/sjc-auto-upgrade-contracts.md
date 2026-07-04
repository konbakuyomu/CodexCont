# SJC Auto-Upgrade and Capacity Contracts

## Scenario: SJC low-disk unattended upgrade governor

### 1. Scope / Trigger
- Trigger this spec when changing SJC host-side upgrade scripts, Docker compose autoupgrade labels, daily update reporting, capacity guard integration, or host component adapters.
- SJC is a small 10G root-disk host. Upgrade automation must prefer low-disk rollback over persistent thick backups.
- This is infra work because Docker labels, cron, host package upgrades, container health checks, Telegram reporting, and capacity guard thresholds must agree.

### 2. Signatures
- Cron entry:
  `30 9 * * * /usr/local/bin/sjc-auto-upgrade-governor.sh >> /var/log/sjc-auto-upgrade-governor.log 2>&1`
- Docker app upgrade:
  `/usr/local/bin/upgrade-container.sh [--dry-run] <container_name>`
- Batch upgrade:
  `/usr/local/bin/upgrade-all.sh [--dry-run] [--group <autoupgrade.group>]`
- Host adapters:
  `/usr/local/bin/sjc-host-adapter-docker-apt.sh [--dry-run]`
  `/usr/local/bin/sjc-host-adapter-x-ui.sh [--dry-run]`
- Daily report:
  `/usr/local/bin/daily-update-report.py --host-label SJC [--dry-run] [--no-telegram]`
- Adapter state file:
  `/var/lib/sjc-auto-upgrade-governor/state.json`

### 3. Contracts
- Docker enrollment is label-driven. A service must opt in with `autoupgrade.enable=true`; grouping uses `autoupgrade.group=<group>`.
- SJC Docker app services should use `autoupgrade.strategy=rollback-tag-only` and a realistic low-disk gate such as `autoupgrade.min-disk-gb=0.75`.
- `upgrade-container.sh` must not create persistent thick backups. It may tag the old image as `<image-base>:rollback` during an upgrade attempt, but success must remove that tag.
- Dry-run must validate container existence, labels, compose config, disk gate, and health without pulling images or changing runtime state.
- Host adapters must normalize status to `current`, `upgraded`, `rolled-back`, `adapter-blocked`, or `failed`.
- Host adapters may mutate only when they can prove a preflight path, a post-upgrade health gate, and a rollback path. Otherwise they must write `adapter-blocked`.
- Daily report is read-only. It may read adapter state, Docker remote digests, and capacity snapshots, but must not upgrade, clean, delete, or restart anything.
- Secrets must never be printed or written to task artifacts: Telegram tokens, admin keys, API keys, OAuth data, cookies, request bodies, response bodies, subscription URLs, and private keys.

### 4. Validation & Error Matrix
- Free space below the configured gate -> skip upgrade and report disk-low; do not broaden cleanup automatically.
- Compose labels missing -> skip and report missing compose metadata.
- Image reference is digest-only -> skip because rollback tag cannot be created safely.
- Pull fails -> leave running container unchanged and report pull failure.
- Health gate fails after recreate -> retag rollback image to the service image, recreate, and report `rolled-back` or failure.
- Host package old versions are unavailable in apt metadata -> adapter returns `adapter-blocked`.
- x-ui latest release asset cannot be determined or downloaded -> adapter returns `adapter-blocked` or `failed`, and leaves the running service untouched.
- Daily report sees host component updates but no adapter state -> report `adapter-blocked`, not manual-only.

### 5. Good/Base/Bad Cases
- Good: `cpamp` has a new digest, the host has enough free space, the old image is tagged as rollback, the service recreates healthy, the rollback tag is deleted, and daily report shows zero pending Docker updates.
- Good: Docker Engine and Compose update through apt, Docker validates with `docker version`, `docker compose version`, `docker ps`, and critical containers running; adapter state records `upgraded`.
- Base: A service is current by digest; upgrade exits without recreating the container.
- Base: A fixed-tag or database container is not labeled for autoupgrade and remains report-only or excluded.
- Bad: Adding a new whitelist file in addition to compose labels. This creates dual source of truth.
- Bad: Running broad `docker system prune`, recursive backup deletion, or volume deletion to make room for upgrades.
- Bad: Daily report performs cleanup or upgrades. Reporting and mutation must stay separate.

### 6. Tests Required
- Shell syntax: `bash -n` for every installed shell script.
- Python syntax: `python3 -m py_compile /usr/local/bin/daily-update-report.py`.
- Dry-run: `upgrade-container.sh --dry-run <service>` must not pull or recreate.
- Compose validation: `docker compose -f <compose-file> config` for every changed compose root.
- Docker app validation: health URL or Docker health/running state after upgrade.
- Host adapter validation: versions before/after, `systemctl is-active docker`, `systemctl is-active x-ui`, `docker ps`, and critical service health.
- Reporting validation: `daily-update-report.py --host-label SJC --dry-run --no-telegram` shows no pending updates/failures after a successful rollout.

### 7. Wrong vs Correct

#### Wrong
```bash
docker system prune -af
rm -rf /opt/codex-stacks/backups
```

This can remove unrelated images, volumes, or rollback material on a small production host.

#### Correct
```bash
rm -- '/opt/codex-stacks/backups/<explicit-old-plugin-dir>/cpa-key-policy-plus.so'
/usr/local/bin/upgrade-container.sh cpamp
```

Cleanup is explicit single-file deletion, and the upgrade path owns temporary rollback and health verification.

#### Wrong
```text
daily-update-report.py -> sees Docker Engine update -> tells user to handle it manually forever
```

This leaves unattended maintenance incomplete.

#### Correct
```text
daily-update-report.py -> reads adapter state -> reports current/upgraded/rolled-back/adapter-blocked/failed
sjc-auto-upgrade-governor.sh -> runs the mutating adapter when rollback and health gates exist
```

The report remains read-only while the orchestrator performs safe automated work.
