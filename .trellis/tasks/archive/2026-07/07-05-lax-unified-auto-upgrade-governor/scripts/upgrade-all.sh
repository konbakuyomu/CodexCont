#!/usr/bin/env bash
set -uo pipefail

SCRIPT=/usr/local/bin/upgrade-container.sh
DRY_RUN=0
GROUP=""

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
    *)
      echo "usage: $0 [--dry-run] [--group <group>]" >&2
      exit 1
      ;;
  esac
done

if [ ! -x "$SCRIPT" ]; then
  echo "$SCRIPT not executable" >&2
  exit 1
fi

echo "$(date '+%F %T') [upgrade-all] starting batch (label-driven group=${GROUP:-all} dry_run=$DRY_RUN)"

docker ps --filter 'label=autoupgrade.enable=true' --format '{{.Names}}' | sort | while read -r NAME; do
  [ -z "$NAME" ] && continue
  if [ -n "$GROUP" ]; then
    ACTUAL=$(docker inspect "$NAME" --format '{{index .Config.Labels "autoupgrade.group"}}' 2>/dev/null || true)
    [ "$ACTUAL" = "$GROUP" ] || continue
  fi
  echo "----- $NAME -----"
  if [ "$DRY_RUN" = "1" ]; then
    "$SCRIPT" --dry-run "$NAME" || true
  else
    "$SCRIPT" "$NAME" || true
  fi
done

echo "$(date '+%F %T') [upgrade-all] batch done"
