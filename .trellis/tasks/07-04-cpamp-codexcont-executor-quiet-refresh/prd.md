# CPAMP CodexCont Executor quiet realtime refresh

## Goal

Make the CPAMP `CodexCont Executor` monitor feel calm while staying realtime:
background polling should update data silently, while manual refresh and
foreground resume keep clear, short feedback.

## Requirements

- Fix the current noisy refresh behavior where the top-right live chip flips
  between `同步中` and `实时刷新` on every automatic polling cycle.
- Keep automatic data updates for the admin monitor. Realtime means rows and
  counters update without a full page reload; it does not mean the status label
  must visibly flash every cycle.
- Manual refresh remains meaningful: clicking `刷新` must show a visible
  `同步中 -> 刚刚更新 -> 刷新` style feedback and should take precedence over
  any background refresh in flight.
- Background refresh must not disable the refresh button and must not steal
  visual attention unless it fails.
- Poll quietly while idle, and follow processing rows more quickly so
  `processing` requests still become terminal rows promptly.
- Pause background polling while the page is hidden. On `visibilitychange`,
  `pageshow`, or `focus`, refresh once with visible feedback.
- Keep the public/admin boundary unchanged. Public CPA resource/admin paths must
  remain inaccessible.
- Do not change CPA/CPAMP official APIs or executor admin API response shapes.

## Acceptance Criteria

- [x] CPAMP `CodexCont Executor` no longer alternates `同步中`/`实时刷新` during
      idle background polling.
- [x] Manual `刷新` shows a single clear sync feedback and restores the button
      label after completion.
- [x] Background polling keeps summaries and metrics updated without disabling
      the refresh button.
- [x] If a `processing` row is present, the page schedules a short follow-up
      refresh so terminal state appears promptly.
- [x] Older slow refreshes cannot overwrite newer snapshots.
- [x] Go HTML tests cover the quiet auto-refresh markers and latest-wins/abort
      markers.
- [x] `go test ./... -count=1 -timeout=120s` passes in
      `cpa_codexcont_executor_plugin/go`.
- [x] `git diff --check` passes.

## Notes

- Source evidence: `cpa_codexcont_executor_plugin/go/assets/admin.html` had
  `POLL_MS = 2200` and called `setLive("info", "同步中")` for every
  `refresh(false)` automatic poll, then immediately switched to `实时刷新`.
- Validation:
  - `go test ./... -count=1 -timeout=120s` passed in
    `cpa_codexcont_executor_plugin/go`.
  - `git diff --check` passed.
