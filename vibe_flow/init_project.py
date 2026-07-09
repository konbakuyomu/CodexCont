from pathlib import Path
from typing import Any

from .trellis_bridge import apply_trellis_bridge


CONFIG_TEXT = """# vibe-flow bridge configuration
schema_version: 1
default_mode: status-first
allow_run_next: true
"""

AGENTS_BLOCK = """<!-- VIBE-FLOW:START -->
# Vibe Flow Bridge

This project uses `vibe-flow` as the Trellis + CodeStable bridge.

When the user invokes `cs` or any `cs-*` CodeStable workflow, first run the status and
next-action checks below. Use `next_action.label` as the routing signal; explicit
`cs` / `cs-*` requests remain passthrough and must not auto-advance to later
gates, even if the same request also contains words such as "continue".

Before deciding the workflow stage, run:

```bash
vibe status --json
```

Before continuing or autonomously advancing work, run:

```bash
vibe next --intent "<user request>" --json
```

Explicit `cs` / `cs-*` requests preserve CodeStable's original stage semantics. Do not
auto-run `vibe run next --json` unless the user explicitly asks to
execute the proposed next action.

When `next_action.label` is `direct-owner-action`, follow the owner's explicit
Git/Trellis lifecycle request and do not invent a `cs-*` stage.
<!-- VIBE-FLOW:END -->
"""


def init_project(root: Path) -> dict[str, Any]:
    vibe_dir = root / ".vibe-flow"
    vibe_dir.mkdir(exist_ok=True)
    config = vibe_dir / "config.yaml"
    created: list[str] = []
    updated: list[str] = []
    if not config.exists():
        config.write_text(CONFIG_TEXT, encoding="utf-8")
        created.append(".vibe-flow/config.yaml")
    agents = root / "AGENTS.md"
    if agents.exists():
        text = agents.read_text(encoding="utf-8")
    else:
        text = ""
    next_text = upsert_managed_block(text, AGENTS_BLOCK)
    if next_text != text:
        agents.write_text(next_text, encoding="utf-8")
        (updated if text else created).append("AGENTS.md")
    return {"created": created, "updated": updated, "trellis_bridge": apply_trellis_bridge(root)}


def upsert_managed_block(text: str, block: str) -> str:
    start = "<!-- VIBE-FLOW:START -->"
    end = "<!-- VIBE-FLOW:END -->"
    if start in text and end in text:
        before = text[:text.index(start)].rstrip()
        after = text[text.index(end) + len(end):].lstrip()
        return join_parts(before, block, after)
    return join_parts(text.rstrip(), block, "")


def join_parts(*parts: str) -> str:
    content = "\n\n".join(part for part in parts if part)
    return content.rstrip() + "\n"
