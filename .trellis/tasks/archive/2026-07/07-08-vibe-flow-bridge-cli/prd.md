# Trellis CodeStable bridge CLI

## Goal

Build a small, independent `vibe-flow` CLI that lets AI coding agents recover the
current Trellis + CodeStable workflow state from disk instead of relying on
remembered prompts or long always-loaded skills.

The first release is a local, repo-agnostic bridge for status detection and next
action planning. It must be safe by default: `status` and `next` are read-only,
and any mutating behavior is limited to an explicit `run next` command with a
small allowlist.

## Requirements

- Provide these user-facing commands:
  - `vibe init`
  - `vibe doctor --json`
  - `vibe status --json`
  - `vibe next --intent "<user text>" --json`
  - `vibe run next --json`
- Detect Trellis presence, current task, task status, and ambiguity without
  guessing between multiple session pointers.
- Detect CodeStable presence, active feature / issue / refactor / goal /
  roadmap units, and canonical feature stages from files under `.codestable/`.
- Preserve old CodeStable habits: explicit `cs-*` intents run in
  `codestable_passthrough` mode and do not auto-advance beyond the requested
  stage.
- Support bridge autopilot semantics for intents such as "continue",
  "complete the rest", "autonomously finish", and "use cs-goal to take over".
- Treat `cs-goal` as a wrapper when it references an existing feature / issue /
  refactor child unit; the execution gate belongs to the child unit.
- Provide deterministic JSON output with `schema_version`, `mode`, `trellis`,
  `codestable`, and `next_action`.
- Add fixture-based tests covering the canonical feature flow and non-linear
  `design-review -> cs-goal` recovery.
- Add documentation for offline tests and live Codex smoke tests.
- Do not require network, model credentials, Trellis package changes, or
  CodeStable package changes.

## Acceptance Criteria

- [x] `python -m vibe_flow status --json` works in this repository and in temp
      fixture repositories.
- [x] `python -m vibe_flow next --intent "cs-feat-design" --json` returns
      `codestable_passthrough` and does not propose unrelated auto-advance.
- [x] `python -m vibe_flow next --intent "继续完成剩下的" --json` can move through
      the feature stage map until the next owner gate.
- [x] `doctor --json` reports missing Trellis / CodeStable surfaces as warnings
      without mutating the repository.
- [x] `init` creates only `.vibe-flow/config.yaml` and a managed `AGENTS.md`
      block.
- [x] `run next` refuses owner-gated, destructive, commit, merge, push, deploy,
      or secret-related actions.
- [x] Offline tests pass without network or model credentials.
- [x] The manual live-smoke checklist documents how to verify Codex AI calling
      behavior without making it part of normal CI.

## Notes

- First implementation lives in this repository as a Python standard-library
  package plus wrapper scripts. It remains conceptually independent so it can be
  extracted later.
- Existing dirty files in the repository are unrelated and must not be reverted
  or swept into cleanup.
