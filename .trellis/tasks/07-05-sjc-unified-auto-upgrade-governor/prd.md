# SJC Unified Auto-Upgrade Governor

## Goal

Make SJC's upgrade workflow genuinely unattended for components that can be upgraded safely, while respecting the small 10G root disk and preserving rollback for failed upgrades.

## Confirmed Facts

- As of 2026-07-05 05:13 CST, SJC had about 974M free on `/`, below the existing `autoupgrade.min-disk-gb=1.5` gate.
- `cpamp` and `frontier-sub-store` already had newer Docker images available, but `/usr/local/bin/upgrade-container.sh --dry-run cpamp` skipped because disk was below the configured gate.
- CPAMP upstream latest is `v1.10.2`; SJC's running CPAMP image metadata still reports `org.opencontainers.image.version=1.10.0`.
- Existing SJC automation uses `diun.enable=true`, `autoupgrade.enable=true`, `autoupgrade.group=...`, `/usr/local/bin/upgrade-all.sh`, `/usr/local/bin/upgrade-container.sh`, and `/usr/local/bin/daily-update-report.py`.
- Docker Engine, Docker Compose plugin, and x-ui are detected by daily report but currently categorized as manual-only.

## Requirements

- Keep Docker service enrollment label-driven; do not introduce a second whitelist file.
- Replace thick persistent backups with a low-disk rollback model: keep only the old image or binary needed during the upgrade attempt, then delete the rollback artifact after success.
- Upgrade Docker app containers serially with preflight, pull, digest comparison, recreate, health gate, success cleanup, and rollback on failure.
- Add a SJC orchestrator that runs capacity preflight, Docker app upgrades, host component adapters, and post-report in one daily maintenance flow.
- Add v1 host adapters for Docker Engine, Docker Compose plugin, and x-ui.
- Treat host components without a verified preflight, health gate, and rollback path as `adapter-blocked`, not as manual chores.
- Allow short service interruptions from single-container recreates, Docker daemon restart, or x-ui restart. Do not automatically reboot the whole server.
- Keep secrets out of logs, task artifacts, Telegram messages, shell output, and git.
- Follow project deletion rules: no broad recursive deletion or broad prune; if space must be released, delete only explicit confirmed files one at a time.

## Acceptance Criteria

- [x] Trellis planning artifacts exist for this task: `prd.md`, `design.md`, and `implement.md`.
- [x] SJC root free space is restored to at least 1.5G before real upgrades begin, using only explicit safe cleanup.
- [x] `upgrade-container.sh --dry-run cpamp` and `--dry-run frontier-sub-store` pass before real upgrades.
- [x] `cpamp` upgrades to the latest image, remains healthy, and CPAMP reports the new dashboard version.
- [x] `frontier-sub-store` upgrades or is confirmed current by digest after the new script path.
- [x] Docker app upgrade success removes temporary rollback tags and leaves no persistent thick backup created by the upgrade script.
- [x] Host adapters dry-run cleanly and either upgrade Docker Engine / Compose / x-ui or report `adapter-blocked` with a concrete missing contract.
- [x] Daily report shows adapter statuses (`upgraded`, `current`, `rolled-back`, `adapter-blocked`, or `failed`) instead of hard-coded manual-only wording for those host components.
- [x] Post-upgrade smoke checks pass for CPA, CPAMP, Caddy edge/admin proxy, frontier-sub-store, and key public/admin endpoints.

## Out of Scope

- Automatic full-system reboot.
- Database major-version upgrades.
- Recursive deletion of directories, Docker volumes, Redis AOF, Postgres data, or live compose state.
- Replacing Diun itself through this automation path.
