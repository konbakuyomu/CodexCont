#!/usr/bin/env bash
set -uo pipefail

LOG=/var/log/lax-auto-upgrade-governor.log
STATE_DIR=/var/lib/lax-auto-upgrade-governor
DRY_RUN=0
RUN_HOST=1
GROUP=""
DEFAULT_GROUPS=(lax-data lax-network lax-apps lax-platform lax-observability)

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --group)
      GROUP="${2:-}"
      shift 2
      ;;
    --docker-only)
      RUN_HOST=0
      shift
      ;;
    *)
      echo "usage: $0 [--dry-run] [--group <group>] [--docker-only]" >&2
      exit 1
      ;;
  esac
done

mkdir -p "$STATE_DIR"

run_step() {
  echo "$(date '+%F %T') [governor] $*" | tee -a "$LOG"
}

run_upgrade_group() {
  local group="$1"
  if [ "$DRY_RUN" = "1" ]; then
    /usr/local/bin/upgrade-all.sh --dry-run --group "$group" | tee -a "$LOG"
  else
    /usr/local/bin/upgrade-all.sh --group "$group" | tee -a "$LOG"
  fi
}

run_step "start dry_run=$DRY_RUN group=${GROUP:-default-full-coverage} run_host=$RUN_HOST"
df -h / | tee -a "$LOG"

if [ -x /usr/local/bin/lax-capacity-guard.py ]; then
  /usr/local/bin/lax-capacity-guard.py --dry-run --no-telegram | tee -a "$LOG" || true
fi

if [ "$RUN_HOST" = "1" ] && [ -x /usr/local/bin/lax-host-adapter-docker-apt.sh ]; then
  if [ "$DRY_RUN" = "1" ]; then
    /usr/local/bin/lax-host-adapter-docker-apt.sh --dry-run | tee -a "$LOG" || true
  else
    /usr/local/bin/lax-host-adapter-docker-apt.sh | tee -a "$LOG" || true
  fi
fi

if [ -n "$GROUP" ]; then
  run_upgrade_group "$GROUP"
else
  for group in "${DEFAULT_GROUPS[@]}"; do
    run_upgrade_group "$group"
  done
fi

if [ "$RUN_HOST" = "1" ] && [ -x /usr/local/bin/lax-host-adapter-1panel.sh ]; then
  if [ "$DRY_RUN" = "1" ]; then
    /usr/local/bin/lax-host-adapter-1panel.sh --dry-run | tee -a "$LOG" || true
  else
    /usr/local/bin/lax-host-adapter-1panel.sh | tee -a "$LOG" || true
  fi
fi

if [ -x /usr/local/bin/daily-update-report.py ]; then
  /usr/local/bin/daily-update-report.py --host-label LAX --dry-run --no-telegram | tee -a "$LOG" || true
fi

run_step "done"
