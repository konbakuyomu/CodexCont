#!/usr/bin/env bash
set -uo pipefail

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

STATE=/var/lib/lax-auto-upgrade-governor/state.json
ROOT=/var/lib/lax-auto-upgrade-governor/1panel
LOG=/var/log/lax-host-adapter-1panel.log
LATEST_URL=https://resource.1panel.pro/v2/stable/latest

log() { echo "$(date '+%F %T') [1panel] $*" | tee -a "$LOG"; }

record() {
  local status="$1" old="$2" new="$3" note="$4"
  mkdir -p "$(dirname "$STATE")"
  python3 - "$STATE" 1panel "$status" "$old" "$new" "$note" <<'PY'
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

version_num() {
  echo "$1" | sed -E 's/^v//; s/[^0-9.].*$//'
}

version_lt() {
  python3 - "$1" "$2" <<'PY'
import sys
def parts(v):
    return [int(x) for x in v.lstrip("v").split(".") if x.isdigit()]
print("1" if parts(sys.argv[1]) < parts(sys.argv[2]) else "0")
PY
}

current_version() {
  1pctl version 2>/dev/null | awk '/version:/ {print $2; exit}'
}

arch_name() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l) echo armv7 ;;
    *) return 1 ;;
  esac
}

unit_state() {
  systemctl is-active "$1" 2>/dev/null || true
}

restore_unit_state() {
  local unit="$1" was="$2"
  if [ "$was" = "active" ]; then
    systemctl is-active --quiet "$unit" || systemctl start "$unit" >/dev/null 2>&1 || true
  else
    systemctl stop "$unit" >/dev/null 2>&1 || true
  fi
}

latest=$(curl -fsSL "$LATEST_URL" 2>/dev/null || true)
current=$(current_version)
arch=$(arch_name || true)

if [ -z "$current" ]; then
  log "adapter-blocked: could not read current 1Panel version"
  record adapter-blocked "" "" "current version unavailable"
  exit 0
fi

if [ -z "$latest" ] || [ -z "$arch" ]; then
  log "adapter-blocked: latest version or architecture unavailable"
  record adapter-blocked "$current" "$latest" "latest version or architecture unavailable"
  exit 0
fi

pkg="1panel-${latest}-linux-${arch}.tar.gz"
base="https://resource.1panel.pro/v2/stable/${latest}/release"
pkg_url="${base}/${pkg}"
sum_url="${base}/checksums.txt"

if [ "$(version_lt "$current" "$latest")" != "1" ]; then
  log "current: $current latest=$latest"
  record current "$current" "$latest" "1Panel already current"
  exit 0
fi

if ! curl -fsSL "$sum_url" 2>/dev/null | grep -q "  ${pkg}$"; then
  log "adapter-blocked: checksum entry missing for $pkg"
  record adapter-blocked "$current" "$latest" "checksum entry missing"
  exit 0
fi

if [ "$DRY_RUN" = "1" ]; then
  log "dry-run update-available: $current -> $latest package=$pkg"
  record adapter-blocked "$current" "$latest" "dry-run only; official package path available"
  exit 0
fi

core_before=$(unit_state 1panel-core)
agent_before=$(unit_state 1panel-agent)
work="${ROOT}/${latest}"
mkdir -p "$work"
chmod 700 "$ROOT" "$work" 2>/dev/null || true

log "downloading official package $pkg"
if ! curl -fL "$pkg_url" -o "$work/$pkg" >>"$LOG" 2>&1; then
  log "download failed"
  record failed "$current" "$latest" "package download failed"
  exit 1
fi
if ! curl -fsSL "$sum_url" -o "$work/checksums.txt" 2>>"$LOG"; then
  log "checksum download failed"
  record failed "$current" "$latest" "checksum download failed"
  exit 1
fi

if ! (cd "$work" && grep "  ${pkg}$" checksums.txt | sha256sum -c - >>"$LOG" 2>&1); then
  log "checksum verification failed"
  record failed "$current" "$latest" "checksum verification failed"
  exit 1
fi

if ! tar -xzf "$work/$pkg" -C "$work" >>"$LOG" 2>&1; then
  log "extract failed"
  record failed "$current" "$latest" "package extract failed"
  exit 1
fi

upgrade_script=$(find "$work" -maxdepth 3 -type f -name upgrade.sh | head -n 1)
if [ -z "$upgrade_script" ]; then
  if find "$work" -maxdepth 3 -type f -name install.sh | grep -q .; then
    log "adapter-blocked: official public package has install.sh but no upgrade.sh"
    record adapter-blocked "$current" "$latest" "official public package has no upgrade.sh; install.sh is installer-only"
    exit 0
  fi
  log "adapter-blocked: official upgrade.sh not found in package"
  record adapter-blocked "$current" "$latest" "official upgrade.sh not found in package"
  exit 0
fi

log "running official upgrade script; output is in $LOG"
if ! (cd "$(dirname "$upgrade_script")" && timeout 600 bash ./upgrade.sh >>"$LOG" 2>&1); then
  restore_unit_state 1panel-core "$core_before"
  restore_unit_state 1panel-agent "$agent_before"
  log "upgrade script failed"
  record failed "$current" "$latest" "official upgrade script failed"
  exit 1
fi

restore_unit_state 1panel-core "$core_before"
restore_unit_state 1panel-agent "$agent_before"
after=$(current_version)
core_after=$(unit_state 1panel-core)
agent_after=$(unit_state 1panel-agent)

if [ "$(version_lt "$after" "$latest")" = "1" ] || [ "$core_after" != "$core_before" ] || [ "$agent_after" != "$agent_before" ]; then
  log "validation failed after upgrade: version=$after core=$core_after agent=$agent_after"
  record failed "$current" "$after" "post-upgrade validation failed"
  exit 1
fi

log "upgrade ok: $current -> $after; core=$core_after agent=$agent_after"
record upgraded "$current" "$after" "official package upgrade validated; service states preserved"
exit 0
