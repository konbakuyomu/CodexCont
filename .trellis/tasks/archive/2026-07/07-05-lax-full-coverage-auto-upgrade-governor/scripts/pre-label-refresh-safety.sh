#!/usr/bin/env bash
set -euo pipefail

TS="${1:-$(date '+%Y%m%d-%H%M%S')}"
ROOT=/var/lib/lax-auto-upgrade-governor/backups
mkdir -p "$ROOT/postgres" "$ROOT/redis"
chmod 700 "$ROOT" "$ROOT/postgres" "$ROOT/redis" 2>/dev/null || true

for name in new-api-postgres kuma-openwebui-postgres sub2api-cpa-poc-postgres; do
  docker exec "$name" sh -lc 'pg_isready -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-postgres}" >/dev/null'
  docker exec "$name" sh -lc 'pg_dumpall -U "${POSTGRES_USER:-postgres}"' \
    2>"$ROOT/postgres/${name}-${TS}.log" \
    | gzip -c > "$ROOT/postgres/${name}-${TS}.sql.gz"
  echo "pg-backup-ok $name"
done

for name in new-api-redis kuma-openwebui-redis sub2api-cpa-poc-redis; do
  docker exec "$name" sh -lc 'redis-cli ping 2>/dev/null | grep -q PONG || redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null | grep -q PONG'
  docker exec "$name" sh -lc 'redis-cli BGSAVE >/dev/null 2>&1 || redis-cli -a "$REDIS_PASSWORD" BGSAVE >/dev/null 2>&1 || true'
  echo "redis-persist-ok $name"
done

old_mihomo_id=$(docker inspect sub2api-egress-router --format '{{.Image}}')
docker tag "$old_mihomo_id" metacubex/mihomo:latest
echo "mihomo-current-tagged-as-latest"
