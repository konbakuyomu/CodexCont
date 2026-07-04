# SJC disk deep cleanup implementation

## Ordered Steps

1. Create task artifacts and start the Trellis task.
2. Capture preflight evidence:
   - `df -h /`
   - top-level `du` for `/`, `/opt/codex-stacks`, `/root`, `/tmp`, `/var`
   - live Docker mounts and compose projects
   - `docker system df -v`
   - live binary/plugin hashes
   - current relevant containers
3. Build an explicit single-file cleanup list:
   - stale `/tmp/*.so` and staged plugin files
   - stale `/tmp` smoke output files only when clearly non-secret and old
   - `/opt/codex-stacks/uploads/cpa-key-policy-plus-cpamp-pricing.so`
   - older CPA plugin `.bak` files, keeping newest rollback per plugin
4. Delete agent-owned files one command per explicit path. No loops, wildcards,
   recursive deletion, or directory deletion.
5. Remove dangling Docker images one command per explicit ID.
6. Optionally run `journalctl --vacuum-size=80M` if disk is still critically
   low after file/image cleanup.
7. Capture post-cleanup evidence and verify:
   - live hashes unchanged
   - key containers running
   - local/public health probes pass
8. Write final evidence and manual directory cleanup manifest to this file.
9. Archive the task after verification.

## Validation Commands

- `ssh sjc-snap 'df -h /'`
- `ssh sjc-snap 'sha256sum ...'` for live binary/plugin paths
- `ssh sjc-snap 'docker ps --format ...'`
- `ssh sjc-snap 'curl -fsS http://127.0.0.1:8317/healthz'`
- `curl -fsS https://cpa.konbakuyomu.us/healthz`
- `curl -fsSI https://cpa-usage.konbakuyomu.us/`

## Evidence Log

### Preflight - 2026-07-04

- SJC alias: `sjc-snap`.
- Root disk before cleanup: `/dev/sda1 9.6G`, `9.3G` used, `243M`
  available, `98%`.
- Largest local targets:
  - `/tmp`: `325M`
  - `/opt/codex-stacks/uploads`: `15M`
  - `/opt/codex-stacks/cpa/plugins`: `141M`
  - `/opt/codex-stacks/backups`: `605M`
  - `/opt/codex-stacks/sub2api-canary`: `259M`
  - `/root/migration-sjc-sub2api-restore`: `129M`
- Live hashes before cleanup:
  - `/opt/codex-stacks/cpa/bin/CLIProxyAPI`:
    `8c3f98d64ed77c8ad4a9792ffbb7412b5af2e7da9a883677d559f9955049cbbf`
  - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-codexcont-executor.so`:
    `69e905579af7766abefe455f96497f7a6167c86c3574bd532e906d4a77dfbd6b`
  - `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`:
    `d43e3229251175dec7d680084e516767fb698a1c7cd4290e7df1816d087157c1`
- Docker reclaimable image evidence before cleanup:
  - dangling `21424bc242e8`: unique size `75.6MB`, `0` containers
  - dangling `8361d362e023`: unique size `75.6MB`, `0` containers

### Agent-executable cleanup

Deleted these explicit single files from `/tmp`, one `rm -- <path>` command per
path:

- `/tmp/cpa-codexcont-executor.so`
- `/tmp/cpa-codexcont-executor.f9b4fe4e.so`
- `/tmp/cpa-codexcont-executor.e3b00ab3.so`
- `/tmp/cpa-codexcont-executor-menu-clean.so`
- `/tmp/cpa-codexcont-executor.49953873.so`
- `/tmp/cpa-codexcont-executor.so.new`
- `/tmp/cpa-codexcont-executor-identity.so`
- `/tmp/cpa-codexcont-executor-identity-v2.so`
- `/tmp/cpa-codexcont-executor-identity-v3.so`
- `/tmp/cpa-codexcont-executor-identity-v4.so`
- `/tmp/cpa-codexcont-executor.so.codex-app-upstream-error-context-window`
- `/tmp/plus-live-codexcont-processing-feed/cpa-codexcont-executor.so`
- `/tmp/cpa-governor-deploy/cpa-governor.so`
- `/tmp/cpa-key-policy-plus.so.session-cookie-fix`
- `/tmp/cpa-key-policy-plus-authid.so`
- `/tmp/cpa-key-policy-plus.so`
- `/tmp/cpa-key-policy-plus-4cfb5cdc0633bb428b8526c9146e6705457d753c240e7417e50a875e2fca4adc.so`
- `/tmp/cpa-key-policy-plus-authid-visible-model.so`
- `/tmp/cpa-key-policy-plus.so.new`
- `/tmp/cpa-key-policy-plus-identity.so`
- `/tmp/cpa-key-policy-plus-identity-v2.so`
- `/tmp/cpa-key-policy-plus-identity-v3.so`
- `/tmp/plus-live-codexcont-processing-feed/cpa-key-policy-plus.so`
- `/tmp/plus-codexcont-detail-alignment/cpa-key-policy-plus.so`

Deleted the stale upload after confirming its SHA
`ee4caa28724eefbbaa2fc0c1351a0aafcca54955c762965e03100805328c0c5c`
did not match the live Plus plugin SHA:

- `/opt/codex-stacks/uploads/cpa-key-policy-plus-cpamp-pricing.so`

Deleted older Plus plugin rollback files one explicit path at a time. Preserved
the current live Plus plugin and newest rollback
`cpa-key-policy-plus.so.bak-20260704-051441`:

- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-`
- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-20260703-215216`
- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-20260703-215242`
- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-20260703-221226`
- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-20260703-232706`
- `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so.bak-20260704-051129`

Removed dangling Docker images one explicit ID at a time. No Docker prune was
used:

- `docker image rm 21424bc242e8`
- `docker image rm 8361d362e023`

Disk checkpoints:

- After `/tmp` artifact cleanup: `559M` available, `95%`.
- After upload/plugin-backup cleanup: `663M` available, `94%`.
- After dangling-image cleanup: `807M` available, `92%`.

No `journalctl --vacuum-size=80M` was run because the agent-safe cleanup
already exceeded the `700M` target and journal vacuum would internally remove
multiple log files.

### Final verification

- Final root disk: `/dev/sda1 9.6G`, `8.8G` used, `807M` available, `92%`.
- `/tmp` reduced from `325M` to `17M`.
- `/opt/codex-stacks/uploads` reduced to `4.0K`.
- Docker after cleanup: `Images 11`, `reclaimable 0B`; containers and local
  volumes unchanged.
- CPA plugin directory reduced from `141M` to `52M`. Preserved files:
  - `cpa-codexcont-executor.so`
  - `cpa-codexcont-executor.so.bak-20260703-232706`
  - `cpa-key-policy-plus.so`
  - `cpa-key-policy-plus.so.bak-20260704-051441`
- Live hashes after cleanup matched preflight exactly:
  - CLIProxyAPI:
    `8c3f98d64ed77c8ad4a9792ffbb7412b5af2e7da9a883677d559f9955049cbbf`
  - executor:
    `69e905579af7766abefe455f96497f7a6167c86c3574bd532e906d4a77dfbd6b`
  - Plus:
    `d43e3229251175dec7d680084e516767fb698a1c7cd4290e7df1816d087157c1`
- Relevant containers still running:
  - `cpa`
  - `caddy-edge`
  - `cpamp` healthy
  - `cpa-usage-portal`
  - `sub2api-egress-att`
  - `sub2api-egress-direct`
- Health probes:
  - SJC local `http://127.0.0.1:8317/healthz`: `{"status":"ok"}`
  - public `https://cpa.konbakuyomu.us/healthz`: `{"status":"ok"}`
  - public GET `https://cpa-usage.konbakuyomu.us/`: HTTP `200`
- CPA logs still show both plugins registered:
  - `plugin_id=cpa-codexcont-executor`
  - `plugin_id=cpa-key-policy-plus`
- No recursive delete, wildcard delete, `find -delete`, `xargs rm`, Docker
  prune, or directory-tree deletion was used.

## Manual Directory Cleanup Manifest

The following were not deleted by the agent because they are directories or
directory families. They passed final no-live-mount checks and current Caddy /
CPA / admin proxy configs do not reference `sub2api-canary`.

High-value manual candidates:

- `/opt/codex-stacks/sub2api-canary`: `259M`; retired sub2api app/Postgres/Redis
  data. The migration task intentionally preserved it, so deleting it is a new
  manual retention decision.
- `/root/migration-sjc-sub2api-restore`: `129M`; old restore snapshot from the
  June sub2api migration era.

Backup condensation candidates if older debug rollback points are no longer
needed:

- old executor debug backups matching
  `/opt/codex-stacks/backups/cpa-codexcont-executor-*`
- old visible-model Plus backups matching
  `/opt/codex-stacks/backups/cpa-key-policy-plus-visible-model-*`
- stale unnamed backup `/opt/codex-stacks/backups/cpa-key-policy-plus-`
- superseded identity backups
  `/opt/codex-stacks/backups/codexcont-identity-feed-20260704-072009` and
  `/opt/codex-stacks/backups/codexcont-identity-feed-v3-20260704-072957`

Read-only `du` across these manual candidates reported about `755M` total.
Removing the two high-value retired-data directories plus selected old debug
backups should push SJC comfortably past `1.2G` free, but those removals must be
done manually or under a separate explicit deletion approval because project
rules forbid agent-side batch/directory deletion.
