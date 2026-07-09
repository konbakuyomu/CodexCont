import subprocess
from pathlib import Path
from typing import Any

from .planner import BridgePlanner


DENIED_LABELS = {"commit", "merge", "push", "deploy", "secret", "approve-design"}


def run_next(root: Path, intent: str = "") -> dict[str, Any]:
    payload = BridgePlanner(root).build(intent)
    action = payload.get("next_action", {})
    if action.get("requires_owner"):
        payload["run"] = {"executed": False, "reason": "next action requires owner approval"}
        return payload
    if action.get("kind") != "run_command":
        payload["run"] = {"executed": False, "reason": "next action is not a command"}
        return payload
    if denied(action.get("label", "")):
        payload["run"] = {"executed": False, "reason": "next action label is owner-gated"}
        return payload
    results = []
    for command in action.get("commands", []):
        if not command.get("safe"):
            payload["run"] = {"executed": False, "reason": "command is not marked safe", "command": command}
            return payload
        argv = command.get("argv")
        if not allowlisted(argv):
            payload["run"] = {"executed": False, "reason": "command is not allowlisted", "command": command}
            return payload
        proc = subprocess.run(argv, cwd=root, text=True, encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        results.append({"argv": argv, "returncode": proc.returncode, "stdout": proc.stdout, "stderr": proc.stderr})
        if proc.returncode != 0:
            break
    payload["run"] = {"executed": bool(results), "results": results}
    return payload


def denied(label: str) -> bool:
    lowered = str(label or "").lower()
    return any(word in lowered for word in DENIED_LABELS)


def allowlisted(argv: Any) -> bool:
    if not isinstance(argv, list) or len(argv) < 2:
        return False
    joined = " ".join(str(part).replace("\\", "/") for part in argv).lower()
    allowed_needles = (
        ".trellis/scripts/task.py start",
        ".trellis/scripts/get_context.py",
        ".codestable/tools/codestable-worktree-gate.py",
        ".codestable/tools/validate-yaml.py",
        ".codestable/tools/build-review-packet.py",
        ".codestable/tools/check-context-sufficiency.py",
    )
    forbidden = (" commit", " merge", " push", " deploy", "rm ", "remove-item", "rmdir", "del /s")
    return any(needle in joined for needle in allowed_needles) and not any(word in joined for word in forbidden)
