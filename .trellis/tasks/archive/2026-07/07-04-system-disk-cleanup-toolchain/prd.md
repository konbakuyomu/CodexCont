# System Disk Cleanup Toolchain

## Goal

Build a reproducible, AI-friendly disk audit toolchain for this Windows
machine, then run a read-only baseline inventory that identifies cleanup
candidates without deleting files, pruning Docker data, compacting WSL disks, or
mutating active projects.

## Requirements

- Use this Trellis task as the evidence trail for tool installation, read-only
  inventory, candidate classification, and final summary.
- Install the planned CLI tools through existing Scoop buckets:
  `extras/sysinternals`, `main/gdu`, `extras/wiztree`, `main/everything-cli`,
  `main/fd`, `main/ripgrep`, and `main/yq`.
- Use existing `mise`-managed runtime tools (`python`, `node`, `jq`, `uv`) for
  parsing and reporting, not as the main disk scanner.
- Store large raw scan output outside the repository under
  `D:\Dev\20_Software\_LocalRuntime\DiskAudit\reports\<timestamp>\`.
- Run the audit as the current non-admin user first. Mark protected paths and
  permission failures as blind spots instead of elevating automatically.
- Capture read-only evidence for:
  - Windows drive capacity and top-level usage.
  - `D:\Dev\20_Software\_LocalRuntime`.
  - `D:\Dev`.
  - common user and package-manager caches.
  - WSL distro state and VHDX locations/sizes.
  - Docker CLI/daemon status, without starting Docker Desktop if it is stopped.
- Classify cleanup candidates as `safe-cache-command`,
  `regenerable-build-artifact`, `manual-review-directory`,
  `admin-blind-spot`, or `do-not-touch`.
- Do not execute destructive cleanup. Directory-tree cleanup must be output as a
  manual review manifest only.

## Acceptance Criteria

- [x] Each installed/planned tool has `which`, version, or help evidence.
- [x] Report directory contains drive summary, tool versions, DU CSV, WizTree
      attempt or export, WSL summary, Docker status, package-cache summary, and
      cleanup candidates.
- [x] Cleanup candidates include path, size when available, classification,
      evidence command, and recommended action.
- [x] DU, WizTree/scan output, and PowerShell drive summaries are cross-checked;
      differences over 5% are explained.
- [x] No forbidden cleanup command is used: no recursive filesystem deletion, no
      Docker prune, no WSL unregister, no VHD optimize/compact.
- [x] Final task notes identify the report directory and any admin-only
      follow-up scan needs.

## Notes

- The current session is non-admin. This is intentional for the first pass.
- Current local policy from memory: long-lived software runtime state and shared
  package caches should live under `D:\Dev\20_Software\_LocalRuntime`.
- Final report directory:
  `D:\Dev\20_Software\_LocalRuntime\DiskAudit\reports\20260704-234120`.
- Admin-only follow-up need: explain the C: protected/system-storage gap
  (`pagefile.sys`, `hiberfil.sys`, shadow storage, protected metadata) with
  elevated Windows storage tools only if explicitly approved later.
