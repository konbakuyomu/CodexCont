# Design

## Architecture

The LAX governor remains cron-driven but now treats every covered item as an adapter-owned target:

```text
09:30 cron
  -> lax-auto-upgrade-governor.sh
     -> capacity dry-run gate
     -> Docker/Compose apt adapter
     -> upgrade-all --group lax-data
     -> upgrade-all --group lax-network
     -> upgrade-all --group lax-apps
     -> upgrade-all --group lax-platform
     -> upgrade-all --group lax-observability
     -> 1Panel host adapter
     -> read-only daily report
```

`upgrade-all.sh` remains label-driven and delegates to `upgrade-container.sh`.

## Docker Labels

- `autoupgrade.enable=true`
- `autoupgrade.group=<lax-apps|lax-data|lax-platform|lax-network|lax-observability>`
- `autoupgrade.adapter=<app|postgres-same-major|redis-same-major|openwebui-assets|mihomo-config|running-only>`
- `autoupgrade.strategy=rollback-tag-only`
- `autoupgrade.min-disk-gb=20`
- optional `autoupgrade.health-url`
- optional `autoupgrade.healthcheck-sec`

## Adapter Behavior

- `app`: pull desired compose image, rollback-tag old image ID, recreate only the service, then health URL or Docker health/running gate.
- `postgres-same-major`: require old and desired image major match, run `pg_isready`, create a root-only `pg_dumpall` gzip backup before real update, then recreate and validate.
- `redis-same-major`: require old and desired image major match, validate `redis-cli ping`, request background persistence before real update, then recreate and validate.
- `openwebui-assets`: validate host static assets, recreate OpenWebUI app, then validate `/health` and the mounted `custom.css` / `loader.js` files.
- `mihomo-config`: recreate Mihomo, then run `/mihomo -t -d /root/.config/mihomo`.
- `running-only`: recreate and require the container to be running.

All container adapters write state entries under `/var/lib/lax-auto-upgrade-governor/state.json`.

## 1Panel

The host adapter discovers latest from `https://resource.1panel.pro/v2/stable/latest`, downloads the matching official tarball and checksums, verifies SHA-256, and runs the bundled `upgrade.sh` only if the verified package provides one. If the verified package has only `install.sh`, the adapter records `adapter-blocked` because `install.sh` is installer-only and must not be treated as a program upgrade.

`1pctl update` is explicitly not used as a program upgrade because it is for panel information such as username, password, and port.

## Reporting

`daily-update-report.py` remains read-only. Docker containers become `auto-upgrade` when labeled. Docker report status falls back to governor state when remote registry digest probing is unavailable, while still surfacing `adapter-blocked`, `rolled-back`, and `failed`. `1panel` is a `host-adapter` result, not manual-only. If an adapter cannot complete, the report shows adapter attention instead of hiding the gap.
