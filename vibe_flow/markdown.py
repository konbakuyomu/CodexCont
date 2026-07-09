from pathlib import Path


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return path.read_text(encoding="utf-8-sig")


def parse_frontmatter(text: str) -> dict[str, str]:
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return {}
    data: dict[str, str] = {}
    for line in lines[1:]:
        if line.strip() == "---":
            break
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        data[key.strip().lower()] = value.strip().strip("'\"").lower()
    return data


def markers(path: Path | None) -> dict[str, str]:
    if not path or not path.exists():
        return {}
    text = read_text(path)
    fm = parse_frontmatter(text)
    lowered = text.lower()
    for key in ("status", "decision", "verdict", "approved", "implementation_done", "implementation", "blocker_signature"):
        if key not in fm:
            value = find_body_marker(lowered, key)
            if value:
                fm[key] = value
    return fm


def find_body_marker(text: str, key: str) -> str | None:
    for sep in (":", "="):
        needle = f"{key}{sep}"
        index = text.find(needle)
        if index < 0:
            continue
        value = text[index + len(needle):].splitlines()[0].strip().strip("'\"")
        if value:
            return value.split()[0].strip(",.;")
    return None


def truthy(value: str | None) -> bool:
    return str(value or "").strip().lower() in {"true", "yes", "y", "1", "approved", "pass", "passed", "done", "complete", "completed"}


def is_pass(value: str | None) -> bool:
    return str(value or "").strip().lower() in {"pass", "passed", "approved", "accepted", "complete", "completed", "done"}


def is_fail(value: str | None) -> bool:
    return str(value or "").strip().lower() in {"fail", "failed", "block", "blocked", "changes-requested", "changes_requested"}
