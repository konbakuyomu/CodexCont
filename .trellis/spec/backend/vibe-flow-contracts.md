# Vibe Flow Contracts

## Scenario: CodeStable Review Gate Feasibility

### 1. Scope / Trigger

- Trigger: `vibe-flow` reads CodeStable feature/goal state and returns `status`,
  `doctor`, or `next` JSON.
- Applies when a feature has implementation review, QA, acceptance, or a
  wrapping `cs-goal`.
- Goal: prevent agents from treating a local/self review as a formal CodeStable
  implementation review.

### 2. Signatures

- CLI status: `vibe status --json`
- CLI planner: `vibe next --intent "<user text>" --json`
- Python planner: `BridgePlanner(root).build(intent)`
- CodeStable adapter: `CodeStableAdapter(root).status(trellis=None)`

### 3. Contracts

- Feature candidates may include:
  - `gates.implementation_review.status`
  - `gates.implementation_review.reviewer`
  - `gates.implementation_review.review_path`
  - `blocked`
  - `blocker`
- Top-level CodeStable state may include:
  - `blocked`
  - `blockers`
  - `wrapper_unit`
  - `child_unit`
- Formal implementation review passes only when `{slug}-review.md` has
  `status: passed` and `reviewer` is `subagent` or `subagent+ocr`.
- If review is passed but reviewer is missing, `self`, or any non-Task-agent
  value, `implementation_review.status` must be `requires_task_agent`.
- A blocked goal wrapping a child feature must be surfaced in `wrapper_unit` and
  `blockers`; child feature stage must not hide the parent blocker.

### 4. Validation & Error Matrix

- Implementation done and review report missing -> stage remains
  `implementation_done`; next action is `cs-code-review`.
- Review `status: failed` or `changes-requested` -> stage is `review_failed`;
  next action is `cs-feat-impl` with review-fix mode.
- Review `status: passed`, `reviewer: subagent` -> review gate is `passed`.
- Review `status: passed`, `reviewer: subagent+ocr` -> review gate is `passed`.
- Review `status: passed`, `reviewer: self` -> `blocked=true`,
  blocker code `implementation_review_requires_task_agent`, next action
  `ask_owner`.
- Goal `state.yaml` has `status: blocked` and blocker signature
  `implementation_review_requires_task_agent` -> same `ask_owner` action even
  if child feature stage is `qa_passed`.

### 5. Good/Base/Bad Cases

- Good: user says `继续完成剩下的`, feature is `qa_passed`, review is
  `reviewer: self` -> planner returns
  `next_action.label = implementation-review-needs-task-agent`.
- Good: user says `cs-feat-accept` in the same state -> planner still returns
  `implementation-review-needs-task-agent` and records
  `requested_label = cs-feat-accept`.
- Good: user says `cs-code-review` in the same state -> planner returns
  `invoke_skill / cs-code-review`, because that is the repair path.
- Base: review is `reviewer: subagent` and QA passed -> planner may route to
  `cs-feat-accept`.
- Bad: QA passed and self-review exists -> planner routes directly to
  `cs-feat-accept`.

### 6. Tests Required

- Unit fixture with `reviewer: self` and QA passed asserts:
  - feature stage remains `qa_passed`;
  - implementation review gate is `requires_task_agent`;
  - next action is `ask_owner / implementation-review-needs-task-agent`.
- Explicit `cs-feat-accept` fixture with same state asserts no bypass.
- Explicit `cs-code-review` fixture with same state asserts repair passthrough.
- Goal wrapper fixture with blocked `state.yaml` asserts wrapper blocker wins
  over child feature `qa_passed`.
- Live smoke against a real repository should verify `vibe next` is read-only
  and does not mutate CodeStable reports.

### 7. Wrong vs Correct

#### Wrong

```text
qa_passed + self review -> cs-feat-accept
```

This skips CodeStable's independent implementation review gate.

#### Correct

```text
qa_passed + self review -> ask_owner / implementation-review-needs-task-agent
```

The owner can either rerun `cs-code-review` in an environment with an
independent reviewer or explicitly accept local-only downgrade before continuing.

## Scenario: Trellis Closeout With External Implementation Repo

### 1. Scope / Trigger

- Trigger: a CodeStable feature is `accepted` and `vibe-flow` is deciding the
  next action before Trellis archive/journal closeout.
- Applies when a feature has `worktree-override.md` that points to an external
  implementation folder, such as a sibling tool repository outside the Trellis
  project root.
- Goal: prevent Trellis closeout from archiving a task while the actual
  implementation repository is missing, not initialized with Git, or dirty.

### 2. Signatures

- CLI status: `vibe status --json`
- CLI planner: `vibe next --intent "<user text>" --json`
- Python adapter: `CodeStableAdapter(root).status(trellis)`
- Python planner: `BridgePlanner(root).build(intent)`
- External repo result: `codestable.external_worktrees[]`

### 3. Contracts

- `vibe-flow` may read Windows absolute paths from
  `.codestable/features/<feature>/worktree-override.md`.
- For each external path, `codestable.external_worktrees[]` should report:
  - `path`
  - `present`
  - `is_git_repo`
  - `is_clean`
  - `branch`
  - `recent_commit`
- After feature acceptance, dirty CodeStable feature evidence is a hard
  blocker:
  - dirty `.codestable/features/<feature>` path -> blocker code
    `codestable_unit_dirty`
- After feature acceptance, external worktree problems are also hard blockers:
  - missing path -> blocker code `external_worktree_missing`
  - non-Git path -> blocker code `external_worktree_not_git`
  - dirty Git repo -> blocker code `external_worktree_dirty`
- `vibe-flow` must not run `git init`, `git add`, `git commit`, archive, or
  journal commands automatically for these blockers.
- A clean external Git repo allows the normal accepted-feature route:
  `next_action.label = trellis-check`.

### 4. Validation & Error Matrix

- Accepted feature evidence is dirty -> `codestable_unit_dirty`.
- External implementation path is missing -> `external_worktree_missing`.
- External implementation path is not a Git repo ->
  `external_worktree_not_git`.
- External implementation Git repo is dirty -> `external_worktree_dirty`.
- Feature evidence and every external repo are clean -> allow `trellis-check`.

### 5. Good/Base/Bad Cases

- Good: accepted feature points to a clean external Git repo -> planner routes
  to `trellis-check`.
- Good: accepted feature has uncommitted CodeStable acceptance/report evidence
  -> planner returns `ask_owner / codestable-evidence-commit-needed`.
- Good: accepted feature points to an existing non-Git folder -> planner returns
  `ask_owner / external-worktree-git-required`.
- Good: accepted feature points to a dirty Git repo -> planner returns
  `ask_owner / external-worktree-commit-needed`.
- Base: feature has no external worktree override -> accepted feature behaves as
  before and may route to Trellis finish checks.
- Bad: accepted feature points to dirty external code but planner still routes
  to Trellis archive/journal.

### 6. Tests Required

- Accepted feature + external non-Git folder asserts:
  `next_action.label = external-worktree-git-required`.
- Accepted feature + dirty CodeStable feature folder asserts:
  `next_action.label = codestable-evidence-commit-needed`.
- Accepted feature + dirty external Git repo asserts:
  `next_action.label = external-worktree-commit-needed`.
- Accepted feature + clean external Git repo asserts:
  `next_action.label = trellis-check`.
- `vibe next` remains read-only in all three cases.

### 7. Wrong vs Correct

#### Wrong

```text
accepted feature + dirty external implementation repo -> archive Trellis task
```

#### Correct

```text
accepted feature + dirty external implementation repo -> ask owner to commit or record an exception
```

## Scenario: Trellis Codex Inline Prompt Bridge

### 1. Scope / Trigger

- Trigger: `vibe init` runs in a project that already has Trellis Codex runtime
  prompts.
- Applies to `.trellis/workflow.md` and
  `.codex/hooks/inject-workflow-state.py`.
- Goal: keep Trellis inline mode behavior, but prevent the broad
  "do not dispatch implement/check sub-agents" text from blocking CodeStable's
  independent implementation review gate.

### 2. Contracts

- `vibe init` may patch only the known legacy Trellis prompt fragments:
  - `Do not dispatch implement/check sub-agents in inline mode.`
  - `do not dispatch implement/check sub-agents.`
- The replacement must scope the rule to Trellis implement/check sub-agents and
  explicitly state that CodeStable independent reviewers are still allowed.
- The patch must be idempotent. Running `vibe init` twice must not duplicate the
  bridge text.
- `vibe doctor --json` must stay read-only and report:
  - `trellis_workflow_bridge_missing` when `.trellis/workflow.md` still has the
    broad legacy rule.
  - `codex_hook_bridge_missing` when the Codex hook still injects the broad
    legacy rule.
- Missing Trellis or missing Codex hook files are not bridge errors; they are
  partial-install/degraded setup signals handled by normal diagnostics.

### 3. Good/Base/Bad Cases

- Good: Trellis workflow and Codex hook contain the legacy broad rule -> `vibe
  init` patches both and reports `patched`.
- Good: both files are already patched -> `vibe init` reports
  `already_patched` and leaves the text unchanged.
- Base: project has no Trellis -> `vibe init` creates `.vibe-flow` and AGENTS
  instructions, while bridge patch results are `missing`.
- Bad: `vibe doctor` stays silent while a Trellis Codex hook still injects the
  broad no-sub-agent rule.

### 4. Tests Required

- Init fixture with legacy workflow/hook asserts both files are patched.
- Repeated init asserts the patched text appears once.
- Doctor fixture with legacy workflow/hook asserts both bridge warning codes.
- Doctor after init asserts both bridge warning codes disappear.

## Scenario: Current Task Routing Precedence

### 1. Scope / Trigger

- Trigger: `vibe status` or `vibe next` sees historical CodeStable units while
  the current Trellis task belongs to newer work.
- Goal: prevent completed goals, accepted features, or unrelated active issues
  from replacing the current task's workflow.

### 2. Signatures

- CLI status: `vibe status --json`
- CLI planner: `vibe next --intent "<user text>" --json`
- Python adapter: `CodeStableAdapter(root).status(trellis)`
- Python planner: `BridgePlanner(root).build(intent)`
- Selection result: `codestable.selection.status`

### 3. Contracts

- An explicit `cs` or `cs-*` request uses `codestable_passthrough` even when the
  same text contains a generic autopilot word such as `continue` or `继续`.
- The phrase `cs-goal` plus an explicit takeover expression such as `接管`
  remains `bridge_autopilot` so the wrapper/child flow can resume.
- A resolved Trellis current task is an ownership boundary:
  - one matching CodeStable slug -> select it;
  - no matching slug -> return no primary unit and
    `codestable.selection.status = trellis_current_task_unmatched`;
  - multiple matching slugs -> report ambiguity instead of guessing.
- Only active goal wrappers may participate in goal-child fallback. A complete
  goal must never resurrect its accepted child feature as current work.
- An active goal wrapper may collapse with its own child only when there are no
  unrelated active CodeStable units.
- When `state.yaml` is absent, roadmap stage comes from the canonical
  `{slug}-roadmap.md` frontmatter.
- When `state.yaml` is absent, an issue becomes `completed` only when a
  `*-fix-note.md` exists and `*-review.md` is `passed` by `subagent` or
  `subagent+ocr`. Self-reviewed issue evidence remains active for repair.
- Multiple session pointer files are diagnostic history, not ambiguity, when
  `task.py current --source` successfully resolves the current session.
- A confirmed Git/Trellis lifecycle request is outside CodeStable stage
  inference. It returns `stop / direct-owner-action` with one of
  `commit`, `archive`, or `commit_and_archive`; it must not become `cs-feat`.

### 4. Validation & Error Matrix

- Current Trellis slug has no CodeStable match -> no primary unit;
  `trellis_current_task_unmatched`.
- Current Trellis slug has more than one CodeStable match -> no primary unit;
  `trellis_task_match_ambiguous`.
- No current task and more than one unrelated active unit ->
  `active_units_ambiguous`; ask the owner to choose.
- Complete goal plus accepted child -> neither participates in active fallback.
- Issue fix note plus passed `reviewer: self` review ->
  `review_requires_task_agent`, not `completed`.
- Intent containing `Foo.cs` -> not an explicit CodeStable command.
- Confirmed text containing both commit and archive ->
  `requested_action = commit_and_archive`; no CodeStable label is inferred.

### 5. Good/Base/Bad Cases

- Good: current Trellis task has no CodeStable unit yet, while an old completed
  goal wraps an accepted feature -> no primary unit; route from current intent.
- Good: `cs-roadmap 继续完成规划` -> passthrough to `cs-roadmap`.
- Good: generic `cs` plugin mention -> passthrough to `cs`.
- Good: twelve session files plus one successfully resolved current task ->
  `session_ambiguous = false` and `session_pointer_count = 12`.
- Good: `确认只提交并归档 vibe-flow 任务` -> `direct-owner-action`.
- Bad: old accepted feature returns `trellis-check` for a newly created Trellis
  task.
- Bad: `继续` in an explicit `cs-roadmap` request changes the mode to autopilot.

### 6. Tests Required

- Fixture with unmatched current Trellis task, complete old goal, accepted old
  feature, and active old issue asserts neither `trellis-check` nor `cs-issue`.
- Generic `cs` plugin mention asserts passthrough label `cs`.
- Explicit `cs-roadmap` plus `继续` asserts passthrough label `cs-roadmap`.
- Active goal wrapper plus unrelated active issue asserts ambiguity.
- Completed issue reports plus active roadmap assert the roadmap is selected.
- Self-reviewed issue reports assert the issue is not marked completed.
- A C# filename such as `Foo.cs` must not be parsed as generic `cs` intent.
- Confirmed commit/archive asserts `direct-owner-action / commit_and_archive`.
- An unconfirmed ordinary use of `提交` must not be treated as owner approval.
- Resolved current task with multiple pointer files asserts no session
  ambiguity.

### 7. Wrong vs Correct

#### Wrong

```text
new unmatched Trellis task + old complete goal -> old accepted child -> trellis-check
explicit cs-roadmap + continue keyword -> bridge_autopilot
confirmed commit/archive -> cs-feat
```

#### Correct

```text
new unmatched Trellis task -> no primary unit -> route from current intent
explicit cs-roadmap + continue keyword -> codestable_passthrough / cs-roadmap
confirmed commit/archive -> direct-owner-action / commit_and_archive
```
