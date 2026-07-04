# SJC disk deep cleanup

## Goal

Recover safe working space on the small SJC production VPS without disturbing
the live CPA/CodexCont stack, while recording evidence, explicit no-touch
boundaries, and a manual manifest for directory-tree cleanup that the agent is
not allowed to perform.

Initial planning evidence from 2026-07-04:

- SJC root filesystem was `9.6G` total, `9.3G` used, `247M` available, `98%`.
- Largest likely reclaim targets were `/opt/codex-stacks/backups` around
  `605M`, `/tmp` around `325M`, retired `/opt/codex-stacks/sub2api-canary`
  around `259M`, `/root/migration-sjc-sub2api-restore` around `129M`, and
  Docker dangling images around `151M`.
- Current live production hashes to preserve:
  - `/opt/codex-stacks/cpa/bin/CLIProxyAPI`:
    `8c3f98d64ed77c8ad4a9792ffbb7412b5af2e7da9a883677d559f9955049cbbf`
  - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-codexcont-executor.so`:
    `69e905579af7766abefe455f96497f7a6167c86c3574bd532e906d4a77dfbd6b`
  - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`:
    `d43e3229251175dec7d680084e516767fb698a1c7cd4290e7df1816d087157c1`

## Requirements

- Create and maintain this Trellis task as the operational evidence trail.
- Use `ssh sjc-snap` for SJC access unless live checks show it is unavailable.
- Perform a fresh preflight snapshot before cleanup: disk, top-level `du`,
  Docker state, live mounts, compose projects, current hashes, and service
  status.
- Only the agent may remove explicit single files by exact path and explicit
  Docker images by exact image ID. Do not use recursive delete, wildcard delete,
  `find -delete`, `xargs rm`, Docker prune, or directory-tree deletion.
- Generate a manual deletion manifest for directory trees such as retired
  sub2api data and old backup directories instead of deleting those trees.
- Keep current live production binaries, plugin state, secrets, CPAMP data,
  CPA plugin state, egress routers, and current rollback anchors.
- After every cleanup batch, capture `df -h /` and stop if service health or
  current live hashes drift unexpectedly.

## Acceptance Criteria

- [x] Trellis artifacts record requirements, design, implementation steps,
      cleanup actions, final evidence, and manual deletion manifest.
- [x] Current live CLIProxyAPI, executor plugin, and Plus plugin hashes are
      unchanged after cleanup.
- [x] `cpa`, `caddy-edge`, `cpamp`, `cpa-usage-portal`,
      `sub2api-egress-att`, and `sub2api-egress-direct` remain running.
- [x] Local CPA `/healthz`, public CPA `/healthz`, and
      `https://cpa-usage.konbakuyomu.us/` pass after cleanup.
- [x] No recursive/bulk filesystem deletion command is used by the agent.
- [x] Agent-safe cleanup recovered more than `700M` free; the remaining path to
      `1.2G+` free is documented as manual directory cleanup.

## Notes

- Directory trees are intentionally not deleted by the agent because
  `AGENTS.md` forbids batch or recursive deletion. The task can still identify
  exact manual cleanup candidates with evidence.
