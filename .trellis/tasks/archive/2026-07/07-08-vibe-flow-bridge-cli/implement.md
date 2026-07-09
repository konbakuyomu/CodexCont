# Implementation plan

## Checklist

1. [x] Add `vibe_flow/` Python package with:
   - CLI parser and JSON output helpers.
   - Trellis adapter.
   - CodeStable adapter.
   - Bridge planner.
   - Safe command runner.
   - Fixture generator.
2. [x] Add wrapper entry points:
   - `vibe.py`
   - `vibe`
3. [x] Add tests:
   - offline stage-map tests.
   - CLI contract tests against generated fixtures.
   - init behavior tests in temp repos.
   - run-next refusal / safe-execution tests.
4. [x] Add documentation:
   - `docs/vibe-flow.md`
   - `docs/vibe-flow-live-smoke.md`
5. [x] Run validation:
   - `python -m unittest discover -s tests -p "test_vibe_flow*.py"`
   - `python -m vibe_flow doctor --json`
   - `python -m vibe_flow status --json`
   - `python -m vibe_flow next --intent "继续完成剩下的" --json`

## Final validation

- Standalone package: `npm test` passed 39 tests and package dry-run.
- Repository runtime: `.venv\Scripts\python.exe -m unittest discover -s tests -p "test_*.py"` passed 39 tests.
- Global install: bare `vibe --version` and `vibe-flow --version` report `0.1.6`.
- Obsidian live smoke: current task and CodeStable child resolve correctly,
  historical completed work no longer wins routing, and repeated read-only
  status calls are deterministic without changing the worktree.

## Risk points

- Do not let `run next` execute owner-gated actions.
- Do not guess between multiple Trellis session pointers.
- Do not treat a Markdown report body as more authoritative than frontmatter or
  known machine state.
- Do not mutate `.trellis` or `.codestable` in `status` / `next`.

## Rollback

The feature is additive. If validation fails, remove only the new files added by
this task or fix them in place. Do not revert unrelated dirty files.
