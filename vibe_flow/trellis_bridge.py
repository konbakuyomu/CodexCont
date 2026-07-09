from __future__ import annotations

from pathlib import Path
from typing import Any

from .markdown import read_text


WORKFLOW_PATH = Path(".trellis") / "workflow.md"
CODEX_HOOK_PATH = Path(".codex") / "hooks" / "inject-workflow-state.py"

LEGACY_WORKFLOW_RULE = "Do not dispatch implement/check sub-agents in inline mode."
BRIDGED_WORKFLOW_RULE = (
    "Do not dispatch Trellis implement/check sub-agents in inline mode. "
    "This does not forbid independent reviewers required by CodeStable gates; "
    "run vibe next before advancing."
)

LEGACY_CODEX_MODE_RULE = "do not dispatch implement/check sub-agents."
BRIDGED_CODEX_MODE_RULE = (
    "do not dispatch Trellis implement/check sub-agents. "
    "This does not forbid independent reviewers required by CodeStable gates; "
    "run vibe next before advancing."
)


def apply_trellis_bridge(root: Path) -> dict[str, str]:
    """Patch Trellis runtime prompts so CodeStable review gates are not blocked."""
    return {
        "workflow": _patch_file(root / WORKFLOW_PATH, LEGACY_WORKFLOW_RULE, BRIDGED_WORKFLOW_RULE),
        "codex_hook": _patch_file(root / CODEX_HOOK_PATH, LEGACY_CODEX_MODE_RULE, BRIDGED_CODEX_MODE_RULE),
    }


def trellis_bridge_status(root: Path) -> dict[str, str]:
    return {
        "workflow": _file_status(root / WORKFLOW_PATH, LEGACY_WORKFLOW_RULE, BRIDGED_WORKFLOW_RULE),
        "codex_hook": _file_status(root / CODEX_HOOK_PATH, LEGACY_CODEX_MODE_RULE, BRIDGED_CODEX_MODE_RULE),
    }


def trellis_bridge_diagnostics(root: Path) -> list[dict[str, Any]]:
    diagnostics: list[dict[str, Any]] = []
    statuses = trellis_bridge_status(root)
    if statuses["workflow"] == "missing_patch":
        diagnostics.append(_diag(
            "warning",
            "trellis_workflow_bridge_missing",
            ".trellis/workflow.md still has the broad inline sub-agent ban; run vibe init to narrow it for CodeStable gates",
        ))
    if statuses["codex_hook"] == "missing_patch":
        diagnostics.append(_diag(
            "warning",
            "codex_hook_bridge_missing",
            ".codex/hooks/inject-workflow-state.py still injects the broad sub-agent ban; run vibe init to narrow it for CodeStable gates",
        ))
    return diagnostics


def _patch_file(path: Path, legacy: str, bridged: str) -> str:
    if not path.exists():
        return "missing"
    text = read_text(path)
    if bridged in text:
        return "already_patched"
    if legacy not in text:
        return "not_needed"
    path.write_text(text.replace(legacy, bridged), encoding="utf-8")
    return "patched"


def _file_status(path: Path, legacy: str, bridged: str) -> str:
    if not path.exists():
        return "missing"
    text = read_text(path)
    if bridged in text:
        return "patched"
    if legacy in text:
        return "missing_patch"
    return "not_needed"


def _diag(level: str, code: str, message: str) -> dict[str, str]:
    return {"level": level, "code": code, "message": message}
