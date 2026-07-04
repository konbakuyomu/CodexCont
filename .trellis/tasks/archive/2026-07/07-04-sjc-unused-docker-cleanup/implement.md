# SJC unused Docker cleanup implementation

## Plan

1. Record preflight disk, Docker, route, inspect, mount, and health evidence.
2. Stop `cpa-usage-portal`.
3. Validate:
   - public `https://cpa-usage.konbakuyomu.us/` returns HTTP `200`
   - public `https://cpa.konbakuyomu.us/healthz` returns OK
   - SJC local `http://127.0.0.1:8317/healthz` returns OK
   - CPAMP health returns OK
4. If validation passes, remove `cpa-usage-portal`.
5. Remove image `cpa-usage-portal:latest` by exact reference or image ID.
6. Record final disk, Docker image, container, and health evidence.
7. Archive the task.

## Guardrails

- No `docker system prune`, `docker image prune -a`, `docker volume prune`, or
  directory-tree deletion.
- Do not remove any image still used by a running container.
- Do not modify Caddy, CPA, CPAMP, egress router, or shared-hy2 configs.

## Evidence Log

### Preflight - 2026-07-04

- Host: `VPS6749127`.
- Disk before retirement: `/dev/sda1 9.6G`, `8.8G` used, `799M`
  available, `92%`.
- `cpa-usage-portal` state before cleanup:
  - container: `cpa-usage-portal`
  - image: `cpa-usage-portal:latest`
  - image ID: `f6b1ad34e477`
  - status: `Up 2 days`
  - ports: none
  - mounted read-only CPA plugin state and local portal data/secrets.
- Caddy route for `cpa-usage.konbakuyomu.us` rewrites `/` to
  `/v0/resource/plugins/cpa-key-policy-plus/user` and reverse-proxies
  `cpa:8317`.
- Docker image evidence before cleanup showed `cpa-usage-portal:latest` unique
  size `233.6MB`, attached to one container.
- Pre-stop health:
  - public `https://cpa-usage.konbakuyomu.us/`: HTTP `200`
  - public `https://cpa.konbakuyomu.us/healthz`: `{"status":"ok"}`
  - SJC local CPA `http://127.0.0.1:8317/healthz`: `{"status":"ok"}`
  - CPAMP inside container: `{"ok":true,"service":"cpa-manager-plus"}`

### Actions

- Stopped the old portal container with `docker stop cpa-usage-portal`.
- Post-stop validation passed:
  - public usage page: HTTP `200`
  - public CPA health: `{"status":"ok"}`
  - local CPA health: `{"status":"ok"}`
  - CPAMP health: `{"ok":true,"service":"cpa-manager-plus"}`
- Removed the stopped container with `docker rm cpa-usage-portal`.
- Removed the exact image with `docker image rm cpa-usage-portal:latest`.
  Docker reported deleted image
  `sha256:f6b1ad34e47763c46690fbca48c704490309a8fa9b668788601de3a5333dc448`.

No Docker prune, volume prune, broad image prune, or directory deletion was
used.

### Final verification

- Final disk: `/dev/sda1 9.6G`, `8.5G` used, `1022M` available, `90%`.
- Docker after cleanup:
  - images: `10`
  - active images: `10`
  - image size: `1.163GB`
  - reclaimable: `0B`
  - containers: `14`, all active
- `cpa-usage-portal` container is absent from `docker ps -a`.
- `cpa-usage-portal:latest` is absent from `docker images`.
- Key containers still running:
  - `cpa`
  - `caddy-edge`
  - `cpamp`
  - `cpa-admin-proxy`
  - `cpa-admin-tunnel`
  - `frontier-edge-mihomo`
  - `frontier-sub-store`
  - `shared-hy2-*`
  - `sub2api-egress-att`
  - `sub2api-egress-direct`
  - `subscription-hub`
- Final health:
  - public `https://cpa-usage.konbakuyomu.us/`: HTTP `200`
  - public `https://cpa.konbakuyomu.us/healthz`: `{"status":"ok"}`
  - SJC local CPA `http://127.0.0.1:8317/healthz`: `{"status":"ok"}`
  - CPAMP: `{"ok":true,"service":"cpa-manager-plus"}`
- CPA logs still contain registration evidence for:
  - `plugin_id=cpa-codexcont-executor`
  - `plugin_id=cpa-key-policy-plus`
