# Implementation Plan

1. Read relevant Trellis specs before editing:
   - `.trellis/spec/backend/index.md`
   - `.trellis/spec/backend/codex-continuation-contracts.md`
   - `.trellis/spec/guides/index.md`
2. Start the task with `task.py start`.
3. Update `cpa_codexcont_executor_plugin/go/assets/admin.html`:
   - replace fixed interval polling with quiet timeout scheduling;
   - add refresh button state helpers;
   - add `AbortController` and sequence latest-wins protection;
   - keep processing rows on a shorter follow-up loop.
4. Update executor Go HTML tests:
   - assert quiet background polling markers;
   - assert no direct `refresh(false)` path forces `同步中`;
   - assert abort/latest-wins helpers exist.
5. Update `.trellis/spec/backend/codex-continuation-contracts.md` with the
   quiet background refresh contract.
6. Validate:
   - `go test ./... -count=1 -timeout=120s` in
     `cpa_codexcont_executor_plugin/go`;
   - `git diff --check`.
7. Commit code/spec/task artifacts, then archive the Trellis task and record a
   journal entry.
