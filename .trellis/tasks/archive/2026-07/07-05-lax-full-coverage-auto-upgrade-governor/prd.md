# LAX Full Coverage Auto-Upgrade Governor

## Goal

Correct the LAX auto-upgrade policy from partial selective automation to full coverage for the currently running LAX services: 15 Docker containers, Docker Engine / Compose, and 1Panel. Every covered item must be either safely auto-upgraded by a concrete adapter or reported as a real adapter failure/blocker, not left as permanent manual or notify-only work.

## Requirements

- Target host is `lax-via-sjc` / `lax-hostdzire`, reached through SJC + WireGuard.
- Coverage is current running services only: no stopped compose projects, backup compose files, whole-system apt, or kernel updates.
- Preserve the existing 09:30 daily governor cron and hourly capacity guard.
- Keep all updates serial and health-gated.
- Continue label-driven Docker enrollment through `autoupgrade.enable=true`; add `autoupgrade.group` and `autoupgrade.adapter` to select safe behavior.
- Keep the existing six app services in `lax-apps`.
- Add `lax-data` coverage for Postgres and Redis containers, limited to same-major updates.
- Add `lax-platform` coverage for OpenWebUI with static asset preservation.
- Add `lax-network` coverage for Mihomo with config validation.
- Add `lax-observability` coverage for Diun.
- Add a 1Panel host adapter using the official v2 offline package resource path; do not treat `1pctl update` as a program upgrade.
- Preserve 1Panel's real pre-upgrade service state. Live evidence corrected the earlier legacy-unit assumption: `1panel-core` and `1panel-agent` are active even though old `systemctl is-active 1panel` is inactive.
- Daily report must be read-only and must not show persistent notify-only/manual chores for the covered items after a successful run.
- Do not print or store secrets in task artifacts.
- Do not recursively delete, remove Docker volumes, delete database files, run `docker system prune`, run `docker image prune -af`, or perform broad cleanup.

## Acceptance Criteria

- [x] Trellis artifacts exist: `prd.md`, `design.md`, `implement.md`, `evidence.md`, `implement.jsonl`, and `check.jsonl`.
- [x] LAX full-coverage spec is updated.
- [x] Runtime scripts are installed with timestamped backups.
- [x] All 15 running Docker containers have `autoupgrade.enable=true`.
- [x] Covered groups exist: `lax-apps`, `lax-data`, `lax-platform`, `lax-network`, `lax-observability`.
- [x] Docker/Compose adapter state is current or upgraded.
- [x] 1Panel adapter uses the official package route, upgrades to latest or records a concrete blocker, and preserves pre-upgrade service state.
- [x] Dry-runs pass for capacity guard, governor, all groups, and 1Panel adapter.
- [x] Real rollout runs serially and leaves no stale `:rollback` tags.
- [x] OpenWebUI `/health` returns 200 and static assets remain mounted/present.
- [x] Postgres and Redis containers are running and pass their health commands.
- [x] Mihomo config test passes with `/mihomo -t -d /root/.config/mihomo`.
- [x] Diun remains running.
- [x] Final daily report does not list `kuma-openwebui`, databases, Redis, Diun, Mihomo, or 1Panel as persistent notify-only/manual chores.
- [x] `ssh lax-via-sjc hostname` returns `lax-hostdzire`.

## Notes

- Official 1Panel sources checked during planning:
  - `https://1panel.pro/docs/v2/installation/cli/`
  - `https://1panel.cn/docs/v2/installation/online_upgrade/`
  - `https://1panel.cn/docs/v2/installation/package_installation/`
  - `https://resource.1panel.pro/v2/stable/latest`
