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

## Independent Development Tools

This project uses native Trellis task management and independently enabled Agent Notes. The Vibe Flow bridge and CodeStable development workflow are retired here. Historical CodeStable statuses and gates do not route current work.

- Read relevant current contracts and existing decision Notes before a non-trivial change. Follow the independently installed `write-notes-like-deepseek` skill; do not require a Trellis task solely to read or update a Note.
- Task requirements and execution steps stay in task files; current contracts stay in their owning specs/module docs; alternatives and rationale stay in Notes. Link rather than duplicate.
- A task and a decision have independent lifecycles. Never synchronize their status or archive a Note because a task finished. Use Trellis's existing file-context manifests when a worker needs a particular Note.
- During closeout, run the independent checks documented in `.agents/notes/AGENTS.md` and relevant code tests. State actual coverage and remaining gaps. Mechanical changes need not create a new Note.
- Before moving a referenced file, identify inbound links; update current consumers in the same batch and check links again after the move. Preserve original historical evidence.
