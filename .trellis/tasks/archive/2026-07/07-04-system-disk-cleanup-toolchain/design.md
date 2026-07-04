# Design

## Architecture

This task is an operational audit, not a code feature. The implementation has
three layers:

1. Toolchain layer: install and verify read-only analysis CLIs via Scoop.
2. Evidence layer: write raw command output under
   `D:\Dev\20_Software\_LocalRuntime\DiskAudit\reports\<timestamp>\`.
3. Synthesis layer: parse raw output into JSON/Markdown summaries and cleanup
   candidate manifests.

## Tool Selection

- Sysinternals `du`: canonical Windows directory-size CSV generator; installed
  through `extras/sysinternals`.
- `gdu`: fast CLI disk usage analyzer for targeted follow-up and
  cross-checks.
- WizTree: optional high-speed NTFS scan/export. It must run non-admin in this
  first pass; permission or MFT limitations are recorded, not bypassed.
- `everything-cli`: fast path/file discovery through the already installed
  Everything service when available.
- `fd`, `rg`, `yq`, `jq`, Python: discovery and parsing utilities.

## Data Flow

Raw command outputs are captured first. A Python synthesis script then reads the
small, stable files it can parse and emits:

- `tool-versions.json`
- `drive-summary.json`
- `wsl-summary.json`
- `docker-status.json`
- `package-cache-summary.json`
- `cleanup-candidates.json`
- `summary.md`
- `manifest.json`

Large CSV exports remain outside git. Trellis artifacts reference paths and
summaries only.

## Boundaries

- No cleanup command is executed in this task.
- Directory trees are not deleted by the agent. They may only appear in
  `manual-review-directory` candidates.
- Docker daemon is not started automatically. If unavailable, record the client
  version and daemon error.
- WSL VHDX files are located and measured only. No `wsl --unregister`,
  `Optimize-VHD`, compaction, export/import, or sparse conversion is allowed.
- Non-admin blind spots are first-class findings, not failures.

## Evidence Sources

Prior source research used these official or primary references:

- Microsoft Sysinternals DU:
  https://learn.microsoft.com/en-us/sysinternals/downloads/du
- Microsoft WSL disk-space docs:
  https://learn.microsoft.com/en-us/windows/wsl/disk-space
- Docker disk usage/prune docs:
  https://docs.docker.com/reference/cli/docker/system/df/
  https://docs.docker.com/engine/manage-resources/pruning/
- pnpm store docs: https://pnpm.io/cli/store
- pip cache docs: https://pip.pypa.io/en/stable/cli/pip_cache/
- WizTree command-line guide: https://diskanalyzer.com/guide

## Final Evidence Layout

Raw and synthesized audit outputs live outside git at:
`D:\Dev\20_Software\_LocalRuntime\DiskAudit\reports\20260704-234120`.

Important synthesized files:

- `summary.md` - human-readable drive, cache, WSL/Docker, and candidate summary.
- `manifest.json` - report file inventory with sizes and hashes.
- `cleanup-candidates.json` - path-level candidate list with classification,
  risk, evidence command, and recommended manual action.
- `package-cache-summary.json` - package/cache paths and measured sizes.
- `cross-checks.json` - DU/WizTree/PowerShell consistency checks and explained
  differences.
- `validation.json` - acceptance validation; final status is `pass`.

The first pass intentionally remains non-admin. The C: gap is recorded as an
`admin-blind-spot`; D: DU and WizTree match closely enough for first-pass
triage.
