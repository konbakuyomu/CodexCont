#!/usr/bin/env bash
set -uo pipefail

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

STATE=/var/lib/lax-auto-upgrade-governor/state.json
LOG=/var/log/lax-host-adapter-docker-apt.log
PACKAGES=(docker-ce docker-ce-cli docker-compose-plugin docker-buildx-plugin containerd.io)
CRITICAL_CONTAINERS=(
  codemerge-static
  fast-note-sync-service
  grok2api-jiujiu
  kuma-openwebui
  new-api
  prompt-manager
  sub2api-cpa-poc
)

log() { echo "$(date '+%F %T') [docker-apt] $*" | tee -a "$LOG"; }

record() {
  local name="$1" status="$2" old="$3" new="$4" note="$5"
  mkdir -p "$(dirname "$STATE")"
  python3 - "$STATE" "$name" "$status" "$old" "$new" "$note" <<'PY'
import json, os, sys, time
path, name, status, old, new, note = sys.argv[1:]
try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    data = {}
adapters = data.setdefault("adapters", {})
adapters[name] = {
    "status": status,
    "old": old,
    "new": new,
    "note": note,
    "updated_at": int(time.time()),
}
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as fh:
    json.dump(data, fh, ensure_ascii=False, indent=2, sort_keys=True)
    fh.write("\n")
os.replace(tmp, path)
PY
}

installed() { dpkg-query -W -f='${Version}' "$1" 2>/dev/null || true; }
candidate() { apt-cache policy "$1" | awk '/Candidate:/ {print $2; exit}'; }
available_version() {
  local pkg="$1" version="$2"
  apt-cache policy "$pkg" | awk -v v="$version" '{for (i = 1; i <= NF; i++) if ($i == v) found = 1} END {exit found ? 0 : 1}'
}

validate_docker() {
  systemctl is-active --quiet docker || return 1
  docker version --format '{{.Server.Version}}' >/dev/null || return 1
  docker compose version --short >/dev/null || return 1
  docker ps --format '{{.Names}}' >/dev/null || return 1
  for name in "${CRITICAL_CONTAINERS[@]}"; do
    docker inspect "$name" >/dev/null 2>&1 || continue
    status=$(docker inspect "$name" --format '{{.State.Status}}' 2>/dev/null || true)
    [ "$status" = "running" ] || return 1
  done
  return 0
}

OLD_LIST=()
NEW_LIST=()
UPGRADE_LIST=()
NEEDS_UPGRADE=0
ROLLBACK_OK=1

for pkg in "${PACKAGES[@]}"; do
  old=$(installed "$pkg")
  new=$(candidate "$pkg")
  [ -n "$old" ] || continue
  [ -n "$new" ] || new="$old"
  OLD_LIST+=("$pkg=$old")
  NEW_LIST+=("$pkg=$new")
  if [ "$old" != "$new" ]; then
    NEEDS_UPGRADE=1
    UPGRADE_LIST+=("$pkg")
    if ! available_version "$pkg" "$old"; then
      ROLLBACK_OK=0
    fi
  fi
done

old_text="${OLD_LIST[*]}"
new_text="${NEW_LIST[*]}"

if [ "$NEEDS_UPGRADE" = "0" ]; then
  log "current: $old_text"
  record docker-engine current "$old_text" "$new_text" "docker packages already current"
  record docker-compose current "$old_text" "$new_text" "docker compose plugin already current"
  exit 0
fi

if [ "$ROLLBACK_OK" != "1" ]; then
  log "adapter-blocked: old package versions are not all available for rollback"
  record docker-engine adapter-blocked "$old_text" "$new_text" "old package versions unavailable for rollback"
  record docker-compose adapter-blocked "$old_text" "$new_text" "old package versions unavailable for rollback"
  exit 0
fi

if [ "$DRY_RUN" = "1" ]; then
  log "dry-run update-available: $old_text -> $new_text"
  record docker-engine adapter-blocked "$old_text" "$new_text" "dry-run only; rollback versions available"
  record docker-compose adapter-blocked "$old_text" "$new_text" "dry-run only; rollback versions available"
  exit 0
fi

log "upgrading: $old_text -> $new_text"
if ! DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade "${UPGRADE_LIST[@]}"; then
  log "apt upgrade failed"
  record docker-engine failed "$old_text" "$new_text" "apt upgrade failed"
  record docker-compose failed "$old_text" "$new_text" "apt upgrade failed"
  exit 1
fi

sleep 5
if validate_docker; then
  after_engine=$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)
  after_compose=$(docker compose version --short 2>/dev/null || true)
  log "upgrade ok: docker=$after_engine compose=$after_compose"
  record docker-engine upgraded "$old_text" "$after_engine" "docker validation passed"
  record docker-compose upgraded "$old_text" "$after_compose" "compose validation passed"
  apt-get clean >/dev/null 2>&1 || true
  exit 0
fi

log "validation failed; attempting apt rollback"
ROLLBACK_ARGS=()
for item in "${OLD_LIST[@]}"; do
  ROLLBACK_ARGS+=("$item")
done
DEBIAN_FRONTEND=noninteractive apt-get install -y --allow-downgrades "${ROLLBACK_ARGS[@]}" || true
systemctl restart docker || true
sleep 5
if validate_docker; then
  log "rolled back successfully"
  record docker-engine rolled-back "$old_text" "$new_text" "validation failed after upgrade; rollback succeeded"
  record docker-compose rolled-back "$old_text" "$new_text" "validation failed after upgrade; rollback succeeded"
  exit 1
fi

record docker-engine failed "$old_text" "$new_text" "validation failed and rollback did not validate"
record docker-compose failed "$old_text" "$new_text" "validation failed and rollback did not validate"
exit 1
