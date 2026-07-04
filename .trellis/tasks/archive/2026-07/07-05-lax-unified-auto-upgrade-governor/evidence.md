# Evidence

Evidence is redacted and excludes secrets, private URLs, request bodies, tokens, cookies, private keys, and 1Panel entrance details.

## Before Snapshot

- Host: `lax-hostdzire`, reached via `ssh lax-via-sjc`.
- Time: 2026-07-05 07:07 CST.
- Root disk: `/dev/sda2`, 99G size, 37G used, 59G available, 39%.
- Docker before: Engine `29.5.3`, Compose plugin `5.1.4`.
- Cron before:
  - `30 9 * * * /usr/local/bin/upgrade-all.sh >> /var/log/upgrade-container.log 2>&1`
  - `10 10 * * * /usr/local/bin/daily-update-report.py --host-label LAX >> /var/log/daily-update-report.log 2>&1`
  - `11 * * * * /usr/local/bin/lax-capacity-guard.py >> /var/log/lax-capacity-guard.log 2>&1`
- Auto labels before had no group, mostly `strategy=full`, and disk gates of blank or `5`.
- Docker apt candidates before:
  - `docker-ce`: `29.5.3` -> `29.6.1`
  - `docker-ce-cli`: `29.5.3` -> `29.6.1`
  - `docker-compose-plugin`: `5.1.4` -> `5.3.0`
  - `docker-buildx-plugin`: `0.34.1` -> `0.35.0`
  - `containerd.io`: `2.2.4` -> `2.2.5`
- Daily report before: 5 attention items, including auto-upgrade `grok2api-jiujiu`, notify-only `kuma-openwebui`, and manual `docker-engine`, `docker-compose`, `1panel`.

## Rollout

- Installed updated scripts on LAX:
  - `/usr/local/bin/upgrade-container.sh`
  - `/usr/local/bin/upgrade-all.sh`
  - `/usr/local/bin/lax-auto-upgrade-governor.sh`
  - `/usr/local/bin/lax-host-adapter-docker-apt.sh`
  - `/usr/local/bin/daily-update-report.py`
- Old LAX upgrade scripts were backed up as:
  - `/usr/local/bin/upgrade-container.sh.bak-lax-autogov-20260705-071219`
  - `/usr/local/bin/upgrade-all.sh.bak-lax-autogov-20260705-071219`
- Old LAX daily report artifact was backed up as:
  - `/usr/local/bin/daily-update-report.py.bak-lax-before-autogov-20260705-071258`
- Updated six eligible app compose services with `autoupgrade.group=lax-apps`, `autoupgrade.strategy=rollback-tag-only`, and `autoupgrade.min-disk-gb=20`:
  - `codemerge-static`
  - `fast-note-sync-service`
  - `grok2api-jiujiu`
  - `new-api`
  - `prompt-manager`
  - `sub2api-cpa-poc`
- Compose backups were created with suffix `.bak-lax-autogov-20260705-071355`.
- Recreated only those six app services with `--no-deps --force-recreate` so running labels matched compose labels.
- Health after label refresh:
  - `codemerge-static`: HTTP 200, Docker healthy.
  - `fast-note-sync-service`: HTTP 200.
  - `grok2api-jiujiu`: HTTP 200, Docker healthy.
  - `new-api`: HTTP 200, Docker healthy.
  - `prompt-manager`: HTTP 200, Docker healthy.
  - `sub2api-cpa-poc`: HTTP 200, Docker healthy.
- Cron after:
  - `30 9 * * * /usr/local/bin/lax-auto-upgrade-governor.sh >> /var/log/lax-auto-upgrade-governor.log 2>&1`
  - Daily report and capacity guard cron remain.

## Validation

- Static validation passed:
  - `bash -n` for all installed shell scripts.
  - `python3 -m py_compile /usr/local/bin/daily-update-report.py`.
- Capacity dry-run reported `ok`, about 58G free, all critical services healthy, and no cleanup triggered.
- `upgrade-all.sh --dry-run --group lax-apps` passed for all six eligible apps.
- `lax-auto-upgrade-governor.sh --dry-run --group lax-apps` passed Docker app dry-run, Docker apt adapter dry-run path, and report rendering.
- Docker apt adapter upgraded successfully:
  - Docker Engine: `29.5.3` -> `29.6.1`
  - Docker Compose plugin: `5.1.4` -> `5.3.0`
  - Docker Buildx plugin: `0.34.1` -> `0.35.0`
  - containerd.io: `2.2.4` -> `2.2.5`
  - Adapter state: `docker-engine=upgraded`, `docker-compose=upgraded`
- Docker app rollout:
  - `grok2api-jiujiu`: digest `bd044af5ca84` -> `7ad4c7cd400d`, recreated, health passed.
  - `codemerge-static`, `fast-note-sync-service`, `new-api`, `prompt-manager`, and `sub2api-cpa-poc`: already current/no-op.
  - No `:rollback` tags remained after success.
- Final versions:
  - Docker Engine `29.6.1`
  - Docker Compose plugin `5.3.0`
  - Docker service active.
- Final daily report:
  - 2 attention items remain: notify-only `kuma-openwebui` and manual `1panel`.
  - Docker Engine and Compose are no longer manual chores; the first post-upgrade report showed `docker-engine=upgraded`, `docker-compose=upgraded`.
  - A later dry-run spot-check after packages were already current refreshed adapter state to `docker-engine=current`, `docker-compose=current`; this still satisfies the reporting contract because Docker/Compose are handled by the adapter, not listed as manual-only chores.
  - Capacity state remains `ok`, about 58G free, all critical services healthy.
- Final jump-login check:
  - `ssh lax-via-sjc hostname` returned `lax-hostdzire`.

## Final Spot Check

- 2026-07-05 07:24-07:27 CST live checks still passed after rollout:
  - `daily-update-report.py --host-label LAX --dry-run --no-telegram`: 2 attention items only, `kuma-openwebui` notify-only and `1panel` manual; Docker/Compose reported through adapter state as `current`.
  - `lax-auto-upgrade-governor.sh --dry-run --group lax-apps`: capacity preflight, six app dry-runs, Docker apt adapter, and report rendering completed.
  - `lax-host-adapter-docker-apt.sh --dry-run`: packages already current.
  - Docker Engine `29.6.1`, Docker Compose plugin `5.3.0`, Docker service `active`.
  - Six eligible containers retained `autoupgrade.enable=true`, `autoupgrade.group=lax-apps`, `autoupgrade.strategy=rollback-tag-only`, and `autoupgrade.min-disk-gb=20`.
  - Compose config passed for all six changed compose roots.
  - No `:rollback` image tags remained.
  - `1panel` systemd status remained `inactive`.

## Notes

- A Windows CRLF line ending in one temporary check script caused the standalone adapter check line to run the real Docker apt adapter instead of passing `--dry-run`. The adapter completed successfully and wrote normal `upgraded` state. Installed runtime scripts were validated separately and are not CRLF-corrupted.
- OpenWebUI remains notify-only by design.
- 1Panel remains manual/report-only by design.
