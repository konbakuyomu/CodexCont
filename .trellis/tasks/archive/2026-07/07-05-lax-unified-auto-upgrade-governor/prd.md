# LAX Unified Auto-Upgrade Governor

## Goal

Port the SJC unattended upgrade governor model to the LAX main business host reachable through `lax-via-sjc`, so LAX can automatically upgrade eligible Docker apps and Docker apt packages with rollback, health gates, and read-only reporting.

## Confirmed Facts

- Target host is `lax-via-sjc` / `lax-hostdzire-wg`, reached through SJC and WireGuard.
- LAX direct public SSH is expected to fail; `ssh lax-via-sjc` is the management path.
- On 2026-07-05 06:47 CST, LAX had about 59G free on `/`.
- LAX had Docker `29.5.3` and Docker Compose plugin `5.1.4`; apt candidates matched the SJC successful upgrade set: Docker `29.6.1`, Compose `5.3.0`, Buildx `0.35.0`, containerd `2.2.5`.
- LAX cron still called `/usr/local/bin/upgrade-all.sh` directly at `09:30`.
- Existing LAX `upgrade-container.sh` still created persistent backups and used broad `docker image prune -af`.
- LAX daily report categorized Docker Engine, Docker Compose, and 1Panel as manual-only.
- 1Panel on LAX is an auxiliary management layer, not production ingress source of truth; live systemd status was `inactive`, and it must not be auto-upgraded in v1.
- OpenWebUI remains notify-only because it has prior custom static UI history.

## Requirements

- Keep Docker app enrollment label-driven through `autoupgrade.enable=true`; do not add a second whitelist file.
- Replace LAX's old persistent-backup upgrade behavior with temporary rollback image tags and health-gated rollback.
- Group eligible LAX app containers with `autoupgrade.group=lax-apps`.
- Use `autoupgrade.strategy=rollback-tag-only` and `autoupgrade.min-disk-gb=20` for eligible LAX apps.
- Preserve current auto-upgrade coverage for `codemerge-static`, `fast-note-sync-service`, `grok2api-jiujiu`, `new-api`, `prompt-manager`, and `sub2api-cpa-poc`.
- Keep DB/Redis, Diun, OpenWebUI, 1Panel/OpenResty, fixed-tag noise, and local build services out of auto-upgrade unless separately approved.
- Add a LAX orchestrator that runs capacity preflight, Docker app upgrades, Docker apt adapter, and post-run daily report.
- Keep the existing LAX capacity guard thresholds and hourly cron.
- Update daily report so Docker Engine and Compose read adapter status from `/var/lib/lax-auto-upgrade-governor/state.json`.
- Treat 1Panel as excluded or manual-only with evidence, not as a v1 host adapter.
- Keep secrets out of logs, task artifacts, Telegram output, and git.
- Do not use recursive deletion, broad prune, volume deletion, database file deletion, or cleanup-by-glob.

## Acceptance Criteria

- [x] Trellis planning artifacts exist: `prd.md`, `design.md`, and `implement.md`.
- [x] LAX current state snapshot is recorded without secrets.
- [x] Installed shell scripts pass `bash -n`.
- [x] Installed daily report passes `python3 -m py_compile`.
- [x] `lax-capacity-guard.py --dry-run --no-telegram` reports healthy enough to proceed.
- [x] `upgrade-all.sh --dry-run --group lax-apps` validates eligible apps without pulling or recreating.
- [x] Each eligible app's `upgrade-container.sh --dry-run <name>` passes or has a concrete safe skip reason.
- [x] LAX cron runs `lax-auto-upgrade-governor.sh` at `09:30` and keeps daily report / capacity guard entries.
- [x] Pending eligible Docker apps are upgraded, confirmed current, or safely skipped.
- [x] Docker apt adapter upgrades Docker Engine / Compose / Buildx / containerd or records `adapter-blocked` with a concrete missing rollback or health contract.
- [x] Final daily report does not list Docker Engine/Compose as manual-only chores.
- [x] 1Panel remains excluded/manual with rationale, and LAX jump login still works.

## Out of Scope

- Auto-upgrading 1Panel.
- Auto-upgrading OpenWebUI.
- Full-system reboot.
- Database major-version upgrades.
- Cleanup, deletion, or Docker prune beyond the controlled image-prune behavior in the successful app-upgrade path.
