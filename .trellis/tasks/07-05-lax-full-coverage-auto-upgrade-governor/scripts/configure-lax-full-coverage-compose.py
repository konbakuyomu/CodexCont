#!/usr/bin/env python3
from __future__ import annotations

import copy
import datetime as dt
from pathlib import Path
from typing import Any, Dict, Iterable, List

import yaml


STAMP = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
SUFFIX = f".bak-lax-fullcov-{STAMP}"


def label_key(item: str) -> str:
    return item.split("=", 1)[0]


def normalize_labels(labels: Any) -> List[str]:
    if labels is None:
        return []
    if isinstance(labels, dict):
        return [f"{k}={v}" for k, v in labels.items()]
    if isinstance(labels, list):
        return [str(item) for item in labels]
    return []


def set_labels(service: Dict[str, Any], labels: Iterable[str]) -> None:
    existing = normalize_labels(service.get("labels"))
    incoming = [str(item) for item in labels]
    incoming_keys = {label_key(item) for item in incoming}
    kept = [item for item in existing if label_key(item) not in incoming_keys]
    service["labels"] = kept + incoming


def update_service(compose: Dict[str, Any], service_name: str, patch: Dict[str, Any]) -> None:
    services = compose.setdefault("services", {})
    if service_name not in services:
        raise KeyError(f"service not found: {service_name}")
    service = services[service_name]
    if "image" in patch:
        service["image"] = patch["image"]
    if "labels" in patch:
        set_labels(service, patch["labels"])


def backup(path: Path) -> None:
    backup_path = Path(str(path) + SUFFIX)
    backup_path.write_bytes(path.read_bytes())
    print(f"backup {backup_path}")


COMMON = [
    "autoupgrade.strategy=rollback-tag-only",
    "autoupgrade.min-disk-gb=20",
]

APP = [
    "diun.enable=true",
    "autoupgrade.enable=true",
    "autoupgrade.group=lax-apps",
    "autoupgrade.adapter=app",
    *COMMON,
]

DATA_POSTGRES = [
    "diun.enable=true",
    "autoupgrade.enable=true",
    "autoupgrade.group=lax-data",
    "autoupgrade.adapter=postgres-same-major",
    "autoupgrade.healthcheck-sec=60",
    *COMMON,
]

DATA_REDIS = [
    "diun.enable=true",
    "autoupgrade.enable=true",
    "autoupgrade.group=lax-data",
    "autoupgrade.adapter=redis-same-major",
    "autoupgrade.healthcheck-sec=60",
    *COMMON,
]


PATCHES: Dict[str, Dict[str, Dict[str, Any]]] = {
    "/opt/frontier/apps/codemerge/docker-compose.yml": {
        "codemerge": {"labels": APP},
    },
    "/opt/frontier/apps/fast-note-sync-service/docker-compose.yaml": {
        "fast-note-sync-service": {"labels": APP},
    },
    "/opt/frontier/apps/grok2api-jiujiu/docker-compose.yml": {
        "grok2api-jiujiu": {"labels": APP + ["autoupgrade.healthcheck-sec=60"]},
    },
    "/opt/frontier/apps/prompt-manager/docker-compose.yml": {
        "prompt-manager": {"labels": APP},
    },
    "/opt/frontier/apps/new-api/docker-compose.yml": {
        "new-api": {"labels": APP},
        "postgres": {"image": "postgres:17", "labels": DATA_POSTGRES},
        "redis": {"labels": DATA_REDIS},
    },
    "/opt/frontier/apps/sub2api/docker-compose.yml": {
        "sub2api": {"labels": APP + ["autoupgrade.healthcheck-sec=60"]},
        "postgres": {"labels": DATA_POSTGRES},
        "redis": {"labels": DATA_REDIS},
        "sub2api-egress-router": {
            "image": "metacubex/mihomo:latest",
            "labels": [
                "diun.enable=true",
                "autoupgrade.enable=true",
                "autoupgrade.group=lax-network",
                "autoupgrade.adapter=mihomo-config",
                "autoupgrade.healthcheck-sec=30",
                *COMMON,
            ],
        },
    },
    "/opt/frontier/apps/openwebui/docker-compose.yml": {
        "app": {
            "labels": [
                "diun.enable=true",
                "autoupgrade.enable=true",
                "autoupgrade.group=lax-platform",
                "autoupgrade.adapter=openwebui-assets",
                "autoupgrade.healthcheck-sec=180",
                "autoupgrade.health-url=http://127.0.0.1:3986/health",
                *COMMON,
            ],
        },
        "postgres": {"labels": DATA_POSTGRES},
        "redis": {"image": "redis:7", "labels": DATA_REDIS},
    },
    "/opt/frontier/apps/diun/docker-compose.yml": {
        "diun": {
            "labels": [
                "diun.enable=true",
                "autoupgrade.enable=true",
                "autoupgrade.group=lax-observability",
                "autoupgrade.adapter=running-only",
                "autoupgrade.healthcheck-sec=30",
                *COMMON,
            ],
        },
    },
}


def main() -> int:
    for raw_path, service_patches in PATCHES.items():
        path = Path(raw_path)
        if not path.exists():
            raise FileNotFoundError(raw_path)
        original = yaml.safe_load(path.read_text(encoding="utf-8"))
        updated = copy.deepcopy(original)
        for service_name, patch in service_patches.items():
            update_service(updated, service_name, patch)
            print(f"updated {raw_path}:{service_name}")
        if updated != original:
            backup(path)
            path.write_text(yaml.safe_dump(updated, allow_unicode=True, sort_keys=False), encoding="utf-8")
        else:
            print(f"no-change {raw_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
