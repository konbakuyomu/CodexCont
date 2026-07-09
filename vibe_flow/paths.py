from pathlib import Path


def repo_root(start: str | Path = ".") -> Path:
    path = Path(start).resolve()
    if path.is_file():
        path = path.parent
    for candidate in [path, *path.parents]:
        if (candidate / ".git").exists() or (candidate / ".trellis").exists() or (candidate / ".codestable").exists():
            return candidate
    return path


def relpath(path: str | Path | None, root: str | Path) -> str | None:
    if path is None:
        return None
    path = Path(path)
    root = Path(root).resolve()
    try:
        return path.resolve().relative_to(root).as_posix()
    except ValueError:
        return str(path)


def strip_date_prefix(name: str) -> str:
    parts = name.split("-")
    if len(parts) > 2 and len(parts[0]) == 2 and len(parts[1]) == 2:
        return "-".join(parts[2:])
    if len(parts) > 3 and len(parts[0]) == 4 and len(parts[1]) == 2 and len(parts[2]) == 2:
        return "-".join(parts[3:])
    return name
