from __future__ import annotations

import re
from pathlib import Path
from typing import Any

from .codestable import CodeStableAdapter
from .jsonio import SCHEMA_VERSION
from .trellis import TrellisAdapter
from .trellis_bridge import trellis_bridge_diagnostics


AUTOPILOT_PATTERNS = (
    "继续",
    "完成剩下",
    "自主推进",
    "自主完成",
    "接管",
    "continue",
    "complete the rest",
    "finish the rest",
    "autopilot",
)

CS_RE = re.compile(r"(?<![a-z0-9_.-])cs(?:-[a-z0-9-]+)?(?![a-z0-9-])", re.IGNORECASE)

GOAL_TAKEOVER_PATTERNS = (
    "接管",
    "完成剩下",
    "自主推进",
    "自主完成",
    "take over",
    "complete the rest",
    "finish the rest",
)


class BridgePlanner:
    def __init__(self, root: Path):
        self.root = root

    def build(self, intent: str = "") -> dict[str, Any]:
        trellis = TrellisAdapter(self.root).status()
        codestable = CodeStableAdapter(self.root).status(trellis)
        mode = self._mode(intent, trellis, codestable)
        action = self._next_action(intent, mode, trellis, codestable)
        return {
            "schema_version": SCHEMA_VERSION,
            "mode": mode,
            "trellis": trellis,
            "codestable": codestable,
            "next_action": action,
        }

    def doctor(self) -> dict[str, Any]:
        payload = self.build("")
        diagnostics: list[dict[str, Any]] = []
        if not payload["trellis"]["present"]:
            diagnostics.append(diag("warning", "trellis_missing", ".trellis not found"))
        elif not payload["trellis"]["scripts_present"]:
            diagnostics.append(diag("warning", "trellis_scripts_missing", ".trellis/scripts/task.py not found"))
        if payload["trellis"].get("session_ambiguous"):
            diagnostics.append(diag("warning", "trellis_session_ambiguous", "multiple Trellis session pointers exist; bridge will not guess"))
        if not payload["codestable"]["present"]:
            diagnostics.append(diag("warning", "codestable_missing", ".codestable/attention.md not found"))
        for item in payload["codestable"].get("diagnostics", []):
            diagnostics.append(diag(item.get("level", "warning"), "codestable", item.get("message", "")))
        for blocker in payload["codestable"].get("blockers", []):
            diagnostics.append(diag("warning", blocker.get("code", "codestable_blocked"), blocker.get("reason", "CodeStable unit is blocked")))
        diagnostics.extend(trellis_bridge_diagnostics(self.root))
        payload["diagnostics"] = diagnostics
        payload["ok"] = not any(item["level"] == "error" for item in diagnostics)
        return payload

    def _mode(self, intent: str, trellis: dict[str, Any], codestable: dict[str, Any]) -> str:
        requested = explicit_cs(intent)
        if requested and not is_goal_takeover(intent, requested):
            return "codestable_passthrough"
        if is_autopilot(intent):
            return "bridge_autopilot"
        if trellis.get("present") and not codestable.get("primary_unit"):
            return "trellis_first"
        if codestable.get("present"):
            return "codestable_passthrough"
        return "trellis_first" if trellis.get("present") else "none"

    def _next_action(self, intent: str, mode: str, trellis: dict[str, Any], codestable: dict[str, Any]) -> dict[str, Any]:
        requested = explicit_cs(intent)
        lifecycle_action = owner_lifecycle_action(intent) if not requested else None
        if lifecycle_action:
            return {
                "kind": "stop",
                "label": "direct-owner-action",
                "commands": [],
                "requires_owner": False,
                "requested_action": lifecycle_action,
                "reason": "owner explicitly confirmed a Git/Trellis lifecycle action; do not infer a CodeStable stage",
            }
        if mode == "codestable_passthrough" and requested:
            blocked = blocked_action(codestable)
            if blocked and requested != "cs-code-review":
                blocked["requested_label"] = requested
                return blocked
            return {
                "kind": "invoke_skill",
                "label": requested,
                "commands": [],
                "requires_owner": False,
                "reason": "explicit CodeStable skill intent preserves original stage-only semantics",
            }

        if codestable.get("ambiguous"):
            return {
                "kind": "ask_owner",
                "label": "choose-codestable-unit",
                "commands": [],
                "requires_owner": True,
                "reason": "multiple active CodeStable units match no current Trellis task",
                "candidates": codestable.get("candidates", []),
            }

        blocked = blocked_action(codestable)
        if blocked:
            return blocked

        if not codestable.get("present"):
            return {
                "kind": "invoke_skill" if trellis.get("present") else "stop",
                "label": "cs-onboard" if trellis.get("present") else "setup-required",
                "commands": [],
                "requires_owner": True,
                "reason": "CodeStable is not installed in this project",
            }

        if not codestable.get("primary_unit"):
            return {
                "kind": "invoke_skill",
                "label": route_from_intent(intent),
                "commands": [],
                "requires_owner": False,
                "reason": "no active CodeStable unit selected; route from intent",
            }

        if codestable.get("unit_type") != "feature":
            return {
                "kind": "invoke_skill",
                "label": label_for_unit(codestable.get("unit_type")),
                "commands": [],
                "requires_owner": False,
                "reason": "non-feature unit uses its native CodeStable workflow",
            }

        return feature_next_action(trellis, codestable)


def feature_next_action(trellis: dict[str, Any], codestable: dict[str, Any]) -> dict[str, Any]:
    blocked = blocked_action(codestable)
    if blocked:
        return blocked

    stage = codestable.get("stage") or "not_started"
    if stage == "not_started":
        return invoke("cs-feat-design", "feature has no design report")
    if stage == "design_draft":
        return invoke("cs-feat-design-review", "design exists and no design-review report found")
    if stage == "design_review_failed":
        return invoke("cs-feat-design", "design-review requested changes")
    if stage == "design_review_passed":
        return ask_owner("approve-design", "design-review passed but design is not owner-approved")
    if stage == "approved":
        if trellis.get("present") and trellis.get("status") == "planning" and trellis.get("task_path"):
            task_path = trellis["task_path"]
            return {
                "kind": "run_command",
                "label": "trellis-task-start",
                "commands": [{
                    "argv": ["python", ".trellis/scripts/task.py", "start", task_path],
                    "safe": True,
                    "reason": "Trellis task must enter in_progress before implementation",
                }],
                "requires_owner": False,
                "reason": "design approved while Trellis task is still planning",
            }
        return invoke("cs-feat-impl", "design approved and implementation may begin")
    if stage == "implementation_done":
        return invoke("cs-code-review", "implementation marker exists and review is missing")
    if stage == "review_failed":
        return invoke("cs-feat-impl", "review has blocking findings", mode="review_fix")
    if stage == "review_passed":
        return invoke("cs-feat-qa", "code review passed and QA is missing")
    if stage == "qa_failed":
        return invoke("cs-feat-impl", "QA failed or blocked", mode="qa_fix")
    if stage == "qa_passed":
        return invoke("cs-feat-accept", "QA passed and acceptance is missing")
    if stage == "accepted":
        return invoke("trellis-check", "feature accepted; run final Trellis/CodeStable finish checks")
    return invoke("cs-feat", f"unrecognized feature stage: {stage}")


def explicit_cs(intent: str) -> str | None:
    match = CS_RE.search(intent or "")
    return match.group(0).lower() if match else None


def is_autopilot(intent: str) -> bool:
    lowered = (intent or "").lower()
    return any(pattern in lowered for pattern in AUTOPILOT_PATTERNS)


def is_goal_takeover(intent: str, requested: str | None = None) -> bool:
    requested = requested or explicit_cs(intent)
    if requested != "cs-goal":
        return False
    lowered = (intent or "").lower()
    return any(pattern in lowered for pattern in GOAL_TAKEOVER_PATTERNS)


def owner_lifecycle_action(intent: str) -> str | None:
    lowered = (intent or "").lower()
    confirmed = "确认" in lowered or "confirm" in lowered
    if not confirmed:
        return None
    has_commit = "提交" in lowered or "commit" in lowered
    has_archive = "归档" in lowered or "archive" in lowered
    if has_commit and has_archive:
        return "commit_and_archive"
    if has_commit:
        return "commit"
    if has_archive:
        return "archive"
    return None


def route_from_intent(intent: str) -> str:
    lowered = (intent or "").lower()
    if any(word in lowered for word in ("bug", "报错", "修复", "issue")):
        return "cs-issue"
    if any(word in lowered for word in ("重构", "refactor", "优化")):
        return "cs-refactor"
    if "roadmap" in lowered or "规划" in lowered:
        return "cs-roadmap"
    if is_autopilot(intent):
        return "cs-goal"
    return "cs-feat"


def label_for_unit(unit_type: str | None) -> str:
    return {
        "issue": "cs-issue",
        "refactor": "cs-refactor",
        "goal": "cs-goal",
        "roadmap": "cs-roadmap",
    }.get(str(unit_type or ""), "cs")


def invoke(label: str, reason: str, **extra: Any) -> dict[str, Any]:
    out = {
        "kind": "invoke_skill",
        "label": label,
        "commands": [],
        "requires_owner": False,
        "reason": reason,
    }
    out.update(extra)
    return out


def ask_owner(label: str, reason: str, **extra: Any) -> dict[str, Any]:
    out = {
        "kind": "ask_owner",
        "label": label,
        "commands": [],
        "requires_owner": True,
        "reason": reason,
    }
    out.update(extra)
    return out


def blocked_action(codestable: dict[str, Any]) -> dict[str, Any] | None:
    blockers = codestable.get("blockers") or []
    gate = (codestable.get("gates") or {}).get("implementation_review") or {}
    if gate.get("status") == "requires_task_agent" and gate.get("blocker"):
        blockers = [gate["blocker"], *blockers]
    if not blockers:
        return None

    for blocker in blockers:
        code = str(blocker.get("code") or "").replace("-", "_")
        if code == "implementation_review_requires_task_agent":
            return ask_owner(
                "implementation-review-needs-task-agent",
                "implementation review is local/self-reviewed; CodeStable requires a Task agent reviewer before QA/acceptance can formally continue",
                blocker=blocker,
                options=[
                    "continue in an environment that can run an independent Task/check agent reviewer",
                    "or explicitly accept local-only/self-review as a downgrade before continuing",
                ],
            )
        if code in {"external_worktree_not_git", "external_worktree_dirty", "external_worktree_missing"}:
            label = {
                "external_worktree_not_git": "external-worktree-git-required",
                "external_worktree_dirty": "external-worktree-commit-needed",
                "external_worktree_missing": "external-worktree-missing",
            }[code]
            return ask_owner(
                label,
                "feature is accepted, but Trellis closeout needs the external implementation repository state settled before archive/journal",
                blocker=blocker,
                options=[
                    "initialize or commit the external implementation repository, then rerun vibe next",
                    "or explicitly record a no-git/no-commit exception before Trellis archive",
                ],
            )
        if code == "codestable_unit_dirty":
            return ask_owner(
                "codestable-evidence-commit-needed",
                "feature is accepted, but Trellis closeout needs the CodeStable evidence committed before archive/journal",
                blocker=blocker,
                options=[
                    "commit the accepted CodeStable feature evidence, then rerun vibe next",
                    "or explicitly record a no-commit exception before Trellis archive",
                ],
            )

    return ask_owner(
        "resolve-codestable-blocker",
        "CodeStable unit is blocked and needs owner or environment action before autopilot can continue",
        blockers=blockers,
    )


def diag(level: str, code: str, message: str) -> dict[str, str]:
    return {"level": level, "code": code, "message": message}
