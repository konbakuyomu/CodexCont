# Vibe Flow Live Smoke Checklist

These checks validate AI behavior with real Codex/model calls. They are manual or
nightly checks, not normal CI.

## Setup

Use a disposable repository or fixture directory. Do not run live smoke on a repo
with unrelated dirty work.

## Scenarios

1. Start a tiny feature and ask Codex:
   ```text
   cs-feat 给订单列表加 CSV 导出
   ```
   Expected: Codex calls `vibe status --json` and routes to
   feature design.

2. Resume from design draft:
   ```text
   cs-feat-design-review
   ```
   Expected: Codex calls `vibe next --intent ... --json` and does
   not implement before the design gate.

3. Resume from approved design:
   ```text
   继续完成剩下的
   ```
   Expected: Codex either proposes Trellis task start or routes to implementation
   according to the JSON state.

4. Goal takeover:
   ```text
   用 cs-goal 接管这个 feature
   ```
   Expected: goal is treated as a wrapper when it references a child feature.

5. Negative gate test:
   ```text
   跳过 QA 直接验收
   ```
   Expected: Codex refuses to skip QA and routes to `cs-feat-qa` if review has
   passed.

6. Generic router with historical completed work present:
   ```text
   cs 允许你进入规划阶段，看看目前怎么走
   ```
   Expected: `next_action.label` is `cs`; an old accepted feature must not
   produce `trellis-check`.

7. Explicit skill plus continue wording:
   ```text
   cs-roadmap 继续完成规划
   ```
   Expected: explicit `cs-roadmap` remains passthrough and wins over the generic
   autopilot keyword.

8. Confirmed Git/Trellis closeout:
   ```text
   确认只提交并归档 vibe-flow 任务
   ```
   Expected: `direct-owner-action / commit_and_archive`; never `cs-feat`.

## Pass criteria

- Five consecutive runs produce the same `next_action.kind` and label for the
  same fixture state.
- No mandatory gate is skipped.
- No files change during `status` or `next`.
- Provider/network failures are reported as live-smoke environment failures, not
  workflow success.
