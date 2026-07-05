#!/usr/bin/env bash
set -uo pipefail

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=1
  shift
fi

NAME="${1:-}"
[ -n "$NAME" ] || { echo "usage: $0 [--dry-run] <container_name>" >&2; exit 1; }

STATE=/var/lib/lax-auto-upgrade-governor/state.json
BACKUP_ROOT=/var/lib/lax-auto-upgrade-governor/backups
LIB=/etc/auto-upgrade.d/lib/notify.sh
LOCK=/tmp/upgrade-${NAME//[^a-zA-Z0-9_-]/_}.lock

[ -f "$LIB" ] && source "$LIB" || tg() { :; }

log() { echo "$(date '+%F %T') [$NAME] $*"; }

record() {
  local status="$1" old="$2" new="$3" note="$4"
  mkdir -p "$(dirname "$STATE")"
  python3 - "$STATE" "$NAME" "$status" "$old" "$new" "$note" <<'PY'
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

compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    docker compose "$@"
  elif command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
  else
    log "no docker compose or docker-compose command found"
    return 127
  fi
}

min_disk_bytes() {
  python3 - "$1" <<'PY'
import sys
try:
    print(int(float(sys.argv[1]) * 1024 * 1024 * 1024))
except Exception:
    print(20 * 1024 * 1024 * 1024)
PY
}

label() {
  docker inspect "$NAME" --format "{{index .Config.Labels \"$1\"}}" 2>/dev/null || true
}

image_base() {
  local image="$1"
  if [[ "$image" == *@sha256:* ]] || [[ "$image" == sha256:* ]]; then
    echo ""
    return 0
  fi
  if [[ "$image" == *:* ]] && [[ "${image##*/}" == *:* ]]; then
    echo "${image%:*}"
    return 0
  fi
  echo "$image"
}

image_major() {
  local image="$1"
  local tag
  tag="${image##*:}"
  if [ "$tag" = "$image" ]; then
    echo ""
    return 0
  fi
  echo "$tag" | sed -E 's/^v?([0-9]+).*/\1/'
}

container_state_ok() {
  local status health
  status=$(docker inspect "$NAME" --format '{{.State.Status}}' 2>/dev/null || echo "")
  health=$(docker inspect "$NAME" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo "")
  [ "$status" = "running" ] || return 1
  [ "$health" = "unhealthy" ] && return 1
  [ "$health" = "starting" ] && return 1
  return 0
}

http_health_ok() {
  local health_url="$1"
  [ -n "$health_url" ] || return 1
  local resp
  resp=$(curl -fsS --max-time 12 "$health_url" 2>/dev/null || true)
  [ -n "$resp" ] || return 1
  echo "$resp" | grep -qE '"status":"(ok|healthy)"|"healthy":true' && return 0
  return 0
}

postgres_ok() {
  docker exec "$NAME" sh -lc 'pg_isready -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-postgres}" >/dev/null' >/dev/null 2>&1
}

postgres_backup() {
  local ts out logf
  ts=$(date '+%Y%m%d-%H%M%S')
  mkdir -p "$BACKUP_ROOT/postgres"
  chmod 700 "$BACKUP_ROOT" "$BACKUP_ROOT/postgres" 2>/dev/null || true
  out="$BACKUP_ROOT/postgres/${NAME}-${ts}.sql.gz"
  logf="$BACKUP_ROOT/postgres/${NAME}-${ts}.log"
  umask 077
  if docker exec "$NAME" sh -lc 'pg_dumpall -U "${POSTGRES_USER:-postgres}"' 2>"$logf" | gzip -c > "$out"; then
    log "postgres logical backup created: $out"
    return 0
  fi
  log "postgres logical backup failed; see root-only log $logf"
  return 1
}

redis_ok() {
  docker exec "$NAME" sh -lc 'redis-cli ping 2>/dev/null | grep -q PONG || redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null | grep -q PONG' >/dev/null 2>&1
}

redis_persist() {
  docker exec "$NAME" sh -lc 'redis-cli ping >/dev/null 2>&1 || redis-cli -a "$REDIS_PASSWORD" ping >/dev/null 2>&1' >/dev/null 2>&1 || return 1
  docker exec "$NAME" sh -lc 'redis-cli BGSAVE >/dev/null 2>&1 || redis-cli -a "$REDIS_PASSWORD" BGSAVE >/dev/null 2>&1 || true' >/dev/null 2>&1
  docker exec "$NAME" sh -lc 'redis-cli INFO persistence >/dev/null 2>&1 || redis-cli -a "$REDIS_PASSWORD" INFO persistence >/dev/null 2>&1' >/dev/null 2>&1
}

openwebui_assets_ok() {
  [ -f "$COMPOSE_DIR/openwebui-theme-v1/custom.css" ] || return 1
  [ -f "$COMPOSE_DIR/openwebui-theme-v1/loader.js" ] || return 1
  docker exec "$NAME" test -f /app/build/static/custom.css >/dev/null 2>&1 || return 1
  docker exec "$NAME" test -f /app/build/static/loader.js >/dev/null 2>&1 || return 1
  return 0
}

mihomo_ok() {
  docker exec "$NAME" /mihomo -t -d /root/.config/mihomo >/dev/null 2>&1
}

adapter_health_ok() {
  case "$ADAPTER" in
    postgres-same-major) postgres_ok ;;
    redis-same-major) redis_ok ;;
    openwebui-assets) http_health_ok "$HEALTH_URL" && openwebui_assets_ok ;;
    mihomo-config) container_state_ok && mihomo_ok ;;
    running-only) container_state_ok ;;
    *) if [ -n "$HEALTH_URL" ]; then http_health_ok "$HEALTH_URL"; else container_state_ok; fi ;;
  esac
}

adapter_preflight() {
  case "$ADAPTER" in
    postgres-same-major)
      postgres_ok || { log "postgres preflight failed"; return 1; }
      ;;
    redis-same-major)
      redis_ok || { log "redis preflight failed"; return 1; }
      ;;
    openwebui-assets)
      [ -n "$HEALTH_URL" ] || { log "openwebui health-url missing"; return 1; }
      http_health_ok "$HEALTH_URL" || { log "openwebui health preflight failed"; return 1; }
      openwebui_assets_ok || { log "openwebui static assets preflight failed"; return 1; }
      ;;
    mihomo-config)
      mihomo_ok || { log "mihomo config preflight failed"; return 1; }
      ;;
    running-only)
      container_state_ok || { log "running preflight failed"; return 1; }
      ;;
    *)
      if [ -n "$HEALTH_URL" ]; then
        http_health_ok "$HEALTH_URL" || { log "http health preflight failed"; return 1; }
      else
        container_state_ok || { log "container preflight failed"; return 1; }
      fi
      ;;
  esac
}

adapter_before_real_update() {
  case "$ADAPTER" in
    postgres-same-major) postgres_backup ;;
    redis-same-major) redis_persist ;;
    *) return 0 ;;
  esac
}

desired_compose_image() {
  compose_cmd -f "$COMPOSE_FILE" config --format json 2>/tmp/compose-config-${NAME}.err \
    | python3 -c 'import json, sys; service = sys.argv[1]
try:
    data = json.load(sys.stdin)
    print(data["services"][service].get("image", ""))
except Exception:
    print("")' "$SERVICE"
}

if ! docker inspect "$NAME" >/dev/null 2>&1; then
  log "container does not exist; skipped"
  exit 0
fi

ENABLED=$(label "autoupgrade.enable")
if [ "$ENABLED" != "true" ]; then
  log "not labeled with autoupgrade.enable=true; skipped"
  exit 0
fi

COMPOSE_DIR=$(label "com.docker.compose.project.working_dir")
SERVICE=$(label "com.docker.compose.service")
if [ -z "$COMPOSE_DIR" ] || [ -z "$SERVICE" ]; then
  log "missing docker compose labels"
  record adapter-blocked "" "" "missing docker compose labels"
  tg "WARN ${NAME} upgrade skipped: missing docker compose labels"
  exit 1
fi

HEALTH_URL=$(label "autoupgrade.health-url")
BACKUP_STRATEGY=$(label "autoupgrade.strategy")
MIN_DISK_GB=$(label "autoupgrade.min-disk-gb")
HEALTHCHECK_TIMEOUT_SEC=$(label "autoupgrade.healthcheck-sec")
ADAPTER=$(label "autoupgrade.adapter")

: "${BACKUP_STRATEGY:=rollback-tag-only}"
: "${MIN_DISK_GB:=20}"
: "${HEALTHCHECK_TIMEOUT_SEC:=30}"
: "${ADAPTER:=app}"

if [ -n "${AUTOGOVERNOR_MIN_DISK_GB_OVERRIDE:-}" ]; then
  MIN_DISK_GB="$AUTOGOVERNOR_MIN_DISK_GB_OVERRIDE"
fi

log "config: dir=$COMPOSE_DIR svc=$SERVICE adapter=$ADAPTER strategy=$BACKUP_STRATEGY disk_min=${MIN_DISK_GB}GB hc_sec=${HEALTHCHECK_TIMEOUT_SEC}s dry_run=$DRY_RUN"

exec 200>"$LOCK"
flock -n 200 || { log "another run in progress; skipped"; exit 0; }

AVAIL_BYTES=$(df -B1 / --output=avail | tail -1 | tr -dc '0-9')
MIN_BYTES=$(min_disk_bytes "$MIN_DISK_GB")
log "disk avail_bytes=${AVAIL_BYTES} min_bytes=${MIN_BYTES}"
if [ "${AVAIL_BYTES:-0}" -lt "${MIN_BYTES:-0}" ]; then
  log "disk low; skipped"
  record adapter-blocked "" "" "disk below ${MIN_DISK_GB}GB"
  tg "WARN ${NAME} upgrade skipped: low disk, threshold ${MIN_DISK_GB}GB"
  exit 0
fi

cd "$COMPOSE_DIR" || exit 1
COMPOSE_FILE=""
for candidate in docker-compose.yml docker-compose.yaml compose.yml compose.yaml; do
  if [ -f "$candidate" ]; then
    COMPOSE_FILE="$candidate"
    break
  fi
done
[ -n "$COMPOSE_FILE" ] || { log "no compose file found"; record adapter-blocked "" "" "no compose file found"; exit 1; }

if ! compose_cmd -f "$COMPOSE_FILE" config >/tmp/compose-config-${NAME}.log 2>&1; then
  log "compose config failed: $(tail -3 /tmp/compose-config-${NAME}.log)"
  record adapter-blocked "" "" "compose config failed"
  exit 1
fi

RUNNING_IMAGE=$(docker inspect "$NAME" --format '{{.Config.Image}}')
DESIRED_IMAGE=$(desired_compose_image)
[ -n "$DESIRED_IMAGE" ] || DESIRED_IMAGE="$RUNNING_IMAGE"

if [ "$ADAPTER" = "postgres-same-major" ] || [ "$ADAPTER" = "redis-same-major" ]; then
  OLD_MAJOR=$(image_major "$RUNNING_IMAGE")
  NEW_MAJOR=$(image_major "$DESIRED_IMAGE")
  if [ -z "$OLD_MAJOR" ] || [ -z "$NEW_MAJOR" ] || [ "$OLD_MAJOR" != "$NEW_MAJOR" ]; then
    log "major mismatch old=$RUNNING_IMAGE desired=$DESIRED_IMAGE"
    record adapter-blocked "$RUNNING_IMAGE" "$DESIRED_IMAGE" "same-major guard blocked"
    exit 0
  fi
fi

if [ "$DRY_RUN" = "1" ]; then
  if adapter_preflight; then
    log "dry-run ok desired_image=$DESIRED_IMAGE"
    exit 0
  fi
  log "dry-run adapter preflight failed"
  exit 1
fi

BASE=$(image_base "$DESIRED_IMAGE")
if [ -z "$BASE" ]; then
  log "image reference is digest-only; cannot create rollback tag"
  record adapter-blocked "$RUNNING_IMAGE" "$DESIRED_IMAGE" "digest-only image cannot use rollback tag"
  tg "WARN ${NAME} upgrade skipped: digest-only image cannot use rollback tag"
  exit 1
fi
ROLLBACK_REF="${BASE}:rollback"

OLD_ID=$(docker inspect "$NAME" --format '{{.Image}}' 2>/dev/null || echo "")
log "running_image=$RUNNING_IMAGE desired_image=$DESIRED_IMAGE old_digest=${OLD_ID:7:12}"

if ! adapter_preflight; then
  record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "preflight failed"
  exit 1
fi

if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
  log "removing stale rollback tag: $ROLLBACK_REF"
  docker rmi "$ROLLBACK_REF" >/dev/null 2>&1 || true
fi

log "pulling..."
if ! compose_cmd -f "$COMPOSE_FILE" pull "$SERVICE" >/tmp/pull-${NAME}.log 2>&1; then
  log "pull failed: $(tail -3 /tmp/pull-${NAME}.log)"
  record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "image pull failed"
  tg "WARN ${NAME} image pull failed"
  exit 1
fi

NEW_ID=$(docker image inspect "$DESIRED_IMAGE" --format '{{.Id}}' 2>/dev/null || echo "")
log "new_digest=${NEW_ID:7:12}"

if [ -n "$OLD_ID" ] && [ "$OLD_ID" = "$NEW_ID" ] && [ "$RUNNING_IMAGE" = "$DESIRED_IMAGE" ]; then
  log "already up to date, no action"
  record current "$RUNNING_IMAGE" "$DESIRED_IMAGE" "image already current"
  exit 0
fi

if ! adapter_before_real_update; then
  record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "pre-update adapter backup/check failed"
  exit 1
fi

if [ -n "$OLD_ID" ]; then
  docker tag "$OLD_ID" "$ROLLBACK_REF" || {
    log "could not tag old image as rollback"
    record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "rollback tag failed"
    tg "ERROR ${NAME} upgrade aborted: rollback tag failed"
    exit 1
  }
  log "tagged old as $ROLLBACK_REF"
fi

tg "UPGRADE ${NAME} new image detected old=${OLD_ID:7:12} new=${NEW_ID:7:12}"

log "recreating..."
compose_cmd -f "$COMPOSE_FILE" up -d --no-deps "$SERVICE" 2>&1 | grep -vE '^time=|warning' | tail -8

log "healthcheck..."
sleep 5
HEALTHY=0
tries=$((HEALTHCHECK_TIMEOUT_SEC / 5))
[ "$tries" -lt 1 ] && tries=1
for i in $(seq 1 "$tries"); do
  if adapter_health_ok; then
    HEALTHY=1
    break
  fi
  log "attempt $i: not yet healthy"
  sleep 5
done

if [ "$HEALTHY" = "1" ]; then
  log "upgrade ok"
  record upgraded "$RUNNING_IMAGE" "$DESIRED_IMAGE" "adapter $ADAPTER validation passed"
  tg "OK ${NAME} upgrade succeeded"
  if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
    docker rmi "$ROLLBACK_REF" >/dev/null 2>&1 || true
  fi
  docker image prune -f >/dev/null 2>&1 || true
  exit 0
fi

log "upgrade failed; attempting rollback"
if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
  docker tag "$ROLLBACK_REF" "$DESIRED_IMAGE"
  compose_cmd -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$SERVICE" 2>&1 | grep -vE '^time=|warning' | tail -8
  sleep 5
  if adapter_health_ok; then
    record rolled-back "$RUNNING_IMAGE" "$DESIRED_IMAGE" "health failed; rollback validated"
    tg "FAIL ${NAME} upgrade failed and rollback succeeded"
    exit 1
  fi
  record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "health failed and rollback did not validate"
  tg "ERROR ${NAME} upgrade failed and rollback did not validate"
  exit 1
fi

record failed "$RUNNING_IMAGE" "$DESIRED_IMAGE" "health failed and no rollback tag exists"
tg "ERROR ${NAME} upgrade failed and no rollback tag exists"
docker image prune -f >/dev/null 2>&1 || true
exit 1
