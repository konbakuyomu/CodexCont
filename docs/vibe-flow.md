# Vibe Flow Bridge

`vibe-flow` is a small Trellis + CodeStable bridge CLI. It helps an AI agent
recover workflow state from project files before choosing a next action.

## Commands

```bash
vibe init
vibe doctor --json
vibe status --json
vibe next --intent "继续完成剩下的" --json
vibe run next --json
```

`status` and `next` are read-only. `run next` only executes commands marked safe
by the planner and allowlisted by the runner.

## Modes

- `codestable_passthrough`: explicit `cs` or `cs-*` intent; keep CodeStable's
  original stage behavior. An explicit skill wins over generic words such as
  "continue" in the same request.
- `bridge_autopilot`: continue / complete / autonomous intent; compute the next
  workflow stage from disk.
- `trellis_first`: Trellis exists but no CodeStable unit is selected.

## Agent contract

Agents should call:

```bash
vibe status --json
```

before deciding the current workflow stage, and:

```bash
vibe next --intent "<user request>" --json
```

before continuing or autonomously advancing work.

When the user invokes any `cs-*` CodeStable workflow, agents should still run
both checks first. The returned `next_action.label` is the routing signal, while
explicit `cs-*` requests remain passthrough and must not auto-advance to later
gates. Hard blockers still win: for example, a self-reviewed implementation
cannot proceed to acceptance until an independent review is rerun or the owner
explicitly accepts the downgrade.

Do not run `vibe run next --json` unless the user explicitly asks
the agent to execute the proposed next action.

Explicit owner confirmations for Git/Trellis lifecycle work are not new
CodeStable stages. For example, `确认只提交并归档 vibe-flow 任务` returns
`next_action.label = direct-owner-action` with
`requested_action = commit_and_archive`; the agent follows that confirmed
request instead of inventing `cs-feat`.

## Current-work selection

`vibe-flow` treats a resolved Trellis current task as the current workflow
boundary:

- exact Trellis task slug matches a CodeStable unit -> select that unit
- current Trellis task has no matching CodeStable unit yet -> select nothing
  and route from the user's explicit request
- no current Trellis task and several active CodeStable units -> ask which unit
  to use instead of guessing
- completed goals never pull an old accepted child feature back into the
  current flow

For units without `state.yaml`, canonical reports are used conservatively:
roadmap status comes from `{slug}-roadmap.md`, and an issue is complete only
when it has both a fix note and a passed independent review. A self-reviewed
issue remains active for review repair.

The JSON field `codestable.selection.status` explains which rule selected or
rejected a unit. Multiple historical Trellis session files are not an ambiguity
when `task.py current --source` successfully resolves the current session; the
raw count is exposed as `trellis.session_pointer_count` for diagnostics.

## Trellis compatibility patch

`vibe init` also applies a narrow Trellis bridge patch when a project already
has Trellis Codex runtime prompts:

- `.trellis/workflow.md`
- `.codex/hooks/inject-workflow-state.py`

The patch keeps Trellis inline mode intact, but changes the broad
`do not dispatch implement/check sub-agents` wording so it only applies to
Trellis implement/check sub-agents. It does not forbid independent reviewers
required by CodeStable gates.

`vibe doctor --json` reports `trellis_workflow_bridge_missing` or
`codex_hook_bridge_missing` when those old broad prompts are still present.

## Feature flow

The canonical CodeStable feature flow maps as follows:

```text
not_started          -> cs-feat-design
design_draft         -> cs-feat-design-review
design_review_passed -> owner approval
approved             -> cs-feat-impl or Trellis task start
implementation_done  -> cs-code-review
review_failed        -> cs-feat-impl review_fix
review_passed        -> cs-feat-qa
qa_failed            -> cs-feat-impl qa_fix
qa_passed            -> cs-feat-accept
accepted             -> final Trellis / CodeStable finish checks
```

If the review report is `status: passed` but `reviewer` is not `subagent` or
`subagent+ocr`, `vibe-flow` reports
`next_action.label = implementation-review-needs-task-agent` instead of routing
to QA or acceptance.

## Trellis closeout and external tool repos

Trellis archive and journal commands operate in the Trellis project repository.
When a CodeStable feature implements code in another folder, record that folder
in the feature's `worktree-override.md` using a Windows absolute path, such as:

```markdown
- CLI code lives at `D:\Dev\20_Software\21_Mine\some-tool`.
```

After the feature reaches `accepted`, `vibe-flow` first checks that the
CodeStable feature evidence itself is committed. If the feature folder is still
dirty in the Trellis project Git repo, the planner returns
`codestable-evidence-commit-needed`.

It also checks external paths before allowing the final Trellis closeout route:

- missing path -> `external-worktree-missing`
- path is not a Git repository -> `external-worktree-git-required`
- repository has uncommitted changes -> `external-worktree-commit-needed`
- clean repository -> continue to `trellis-check`

`vibe-flow` never initializes Git or commits automatically. It only stops the
workflow and asks the owner to either settle the external repository or record an
explicit no-git/no-commit exception before Trellis archive and journal.
