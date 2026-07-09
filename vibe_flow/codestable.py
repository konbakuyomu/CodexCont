import re
import subprocess
from pathlib import Path
from typing import Any

from .markdown import is_fail, is_pass, markers, read_text, truthy
from .paths import relpath, strip_date_prefix


UNIT_DIRS = {
    "feature": "features",
    "issue": "issues",
    "refactor": "refactors",
    "goal": "goals",
    "roadmap": "roadmap",
}

TASK_AGENT_REVIEWERS = {"subagent", "subagent+ocr"}
WINDOWS_PATH_RE = re.compile(r"[A-Za-z]:\\[^\s`'\"<>|]+")
INACTIVE_UNIT_STAGES = {
    "accepted",
    "archived",
    "canceled",
    "cancelled",
    "complete",
    "completed",
    "done",
    "dropped",
    "paused",
}


class CodeStableAdapter:
    def __init__(self, root: Path):
        self.root = root
        self.codestable = root / ".codestable"

    def status(self, trellis: dict[str, Any] | None = None) -> dict[str, Any]:
        present = (self.codestable / "attention.md").exists()
        out: dict[str, Any] = {
            "present": present,
            "primary_unit": None,
            "unit_type": None,
            "stage": None,
            "candidates": [],
            "ambiguous": False,
            "child_unit": None,
            "diagnostics": [],
            "blocked": False,
            "blockers": [],
            "selection": {
                "status": "not_evaluated",
                "trellis_task": (trellis or {}).get("current_task"),
            },
        }
        if not present:
            if self.codestable.exists():
                out["diagnostics"].append(diag("warning", ".codestable exists but attention.md is missing"))
            return out

        candidates = self._discover_candidates()
        out["candidates"] = [self._public_candidate(c) for c in candidates]
        primary, selection_status = self._select_primary(candidates, trellis)
        out["selection"]["status"] = selection_status
        if primary is None:
            if selection_status in {"active_units_ambiguous", "trellis_task_match_ambiguous"}:
                out["ambiguous"] = True
            return out

        primary_public = self._public_candidate(primary)
        out.update(primary_public)
        if primary_public.get("blockers"):
            out["blockers"] = primary_public["blockers"]
        elif primary_public.get("blocker"):
            out["blockers"] = [primary_public["blocker"]]
        child = primary.get("child")
        if child:
            wrapper = self._public_candidate(primary)
            child_public = self._public_candidate(child)
            out["wrapper_unit"] = wrapper
            out["child_unit"] = child_public
            out.update(child_public)
            out["blocked"] = bool(wrapper.get("blocked") or child_public.get("blocked"))
            out["blockers"] = [
                item
                for item in [
                    *(wrapper.get("blockers") or []),
                    wrapper.get("blocker"),
                    *(child_public.get("blockers") or []),
                    child_public.get("blocker"),
                ]
                if item
            ]
        return out

    def _discover_candidates(self) -> list[dict[str, Any]]:
        items: list[dict[str, Any]] = []
        for unit_type, dirname in UNIT_DIRS.items():
            root = self.codestable / dirname
            if not root.exists():
                continue
            for path in sorted([p for p in root.iterdir() if p.is_dir()], key=lambda p: p.name):
                if unit_type == "feature":
                    item = self._feature(path)
                elif unit_type == "goal":
                    item = self._goal(path)
                else:
                    item = self._generic_unit(path, unit_type)
                items.append(item)
        return items

    def _select_primary(
        self,
        candidates: list[dict[str, Any]],
        trellis: dict[str, Any] | None,
    ) -> tuple[dict[str, Any] | None, str]:
        if not candidates:
            return None, "no_candidates"
        current = str((trellis or {}).get("current_task") or "")
        slug = strip_date_prefix(current)
        if slug:
            matches = [item for item in candidates if slug == item.get("slug")]
            active_matches = [item for item in matches if item.get("active")]
            if len(active_matches) == 1:
                return active_matches[0], "trellis_current_task"
            if len(matches) == 1:
                return matches[0], "trellis_current_task"
            if len(matches) > 1:
                return None, "trellis_task_match_ambiguous"
            # A current Trellis task is an ownership boundary. Falling back to
            # an unrelated historical CodeStable unit is worse than no route.
            return None, "trellis_current_task_unmatched"

        active = [item for item in candidates if item.get("active")]
        child_goals = [
            item
            for item in active
            if item.get("unit_type") == "goal" and item.get("child")
        ]
        if len(child_goals) == 1:
            wrapper = child_goals[0]
            child_path = (wrapper.get("child") or {}).get("path")
            unrelated = [
                item
                for item in active
                if item is not wrapper and item.get("path") != child_path
            ]
            if not unrelated:
                return wrapper, "active_goal_wrapper"
        if len(active) == 1:
            return active[0], "single_active_unit"
        if len(active) > 1:
            return None, "active_units_ambiguous"
        if len(candidates) == 1:
            return candidates[0], "single_inactive_unit"
        return None, "no_active_unit"

    def _feature(self, path: Path) -> dict[str, Any]:
        slug = strip_date_prefix(path.name)
        files = {
            "design": first(path, "*-design.md"),
            "design_review": first(path, "*-design-review.md"),
            "checklist": first(path, "*-checklist.yaml") or first(path, "*-checklist.yml"),
            "review": first(path, "*-review.md", exclude="*-design-review.md"),
            "qa": first(path, "*-qa.md"),
            "acceptance": first(path, "*-acceptance.md"),
            "approval": path / "approval-report.md",
        }
        stage = infer_feature_stage(files)
        gate = implementation_review_gate(files, self.root)
        external_worktrees = external_worktree_statuses(path)
        blockers = []
        if gate.get("blocking") and gate.get("blocker"):
            blockers.append(gate["blocker"])
        if stage == "accepted":
            blockers.extend(codestable_unit_git_blockers(path, self.root))
            blockers.extend(external_worktree_blockers(path, external_worktrees, self.root))
        blocked = bool(blockers)
        return {
            "path": path,
            "unit_type": "feature",
            "slug": slug,
            "stage": stage,
            "active": stage != "accepted",
            "gates": {"implementation_review": gate},
            "external_worktrees": external_worktrees,
            "blocked": blocked,
            "blocker": blockers[0] if blockers else None,
            "blockers": blockers,
        }

    def _goal(self, path: Path) -> dict[str, Any]:
        item = self._generic_unit(path, "goal")
        child = self._goal_child(path)
        if child:
            item["child"] = child
        return item

    def _goal_child(self, path: Path) -> dict[str, Any] | None:
        text = ""
        for name in ("state.yaml", "goal.md"):
            file = path / name
            if file.exists():
                text += "\n" + read_text(file)
        for feature in sorted((self.codestable / "features").glob("*")):
            if feature.is_dir() and (feature.name in text or relpath(feature, self.root) in text):
                return self._feature(feature)
        return None

    def _generic_unit(self, path: Path, unit_type: str) -> dict[str, Any]:
        state = path / "state.yaml"
        data: dict[str, str] = {}
        if state.exists():
            data = markers(state)
        stage = data.get("status") or self._canonical_unit_stage(path, unit_type)
        blocker_signature = data.get("blocker_signature")
        blocker = None
        if stage == "blocked":
            blocker = {
                "code": blocker_signature or f"{unit_type}_blocked",
                "unit": relpath(path, self.root),
                "reason": f"{unit_type} is blocked",
            }
        return {
            "path": path,
            "unit_type": unit_type,
            "slug": strip_date_prefix(path.name),
            "stage": stage,
            "active": stage not in INACTIVE_UNIT_STAGES,
            "blocked": stage == "blocked",
            "blocker": blocker,
        }

    def _canonical_unit_stage(self, path: Path, unit_type: str) -> str:
        if unit_type == "roadmap":
            roadmap = first(path, "*-roadmap.md", exclude="*-roadmap-review.md")
            return status_value(roadmap) or "active"

        if unit_type == "issue":
            fix_note = first(path, "*-fix-note.md")
            review = first(path, "*-review.md")
            review_status = status_value(review)
            if fix_note and review and is_pass(review_status):
                reviewer = (markers(review).get("reviewer") or "").strip().lower()
                return "completed" if reviewer in TASK_AGENT_REVIEWERS else "review_requires_task_agent"
            if fix_note and review and is_fail(review_status):
                return "review_failed"
            if fix_note:
                return "fix_recorded"
            if first(path, "*-analysis.md"):
                return "analyzed"
            if first(path, "*-report.md"):
                return "reported"

        return "active"

    def _public_candidate(self, item: dict[str, Any]) -> dict[str, Any]:
        out = {
            "primary_unit": relpath(item.get("path"), self.root),
            "unit_type": item.get("unit_type"),
            "stage": item.get("stage"),
            "slug": item.get("slug"),
        }
        for key in ("gates", "external_worktrees", "blocked", "blocker", "blockers"):
            value = item.get(key)
            if value:
                out[key] = value
        return out


def infer_feature_stage(files: dict[str, Path | None]) -> str:
    if files["acceptance"] and is_pass(status_value(files["acceptance"])):
        return "accepted"
    if files["qa"]:
        qa = status_value(files["qa"])
        if is_fail(qa):
            return "qa_failed"
        if is_pass(qa):
            return "qa_passed"
    if files["review"]:
        review = status_value(files["review"])
        if is_fail(review):
            return "review_failed"
        if is_pass(review):
            return "review_passed"
    if implementation_done(files["checklist"]):
        return "implementation_done"
    if design_approved(files["design"], files["approval"] if files["approval"] and files["approval"].exists() else None):
        return "approved"
    if files["design_review"]:
        review = status_value(files["design_review"])
        if is_fail(review):
            return "design_review_failed"
        return "design_review_passed"
    if files["design"]:
        return "design_draft"
    return "not_started"


def design_approved(design: Path | None, approval: Path | None) -> bool:
    for path in (design, approval):
        data = markers(path)
        if truthy(data.get("approved")) or data.get("status") == "approved" or data.get("decision") == "approved":
            return True
    return False


def implementation_done(checklist: Path | None) -> bool:
    if not checklist or not checklist.exists():
        return False
    data = markers(checklist)
    if truthy(data.get("implementation_done")) or data.get("status") in {"implementation_done", "implemented", "done", "complete", "completed"}:
        return True
    text = read_text(checklist).lower()
    return "implementation_done: true" in text or "impl_status: done" in text or "implementation: done" in text


def status_value(path: Path | None) -> str | None:
    data = markers(path)
    return data.get("status") or data.get("verdict") or data.get("decision")


def implementation_review_gate(files: dict[str, Path | None], root: Path) -> dict[str, Any]:
    review_path = files["review"]
    if not review_path:
        if implementation_done(files["checklist"]):
            return {
                "status": "missing",
                "required": True,
                "review_path": None,
                "reason": "implementation is marked done but code review report is missing",
            }
        return {"status": "not_applicable", "required": False}

    data = markers(review_path)
    status = status_value(review_path)
    reviewer = (data.get("reviewer") or "").strip().lower()
    gate = {
        "status": "unknown",
        "required": True,
        "review_path": relpath(review_path, root),
        "reviewer": reviewer or None,
    }

    if is_fail(status):
        gate["status"] = "failed"
        return gate
    if not is_pass(status):
        return gate
    if reviewer in TASK_AGENT_REVIEWERS:
        gate["status"] = "passed"
        return gate

    blocker = {
        "code": "implementation_review_requires_task_agent",
        "unit": relpath(review_path.parent, root),
        "path": relpath(review_path, root),
        "reason": "CodeStable implementation review is passed but reviewer is not a Task agent reviewer",
    }
    gate.update({
        "status": "requires_task_agent",
        "blocking": True,
        "blocker": blocker,
    })
    return gate


def external_worktree_statuses(unit_path: Path) -> list[dict[str, Any]]:
    override = unit_path / "worktree-override.md"
    if not override.exists():
        return []
    statuses = []
    seen: set[str] = set()
    for raw_path in extract_windows_paths(read_text(override)):
        path = Path(raw_path)
        key = str(path).lower()
        if key in seen:
            continue
        seen.add(key)
        statuses.append(git_worktree_status(path))
    return statuses


def extract_windows_paths(text: str) -> list[str]:
    paths = []
    for match in WINDOWS_PATH_RE.finditer(text):
        value = match.group(0).rstrip(".,;:，。；：)")
        if value:
            paths.append(value)
    return paths


def git_worktree_status(path: Path) -> dict[str, Any]:
    out: dict[str, Any] = {
        "path": str(path),
        "present": path.exists(),
        "is_git_repo": False,
        "is_clean": False,
        "branch": None,
        "recent_commit": None,
    }
    if not out["present"]:
        return out

    rc, stdout, _ = run_git(path, ["rev-parse", "--is-inside-work-tree"])
    if rc != 0 or stdout.strip().lower() != "true":
        return out

    out["is_git_repo"] = True
    _, branch, _ = run_git(path, ["branch", "--show-current"])
    out["branch"] = branch.strip() or "unknown"
    _, status, _ = run_git(path, ["status", "--porcelain"])
    out["is_clean"] = not status.strip()
    _, commit, _ = run_git(path, ["log", "-1", "--format=%h %s"])
    out["recent_commit"] = commit.strip() or None
    return out


def external_worktree_blockers(unit_path: Path, worktrees: list[dict[str, Any]], root: Path) -> list[dict[str, Any]]:
    blockers = []
    for item in worktrees:
        if not item.get("present"):
            blockers.append({
                "code": "external_worktree_missing",
                "unit": relpath(unit_path, root),
                "path": item.get("path"),
                "reason": "CodeStable feature is accepted but the external implementation directory is missing",
            })
        elif not item.get("is_git_repo"):
            blockers.append({
                "code": "external_worktree_not_git",
                "unit": relpath(unit_path, root),
                "path": item.get("path"),
                "reason": "CodeStable feature is accepted but the external implementation directory is not a Git repository",
            })
        elif not item.get("is_clean"):
            blockers.append({
                "code": "external_worktree_dirty",
                "unit": relpath(unit_path, root),
                "path": item.get("path"),
                "reason": "CodeStable feature is accepted but the external implementation repository still has uncommitted changes",
            })
    return blockers


def codestable_unit_git_blockers(unit_path: Path, root: Path) -> list[dict[str, Any]]:
    rc, stdout, _ = run_git(root, ["rev-parse", "--is-inside-work-tree"])
    if rc != 0 or stdout.strip().lower() != "true":
        return []

    rel = relpath(unit_path, root)
    rc, status, _ = run_git(root, ["status", "--porcelain", "--", rel])
    if rc != 0 or not status.strip():
        return []

    return [{
        "code": "codestable_unit_dirty",
        "unit": rel,
        "path": rel,
        "reason": "CodeStable feature is accepted but its workflow evidence has uncommitted changes",
    }]


def run_git(cwd: Path, args: list[str]) -> tuple[int, str, str]:
    try:
        proc = subprocess.run(
            ["git", *args],
            cwd=str(cwd),
            text=True,
            encoding="utf-8",
            errors="replace",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    except (FileNotFoundError, OSError) as exc:
        return 1, "", str(exc)
    return proc.returncode, proc.stdout, proc.stderr


def first(path: Path, pattern: str, *, exclude: str | None = None) -> Path | None:
    for candidate in sorted(path.glob(pattern)):
        if exclude and candidate.match(exclude):
            continue
        return candidate
    return None


def diag(level: str, message: str) -> dict[str, str]:
    return {"level": level, "message": message}
