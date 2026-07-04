# SJC disk deep cleanup design

## Boundaries

This is an operational cleanup, not an application change. Public APIs,
database schemas, plugin ABIs, and Caddy routing should not change.

The live stack boundaries are:

- CPA runtime and plugins under `/opt/codex-stacks/cpa`.
- CPAMP data under `/opt/codex-stacks/cpamp/data`.
- Public usage portal under `/opt/codex-stacks/cpa-usage-portal`.
- Caddy front door under `/opt/codex-stacks/caddy`.
- Retained egress routers under `/opt/codex-stacks/sub2api-egress-routers`.

No-touch paths:

- `/swapfile`
- `/var/lib/containerd`
- `/opt/codex-stacks/cpa/plugin-state`
- `/opt/codex-stacks/cpamp/data`
- `/opt/codex-stacks/sub2api-egress-routers`
- current files in `/opt/codex-stacks/cpa/bin` and live plugin `.so` files
- secrets/config files mounted into running containers

## Cleanup Approach

Use a balanced cleanup:

- Remove stale files from `/tmp` only when each file is an explicit known
  upload, smoke output, or obsolete plugin artifact.
- Remove the stale upload
  `/opt/codex-stacks/uploads/cpa-key-policy-plus-cpamp-pricing.so` only after
  verifying its SHA differs from the live Plus plugin SHA.
- In the CPA plugin directory, preserve current plugin files and the newest
  rollback `.bak` per plugin, then remove older `.bak` files one explicit path
  at a time.
- Remove only explicitly identified dangling Docker image IDs. Do not run
  Docker prune.
- Vacuum systemd journal only to a bounded size if needed, using
  `journalctl --vacuum-size=80M`.

## Manual Manifest

The agent will not remove directory trees. It will record candidate manual
removals with size, last modified time, and reason:

- retired `/opt/codex-stacks/sub2api-canary`
- old `/root/migration-sjc-sub2api-restore`
- old mid-debug backup directories under `/opt/codex-stacks/backups`

Manual entries must pass no-mount and no-compose checks before being listed as
safe candidates.

## Rollback

Because the agent only removes stale files and dangling images, rollback is
mostly "redeploy from repo/build pipeline" rather than restoring deleted data.
Before cleanup, record live hashes and service state. If health fails after any
batch, stop immediately and inspect the last explicit file/image removed.
