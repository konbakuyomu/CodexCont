# Evidence

Evidence is redacted and excludes secrets, private URLs, request bodies, tokens, cookies, private keys, and 1Panel entrance details.

## Live Snapshot

- Target: `lax-via-sjc` / `lax-hostdzire`.
- Previous governor cron already pointed to `/usr/local/bin/lax-auto-upgrade-governor.sh`.
- Docker Engine was `29.6.1`; Docker Compose plugin was `5.3.0`.
- Daily report still showed 2 attention items: `kuma-openwebui` notify-only and `1panel` manual.
- Running Docker coverage before this task:
  - 6 containers labeled auto-upgrade in `lax-apps`.
  - 9 containers had no `autoupgrade.enable=true`: OpenWebUI, Mihomo, Diun, 3 Postgres containers, 3 Redis containers.
- 1Panel v2 real units were `1panel-core` and `1panel-agent`, both active/running. Legacy `systemctl is-active 1panel` was inactive and is not the source of truth.

## Rollout

- Runtime scripts installed on LAX with timestamped backups:
  - `/usr/local/bin/upgrade-container.sh`
  - `/usr/local/bin/upgrade-all.sh`
  - `/usr/local/bin/lax-auto-upgrade-governor.sh`
  - `/usr/local/bin/lax-host-adapter-docker-apt.sh`
  - `/usr/local/bin/lax-host-adapter-1panel.sh`
  - `/usr/local/bin/daily-update-report.py`
- Compose labels were applied to 15 currently running Docker containers only. `docker compose config` passed for the changed compose roots.
- Pre-label data safety passed:
  - Postgres logical backups were created under `/var/lib/lax-auto-upgrade-governor/backups/postgres/`.
  - Redis persistence preflight passed.
  - Mihomo current image was tagged as `latest` before label refresh to avoid accidental pull during `--pull never`.
- Real rollout ran through `/usr/local/bin/lax-auto-upgrade-governor.sh` on 2026-07-05 around 09:48-09:54 CST.
- Docker/Compose host adapter result: `docker-engine=current`, `docker-compose=current`.
- Docker service adapter state after rollout:
  - `current`: `codemerge-static`, `fast-note-sync-service`, `grok2api-jiujiu`, `new-api`, `prompt-manager`, `sub2api-cpa-poc`, `diun`.
  - `upgraded`: `kuma-openwebui`, `sub2api-egress-router`, `kuma-openwebui-postgres`, `kuma-openwebui-redis`, `new-api-postgres`, `new-api-redis`, `sub2api-cpa-poc-postgres`, `sub2api-cpa-poc-redis`.
- 1Panel adapter result:
  - Current `1pctl version`: `v2.2.1`; latest resource endpoint: `v2.2.2`.
  - Official public release tarball downloaded and checksum-verified.
  - Blocker: the verified public package contains `install.sh` but no `upgrade.sh`. `install.sh` is installer-only and exits when 1Panel is already installed, so it was not used as a program upgrade.
  - State recorded as `1panel=adapter-blocked`; `1panel-core=active`, `1panel-agent=active`, legacy `1panel=inactive` preserved.
- Daily report state fallback was fixed after rollout: remote registry digest probe failures no longer override recent `current` / `upgraded` governor state for auto-upgrade containers.

## Validation

- Static checks passed:
  - `bash -n` for installed shell scripts.
  - `python3 -m py_compile /usr/local/bin/daily-update-report.py`.
- Dry-runs passed before real rollout:
  - `lax-capacity-guard.py --dry-run --no-telegram`.
  - `lax-auto-upgrade-governor.sh --dry-run`.
  - `upgrade-all.sh --dry-run --group lax-apps`.
  - `upgrade-all.sh --dry-run --group lax-data`.
  - `upgrade-all.sh --dry-run --group lax-platform`.
  - `upgrade-all.sh --dry-run --group lax-network`.
  - `upgrade-all.sh --dry-run --group lax-observability`.
  - `lax-host-adapter-1panel.sh --dry-run`.
- Final daily report, 2026-07-05 10:01 CST:
  - 15 Docker services checked.
  - 15 Docker services classified as auto-upgrade.
  - 0 Docker service updates available.
  - 0 check failures.
  - Only remaining attention: `1panel=adapter-blocked`.
- Coverage count: `docker ps` returned 15 running containers; `docker ps --filter label=autoupgrade.enable=true` returned 15.
- Health checks:
  - OpenWebUI `http://127.0.0.1:3986/health` returned HTTP 200.
  - OpenWebUI `/app/build/static/custom.css` and `/app/build/static/loader.js` exist in the container.
  - Mihomo config test passed: `/mihomo -t -d /root/.config/mihomo`.
  - Diun version command returned `v4.33.0` and container remains running.
  - Postgres `pg_isready` passed for all three Postgres containers. Residual maintenance note: `sub2api-cpa-poc-postgres` still reports a Postgres collation version warning, but readiness passed.
  - Redis `redis-cli ping` passed for all three Redis containers.
- Rollback cleanup:
  - `docker images --format "{{.Repository}}:{{.Tag}}" | grep ":rollback$"` returned no rollback tags.
- Host checks:
  - Docker Engine `29.6.1`.
  - Docker Compose `v5.3.0`.
  - Root disk after rollout: about 57G free.
  - `ssh lax-via-sjc hostname` returned `lax-hostdzire`.
- Cron checks:
  - `30 9 * * * /usr/local/bin/lax-auto-upgrade-governor.sh >> /var/log/lax-auto-upgrade-governor.log 2>&1`.
  - `11 * * * * /usr/local/bin/lax-capacity-guard.py >> /var/log/lax-capacity-guard.log 2>&1`.
  - Daily report remains separate and read-only.
