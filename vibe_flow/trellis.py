import json
import subprocess
import sys
from pathlib import Path
from typing import Any

from .paths import relpath


class TrellisAdapter:
    def __init__(self, root: Path):
        self.root = root
        self.trellis = root / ".trellis"

    def status(self) -> dict[str, Any]:
        present = self.trellis.exists()
        out: dict[str, Any] = {
            "present": present,
            "current_task": None,
            "status": "missing" if not present else "no_task",
            "source": None,
            "task_path": None,
            "active_tasks": [],
            "session_ambiguous": False,
            "session_pointer_count": 0,
            "scripts_present": False,
        }
        if not present:
            return out

        task_script = self.trellis / "scripts" / "task.py"
        out["scripts_present"] = task_script.exists()
        out["active_tasks"] = self._active_tasks()
        out["session_pointer_count"] = self._session_pointer_count()
        current = self._current_task(task_script)
        out["session_ambiguous"] = current is None and out["session_pointer_count"] > 1
        if not current:
            return out

        task_path = (self.root / current["path"]).resolve()
        out["current_task"] = task_path.name
        out["task_path"] = relpath(task_path, self.root)
        out["source"] = current.get("source")
        task_json = self._read_task_json(task_path / "task.json")
        out["status"] = str(task_json.get("status") or "planning")
        return out

    def _current_task(self, task_script: Path) -> dict[str, str] | None:
        if not task_script.exists():
            return None
        try:
            proc = subprocess.run(
                [sys.executable, str(task_script), "current", "--source"],
                cwd=self.root,
                text=True,
                encoding="utf-8",
                errors="replace",
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )
        except Exception:
            return None
        path = None
        source = None
        for line in proc.stdout.splitlines():
            clean = line.strip()
            if clean.startswith("Current task:"):
                value = clean.split(":", 1)[1].strip()
                if value and value.lower() != "(none)":
                    path = value
            if clean.startswith("Source:"):
                source = clean.split(":", 1)[1].strip()
        if not path:
            return None
        return {"path": path, "source": source or ""}

    def _active_tasks(self) -> list[dict[str, str]]:
        tasks_root = self.trellis / "tasks"
        if not tasks_root.exists():
            return []
        tasks: list[dict[str, str]] = []
        for task_json in sorted(tasks_root.glob("*/task.json")):
            data = self._read_task_json(task_json)
            status = str(data.get("status") or "")
            if status and status != "completed":
                tasks.append({
                    "name": task_json.parent.name,
                    "status": status,
                    "path": relpath(task_json.parent, self.root) or task_json.parent.name,
                })
        return tasks

    def _read_task_json(self, path: Path) -> dict[str, Any]:
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except Exception:
            return {}

    def _session_pointer_count(self) -> int:
        runtime = self.trellis / ".runtime" / "sessions"
        if not runtime.exists():
            return 0
        return len([p for p in runtime.iterdir() if p.is_file()])
