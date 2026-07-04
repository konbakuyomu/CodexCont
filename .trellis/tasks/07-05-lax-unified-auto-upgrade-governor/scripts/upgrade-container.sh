#!/usr/bin/env bash
set -uo pipefail

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=1
  shift
fi

NAME="${1:-}"
[ -n "$NAME" ] || { echo "usage: $0 [--dry-run] <container_name>" >&2; exit 1; }

LIB=/etc/auto-upgrade.d/lib/notify.sh
LOCK=/tmp/upgrade-${NAME//[^a-zA-Z0-9_-]/_}.lock

[ -f "$LIB" ] && source "$LIB" || tg() { :; }

log() { echo "$(date '+%F %T') [$NAME] $*"; }

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

health_ok() {
  local health_url="$1"
  if [ -n "$health_url" ]; then
    local resp
    resp=$(curl -fsS --max-time 10 "$health_url" 2>/dev/null || true)
    [ -n "$resp" ] || return 1
    echo "$resp" | grep -qE '"status":"(ok|healthy)"|"healthy":true' && return 0
    return 0
  fi

  local status health
  status=$(docker inspect "$NAME" --format '{{.State.Status}}' 2>/dev/null || echo "")
  health=$(docker inspect "$NAME" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo "")
  [ "$status" = "running" ] || return 1
  [ "$health" = "unhealthy" ] && return 1
  [ "$health" = "starting" ] && return 1
  return 0
}

image_base() {
  local image="$1"
  if [[ "$image" == *@sha256:* ]]; then
    echo ""
    return 0
  fi
  if [[ "$image" == sha256:* ]]; then
    echo ""
    return 0
  fi
  if [[ "$image" == *:* ]] && [[ "${image##*/}" == *:* ]]; then
    echo "${image%:*}"
    return 0
  fi
  echo "$image"
}

label() {
  docker inspect "$NAME" --format "{{index .Config.Labels \"$1\"}}" 2>/dev/null || true
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
  tg "WARN ${NAME} upgrade skipped: missing docker compose labels"
  exit 1
fi

HEALTH_URL=$(label "autoupgrade.health-url")
BACKUP_STRATEGY=$(label "autoupgrade.strategy")
MIN_DISK_GB=$(label "autoupgrade.min-disk-gb")
HEALTHCHECK_TIMEOUT_SEC=$(label "autoupgrade.healthcheck-sec")

: "${BACKUP_STRATEGY:=rollback-tag-only}"
: "${MIN_DISK_GB:=20}"
: "${HEALTHCHECK_TIMEOUT_SEC:=30}"

if [ -n "${AUTOGOVERNOR_MIN_DISK_GB_OVERRIDE:-}" ]; then
  MIN_DISK_GB="$AUTOGOVERNOR_MIN_DISK_GB_OVERRIDE"
fi

log "config: dir=$COMPOSE_DIR svc=$SERVICE strategy=$BACKUP_STRATEGY disk_min=${MIN_DISK_GB}GB hc_sec=${HEALTHCHECK_TIMEOUT_SEC}s dry_run=$DRY_RUN"

exec 200>"$LOCK"
flock -n 200 || { log "another run in progress; skipped"; exit 0; }

AVAIL_BYTES=$(df -B1 / --output=avail | tail -1 | tr -dc '0-9')
MIN_BYTES=$(min_disk_bytes "$MIN_DISK_GB")
log "disk avail_bytes=${AVAIL_BYTES} min_bytes=${MIN_BYTES}"
if [ "${AVAIL_BYTES:-0}" -lt "${MIN_BYTES:-0}" ]; then
  log "disk low; skipped"
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
[ -n "$COMPOSE_FILE" ] || { log "no compose file found"; exit 1; }

if ! compose_cmd -f "$COMPOSE_FILE" config >/tmp/compose-config-${NAME}.log 2>&1; then
  log "compose config failed: $(tail -3 /tmp/compose-config-${NAME}.log)"
  exit 1
fi

if [ "$DRY_RUN" = "1" ]; then
  if health_ok "$HEALTH_URL"; then
    log "dry-run ok"
    exit 0
  fi
  log "dry-run health check failed"
  exit 1
fi

IMAGE=$(docker inspect "$NAME" --format '{{.Config.Image}}')
BASE=$(image_base "$IMAGE")
if [ -z "$BASE" ]; then
  log "image reference is digest-only; cannot create rollback tag"
  tg "WARN ${NAME} upgrade skipped: digest-only image cannot use rollback tag"
  exit 1
fi
ROLLBACK_REF="${BASE}:rollback"

OLD_ID=$(docker inspect "$IMAGE" --format '{{.Id}}' 2>/dev/null || echo "")
log "image=$IMAGE old_digest=${OLD_ID:7:12}"

if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
  log "removing stale rollback tag: $ROLLBACK_REF"
  docker rmi "$ROLLBACK_REF" >/dev/null 2>&1 || true
fi

log "pulling..."
if ! compose_cmd -f "$COMPOSE_FILE" pull "$SERVICE" >/tmp/pull-${NAME}.log 2>&1; then
  log "pull failed: $(tail -3 /tmp/pull-${NAME}.log)"
  tg "WARN ${NAME} image pull failed"
  exit 1
fi

NEW_ID=$(docker inspect "$IMAGE" --format '{{.Id}}' 2>/dev/null || echo "")
log "new_digest=${NEW_ID:7:12}"

if [ -n "$OLD_ID" ] && [ "$OLD_ID" = "$NEW_ID" ]; then
  log "already up to date, no action"
  exit 0
fi

if [ -n "$OLD_ID" ]; then
  docker tag "$OLD_ID" "$ROLLBACK_REF" || {
    log "could not tag old image as rollback"
    tg "ERROR ${NAME} upgrade aborted: rollback tag failed"
    exit 1
  }
  log "tagged old as $ROLLBACK_REF"
fi

tg "UPGRADE ${NAME} new image detected old=${OLD_ID:7:12} new=${NEW_ID:7:12}"

log "recreating..."
compose_cmd -f "$COMPOSE_FILE" up -d "$SERVICE" 2>&1 | grep -vE '^time=|warning' | tail -8

log "healthcheck..."
sleep 5
HEALTHY=0
tries=$((HEALTHCHECK_TIMEOUT_SEC / 5))
[ "$tries" -lt 1 ] && tries=1
for i in $(seq 1 "$tries"); do
  if health_ok "$HEALTH_URL"; then
    HEALTHY=1
    break
  fi
  log "attempt $i: not yet healthy"
  sleep 5
done

if [ "$HEALTHY" = "1" ]; then
  log "upgrade ok"
  tg "OK ${NAME} upgrade succeeded"
  if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
    docker rmi "$ROLLBACK_REF" >/dev/null 2>&1 || true
  fi
  docker image prune -f >/dev/null 2>&1 || true
  exit 0
fi

log "upgrade failed; attempting rollback"
if docker inspect "$ROLLBACK_REF" >/dev/null 2>&1; then
  docker tag "$ROLLBACK_REF" "$IMAGE"
  compose_cmd -f "$COMPOSE_FILE" up -d --force-recreate "$SERVICE" 2>&1 | grep -vE '^time=|warning' | tail -8
  tg "FAIL ${NAME} upgrade failed and rollback was attempted"
else
  tg "ERROR ${NAME} upgrade failed and no rollback tag exists"
fi

docker image prune -f >/dev/null 2>&1 || true
exit 1
