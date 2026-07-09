from pathlib import Path


def create_fixture(root: Path, name: str) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    if name == "empty":
        return root
    if name in {"trellis_only", "both_approved_planning"}:
        write_trellis(root, status="planning", task="07-08-export-csv")
    if name in {"codestable_only_design", "both_approved_planning", "goal_wraps_feature"}:
        write_codestable_base(root)
    if name == "codestable_only_design":
        write_feature(root, "2026-07-08-export-csv", design=True)
    elif name == "both_approved_planning":
        write_feature(root, "2026-07-08-export-csv", design=True, design_review=True, approved=True)
    elif name == "goal_wraps_feature":
        write_feature(root, "2026-07-08-export-csv", design=True, design_review=True, approved=True, implementation_done=True)
        goal = root / ".codestable" / "goals" / "2026-07-08-export-csv-goal"
        goal.mkdir(parents=True, exist_ok=True)
        (goal / "state.yaml").write_text("schema_version: 1\ngoal: export-csv\nstatus: active\nchild_unit: .codestable/features/2026-07-08-export-csv\n", encoding="utf-8")
    return root


def write_trellis(root: Path, *, status: str, task: str) -> None:
    task_dir = root / ".trellis" / "tasks" / task
    scripts = root / ".trellis" / "scripts"
    runtime = root / ".trellis" / ".runtime" / "sessions"
    task_dir.mkdir(parents=True, exist_ok=True)
    scripts.mkdir(parents=True, exist_ok=True)
    runtime.mkdir(parents=True, exist_ok=True)
    (root / ".trellis" / "workflow.md").write_text("# workflow\n", encoding="utf-8")
    (task_dir / "task.json").write_text(f'{{"id":"export-csv","status":"{status}"}}\n', encoding="utf-8")
    (scripts / "task.py").write_text(
        "import sys\n"
        "if sys.argv[1:3] == ['current','--source']:\n"
        f"    print('Current task: .trellis/tasks/{task}')\n    print('Source: fixture')\n"
        "elif sys.argv[1:2] == ['start']:\n"
        "    print('started')\n"
        "else:\n"
        "    print('fixture task.py')\n",
        encoding="utf-8",
    )


def write_codestable_base(root: Path) -> None:
    base = root / ".codestable"
    for name in ("features", "issues", "refactors", "goals", "roadmap", "tools"):
        (base / name).mkdir(parents=True, exist_ok=True)
    (base / "attention.md").write_text("# Attention\n", encoding="utf-8")


def write_feature(root: Path, name: str, *, design=False, design_review=False, approved=False, implementation_done=False, review=None, reviewer="subagent", qa=None, acceptance=None) -> None:
    unit = root / ".codestable" / "features" / name
    unit.mkdir(parents=True, exist_ok=True)
    slug = name.removeprefix("2026-07-08-")
    if design:
        status = "approved" if approved else "draft"
        (unit / f"{slug}-design.md").write_text(f"---\nstatus: {status}\n---\n# Design\n", encoding="utf-8")
    if design_review:
        (unit / f"{slug}-design-review.md").write_text("---\nstatus: passed\n---\n# Review\n", encoding="utf-8")
    if implementation_done:
        (unit / f"{slug}-checklist.yaml").write_text("implementation_done: true\n", encoding="utf-8")
    if review:
        (unit / f"{slug}-review.md").write_text(f"---\nstatus: {review}\nreviewer: {reviewer}\n---\n# Code Review\n", encoding="utf-8")
    if qa:
        (unit / f"{slug}-qa.md").write_text(f"---\nstatus: {qa}\n---\n# QA\n", encoding="utf-8")
    if acceptance:
        (unit / f"{slug}-acceptance.md").write_text(f"---\nstatus: {acceptance}\n---\n# Acceptance\n", encoding="utf-8")
