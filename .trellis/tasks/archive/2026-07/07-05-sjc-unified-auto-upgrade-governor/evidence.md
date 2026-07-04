# Evidence

## 2026-07-05 SJC rollout

- Created planning artifacts: `prd.md`, `design.md`, `implement.md`.
- Freed SJC root from about 968M to 1.6G by deleting only explicit old backup binary/plugin files under `/opt/codex-stacks/backups`.
- Installed and validated:
  - `/usr/local/bin/upgrade-container.sh`
  - `/usr/local/bin/upgrade-all.sh`
  - `/usr/local/bin/sjc-auto-upgrade-governor.sh`
  - `/usr/local/bin/sjc-host-adapter-docker-apt.sh`
  - `/usr/local/bin/sjc-host-adapter-x-ui.sh`
- Updated daily report to read host adapter state from `/var/lib/sjc-auto-upgrade-governor/state.json`.
- Updated cron:
  - `30 9 * * * /usr/local/bin/sjc-auto-upgrade-governor.sh >> /var/log/sjc-auto-upgrade-governor.log 2>&1`
  - Existing `10:15` daily report and hourly capacity guard remain.
- Upgraded Docker services:
  - `cpamp`: `e2ae75e14b60` -> `7e832de6f4d7`, CPAMP image version `1.10.2`, container healthy.
  - `frontier-sub-store`: `ca1ab42a5816` -> `581291e6289f`, container running.
- Updated labels on live compose-managed services:
  - CPA stack services now use `autoupgrade.group=cpa-stack`, `autoupgrade.strategy=rollback-tag-only`, `autoupgrade.min-disk-gb=0.75`.
  - `shared-hy2-u01` now uses `autoupgrade.group=shared-hy2`, `autoupgrade.strategy=rollback-tag-only`, `autoupgrade.min-disk-gb=0.75`.
- Upgraded host adapters:
  - Docker Engine: `29.5.3` -> `29.6.1`.
  - Docker Compose plugin: `5.1.4` -> `5.3.0`.
  - Docker Buildx plugin: `0.34.1` -> `0.35.0`.
  - containerd.io: `2.2.4` -> `2.2.5`.
  - x-ui: `3.3.1` -> `3.4.2`.

## Validation

- `bash -n` passed for all installed shell scripts.
- `python3 -m py_compile /usr/local/bin/daily-update-report.py` passed.
- `docker version --format '{{.Server.Version}}'` reported `29.6.1`.
- `docker compose version --short` reported `5.3.0`.
- `systemctl is-active docker` and `systemctl is-active x-ui` reported `active`.
- `/usr/local/x-ui/x-ui -v` reported `3.4.2`.
- CPA health returned `{"status":"ok"}`.
- CPAMP health returned `{"ok":true,"service":"cpa-manager-plus"}`.
- Public/admin smoke:
  - `https://cpa.konbakuyomu.us/v1/models` returned `401`, expected without auth.
  - `https://cpa-admin.konbakuyomu.us/` returned `302`.
  - `https://cpa-usage.konbakuyomu.us/` returned `200`.
- Daily report dry-run after rollout:
  - Conclusion: currently no manual action needed.
  - Docker updates: `0`.
  - Failures: `0`.
  - Adapter states: `docker-engine=upgraded`, `docker-compose=upgraded`, `x-ui=upgraded`.
  - Space: `ok`, about `1.58G` free.

## Notes

- Pre-existing `tobyxdd/hysteria:shared-hy2-rollback-u01` and `shared-hy2-rollback-u03` tags remain. They predate this rollout, reclaim `0B`, and are tied to shared-hy2 history/orphan containers, so they were not removed in this task.
