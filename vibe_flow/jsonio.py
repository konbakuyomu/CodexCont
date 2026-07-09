import json
import sys
from typing import Any


SCHEMA_VERSION = 1


def print_json(payload: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True))
    sys.stdout.write("\n")


def error_payload(message: str, *, diagnostics: list[dict[str, Any]] | None = None) -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "ok": False,
        "error": message,
        "diagnostics": diagnostics or [],
    }
