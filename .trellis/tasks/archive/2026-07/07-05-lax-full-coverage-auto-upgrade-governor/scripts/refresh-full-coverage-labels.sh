#!/usr/bin/env bash
set -euo pipefail

run_compose() {
  local dir="$1"
  local file="$2"
  shift 2
  echo "refresh-labels $dir :: $*"
  docker compose --project-directory "$dir" -f "$file" up -d --no-deps --force-recreate --pull never "$@"
}

run_compose /opt/frontier/apps/new-api /opt/frontier/apps/new-api/docker-compose.yml postgres redis new-api
run_compose /opt/frontier/apps/sub2api /opt/frontier/apps/sub2api/docker-compose.yml postgres redis sub2api-egress-router sub2api
run_compose /opt/frontier/apps/openwebui /opt/frontier/apps/openwebui/docker-compose.yml postgres redis app
run_compose /opt/frontier/apps/codemerge /opt/frontier/apps/codemerge/docker-compose.yml codemerge
run_compose /opt/frontier/apps/fast-note-sync-service /opt/frontier/apps/fast-note-sync-service/docker-compose.yaml fast-note-sync-service
run_compose /opt/frontier/apps/grok2api-jiujiu /opt/frontier/apps/grok2api-jiujiu/docker-compose.yml grok2api-jiujiu
run_compose /opt/frontier/apps/prompt-manager /opt/frontier/apps/prompt-manager/docker-compose.yml prompt-manager
run_compose /opt/frontier/apps/diun /opt/frontier/apps/diun/docker-compose.yml diun
