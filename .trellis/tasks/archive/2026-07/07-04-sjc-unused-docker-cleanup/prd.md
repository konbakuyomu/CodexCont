# SJC unused Docker cleanup

## Goal

Retire the unused `cpa-usage-portal` Docker container and its image on the SJC
VPS to recover disk space while keeping the live CPA usage page served by
`cpa-key-policy-plus` healthy.

Current confirmed state from live planning:

- Host: `VPS6749127`.
- Root disk before this cleanup was around `9.6G` total and `800M` available.
- Public `cpa-usage.konbakuyomu.us` is served by Caddy rewriting to
  `/v0/resource/plugins/cpa-key-policy-plus/user` and reverse proxying
  `cpa:8317`.
- `cpa-usage-portal` is still running but has no host ports and is not used by
  the current public route.

## Requirements

- Create a Trellis evidence trail for the cleanup.
- Do a fresh preflight snapshot before stopping anything.
- Stop `cpa-usage-portal`, then validate public usage page, CPA health, and
  CPAMP health before removing the container.
- If validation fails after stop, restart `cpa-usage-portal` and stop cleanup.
- If validation passes, remove only the `cpa-usage-portal` container and the
  exact `cpa-usage-portal:latest` image.
- Do not run Docker prune, image prune, volume prune, recursive delete, or
  directory-tree deletion.
- Do not touch live CPA/CPAMP state, egress routers, shared-hy2, frontier
  services, Caddy config, or secrets.

## Acceptance Criteria

- [x] `cpa-usage-portal` container is removed.
- [x] `cpa-usage-portal:latest` image is removed.
- [x] Public `https://cpa-usage.konbakuyomu.us/` returns HTTP `200` after
      removal.
- [x] Public and local CPA health remain OK after removal.
- [x] CPAMP remains healthy after removal.
- [x] Other key containers remain running.
- [x] Final disk space and Docker image state are recorded.
- [x] No broad Docker prune or directory deletion is used.

## Notes

- This task is intentionally limited to unused Docker container/image cleanup.
  Retired data directories such as `/opt/codex-stacks/sub2api-canary` are out
  of scope for this run.
