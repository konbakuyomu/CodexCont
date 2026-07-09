import argparse
import sys
from pathlib import Path

from . import __version__
from .fixtures import create_fixture
from .init_project import init_project
from .jsonio import print_json
from .paths import repo_root
from .planner import BridgePlanner
from .runner import run_next


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    root = repo_root(args.root)
    if args.command == "init":
        payload = {"schema_version": 1, "ok": True, "init": init_project(root)}
        emit(payload, args.json)
        return 0
    if args.command == "doctor":
        payload = BridgePlanner(root).doctor()
        emit(payload, args.json)
        return 0
    if args.command == "status":
        payload = BridgePlanner(root).build("")
        emit(payload, args.json)
        return 0
    if args.command == "next":
        payload = BridgePlanner(root).build(args.intent or "")
        emit(payload, args.json)
        return 0
    if args.command == "run" and args.run_command == "next":
        payload = run_next(root, args.intent or "")
        emit(payload, args.json)
        return 0
    if args.command == "fixture":
        fixture_root = Path(args.path).resolve()
        create_fixture(fixture_root, args.name)
        emit({"schema_version": 1, "ok": True, "fixture": {"name": args.name, "path": str(fixture_root)}}, args.json)
        return 0
    parser.print_help()
    return 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="vibe", description="Trellis + CodeStable bridge CLI")
    parser.add_argument("--version", action="version", version=f"vibe-flow {__version__}")
    parser.add_argument("--root", default=".", help="Project root. Defaults to current directory.")
    sub = parser.add_subparsers(dest="command")

    for name in ("init", "doctor", "status"):
        cmd = sub.add_parser(name)
        cmd.add_argument("--json", action="store_true")

    next_cmd = sub.add_parser("next")
    next_cmd.add_argument("--intent", default="")
    next_cmd.add_argument("--json", action="store_true")

    run_cmd = sub.add_parser("run")
    run_sub = run_cmd.add_subparsers(dest="run_command")
    run_next_cmd = run_sub.add_parser("next")
    run_next_cmd.add_argument("--intent", default="")
    run_next_cmd.add_argument("--json", action="store_true")

    fixture_cmd = sub.add_parser("fixture")
    fixture_cmd.add_argument("name")
    fixture_cmd.add_argument("path")
    fixture_cmd.add_argument("--json", action="store_true")
    return parser


def emit(payload: dict, as_json: bool) -> None:
    if as_json:
        print_json(payload)
        return
    if "next_action" in payload:
        action = payload["next_action"]
        sys.stdout.write(f"{payload.get('mode')}: {action.get('label')} - {action.get('reason')}\n")
    elif "diagnostics" in payload:
        for item in payload["diagnostics"]:
            sys.stdout.write(f"{item.get('level')}: {item.get('code')} {item.get('message')}\n")
    else:
        print_json(payload)
