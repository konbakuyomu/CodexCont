<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

<!-- VIBE-FLOW:START -->
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
