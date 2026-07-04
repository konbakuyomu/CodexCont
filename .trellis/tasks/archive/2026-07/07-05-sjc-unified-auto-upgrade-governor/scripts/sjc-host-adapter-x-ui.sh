#!/usr/bin/env bash
set -uo pipefail

DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

STATE=/var/lib/sjc-auto-upgrade-governor/state.json
LOG=/var/log/sjc-host-adapter-x-ui.log
REPO=MHSanaei/3x-ui
INSTALL_DIR=/usr/local/x-ui
TMP_DIR=/tmp/sjc-x-ui-upgrade
ROLLBACK_DIR=/tmp/sjc-x-ui-rollback

log() { echo "$(date '+%F %T') [x-ui] $*" | tee -a "$LOG"; }

record() {
  local status="$1" old="$2" new="$3" note="$4"
  mkdir -p "$(dirname "$STATE")"
  python3 - "$STATE" "$status" "$old" "$new" "$note" <<'PY'
import json, sys, time
path, status, old, new, note = sys.argv[1:]
try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    data = {}
data.setdefault("adapters", {})["x-ui"] = {
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
import os
os.replace(tmp, path)
PY
}

current_version() {
  /usr/local/x-ui/x-ui -v 2>&1 | grep -Eo '[0-9]+(\.[0-9]+)+' | head -1 || true
}

latest_version() {
  python3 - "$REPO" <<'PY'
import json, sys, urllib.request
repo = sys.argv[1]
req = urllib.request.Request(f"https://api.github.com/repos/{repo}/releases/latest", headers={"User-Agent": "sjc-x-ui-adapter"})
with urllib.request.urlopen(req, timeout=20) as resp:
    data = json.load(resp)
print((data.get("tag_name") or "").lstrip("v"))
PY
}

validate_xui() {
  systemctl is-active --quiet x-ui || return 1
  new_ver=$(current_version)
  [ -n "$new_ver" ] || return 1
  return 0
}

cleanup_tmp() {
  rm -f "$TMP_DIR/x-ui-linux-amd64.tar.gz" 2>/dev/null || true
  for file in x-ui x-ui.sh x-ui.service.arch x-ui.service.debian x-ui.service.rhel; do
    rm -f "$TMP_DIR/x-ui/$file" 2>/dev/null || true
  done
  for file in LICENSE README.md geoip.dat geoip_IR.dat geoip_RU.dat geosite.dat geosite_IR.dat geosite_RU.dat mtg-linux-amd64 xray-linux-amd64; do
    rm -f "$TMP_DIR/x-ui/bin/$file" 2>/dev/null || true
  done
  rmdir "$TMP_DIR/x-ui/bin" 2>/dev/null || true
  rmdir "$TMP_DIR/x-ui" 2>/dev/null || true
  rmdir "$TMP_DIR" 2>/dev/null || true
}

cleanup_rollback() {
  for file in x-ui x-ui.sh x-ui.service.arch x-ui.service.debian x-ui.service.rhel; do
    rm -f "$ROLLBACK_DIR/$file" 2>/dev/null || true
  done
  for file in LICENSE README.md geoip.dat geoip_IR.dat geoip_RU.dat geosite.dat geosite_IR.dat geosite_RU.dat mtg-linux-amd64 xray-linux-amd64; do
    rm -f "$ROLLBACK_DIR/bin/$file" 2>/dev/null || true
  done
  rmdir "$ROLLBACK_DIR/bin" 2>/dev/null || true
  rmdir "$ROLLBACK_DIR" 2>/dev/null || true
}

OLD=$(current_version)
LATEST=$(latest_version 2>/dev/null || true)

if [ -z "$OLD" ] || [ -z "$LATEST" ]; then
  log "adapter-blocked: cannot determine current or latest version old=$OLD latest=$LATEST"
  record adapter-blocked "$OLD" "$LATEST" "cannot determine current or latest version"
  exit 0
fi

if [ "$OLD" = "$LATEST" ]; then
  log "current: $OLD"
  record current "$OLD" "$LATEST" "x-ui already current"
  exit 0
fi

URL="https://github.com/${REPO}/releases/download/v${LATEST}/x-ui-linux-amd64.tar.gz"

if [ "$DRY_RUN" = "1" ]; then
  log "dry-run update-available: $OLD -> $LATEST"
  record adapter-blocked "$OLD" "$LATEST" "dry-run only; versioned release asset available"
  exit 0
fi

mkdir -p "$TMP_DIR" "$ROLLBACK_DIR/bin"
log "downloading $URL"
if ! curl -fL --max-time 120 "$URL" -o "$TMP_DIR/x-ui-linux-amd64.tar.gz"; then
  log "download failed"
  record failed "$OLD" "$LATEST" "download failed"
  cleanup_tmp
  exit 1
fi

if ! tar -xzf "$TMP_DIR/x-ui-linux-amd64.tar.gz" -C "$TMP_DIR"; then
  log "extract failed"
  record failed "$OLD" "$LATEST" "extract failed"
  cleanup_tmp
  exit 1
fi

for file in x-ui x-ui.sh x-ui.service.arch x-ui.service.debian x-ui.service.rhel; do
  [ -f "$INSTALL_DIR/$file" ] && cp -a "$INSTALL_DIR/$file" "$ROLLBACK_DIR/$file"
done
for file in LICENSE README.md geoip.dat geoip_IR.dat geoip_RU.dat geosite.dat geosite_IR.dat geosite_RU.dat mtg-linux-amd64 xray-linux-amd64; do
  [ -f "$INSTALL_DIR/bin/$file" ] && cp -a "$INSTALL_DIR/bin/$file" "$ROLLBACK_DIR/bin/$file"
done

systemctl stop x-ui || true

for file in x-ui x-ui.sh x-ui.service.arch x-ui.service.debian x-ui.service.rhel; do
  cp -a "$TMP_DIR/x-ui/$file" "$INSTALL_DIR/$file"
done
for file in LICENSE README.md geoip.dat geoip_IR.dat geoip_RU.dat geosite.dat geosite_IR.dat geosite_RU.dat mtg-linux-amd64 xray-linux-amd64; do
  cp -a "$TMP_DIR/x-ui/bin/$file" "$INSTALL_DIR/bin/$file"
done
chmod +x "$INSTALL_DIR/x-ui" "$INSTALL_DIR/x-ui.sh" "$INSTALL_DIR/bin/mtg-linux-amd64" "$INSTALL_DIR/bin/xray-linux-amd64"

systemctl start x-ui || true
sleep 5

if validate_xui && [ "$(current_version)" = "$LATEST" ]; then
  log "upgrade ok: $OLD -> $LATEST"
  record upgraded "$OLD" "$LATEST" "x-ui validation passed"
  cleanup_rollback
  cleanup_tmp
  exit 0
fi

log "validation failed; rolling back"
systemctl stop x-ui || true
for file in x-ui x-ui.sh x-ui.service.arch x-ui.service.debian x-ui.service.rhel; do
  [ -f "$ROLLBACK_DIR/$file" ] && cp -a "$ROLLBACK_DIR/$file" "$INSTALL_DIR/$file"
done
for file in LICENSE README.md geoip.dat geoip_IR.dat geoip_RU.dat geosite.dat geosite_IR.dat geosite_RU.dat mtg-linux-amd64 xray-linux-amd64; do
  [ -f "$ROLLBACK_DIR/bin/$file" ] && cp -a "$ROLLBACK_DIR/bin/$file" "$INSTALL_DIR/bin/$file"
done
systemctl start x-ui || true
sleep 5

if validate_xui && [ "$(current_version)" = "$OLD" ]; then
  log "rolled back successfully"
  record rolled-back "$OLD" "$LATEST" "validation failed; rollback succeeded"
  cleanup_rollback
  cleanup_tmp
  exit 1
fi

record failed "$OLD" "$LATEST" "validation failed and rollback did not validate"
cleanup_tmp
exit 1
