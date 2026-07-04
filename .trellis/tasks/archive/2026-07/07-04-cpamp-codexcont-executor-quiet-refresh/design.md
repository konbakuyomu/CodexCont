# Design: Quiet Realtime Refresh

## Boundary

This task only changes the executor CPAMP admin monitor frontend and its tests.
The executor `/admin/api/status` and `/admin/api/summaries` contracts stay
unchanged. `cpa-usage` and retired Governor pages are not implementation
targets.

## Refresh Model

- Split connection state from refresh-button state.
- `setLive(kind, text)` remains the live status chip API.
- Add button-state helpers for manual refresh feedback.
- `refresh(force)` becomes a latest-wins snapshot loader:
  - `force=true`: user-visible refresh, abort or supersede any older request,
    disable the button, show `同步中`, then `刚刚更新` or `同步失败`.
  - `force=false`: quiet background refresh, do not change the live chip to
    `同步中`, do not disable the button, and only show an error if the latest
    background request fails.
- Use `AbortController` plus a sequence token so slow older responses cannot
  overwrite newer snapshots.

## Scheduling

- Replace the fixed 2.2s interval with timeout-based scheduling.
- Idle polling interval: 5000 ms.
- Processing follow-up interval: 1800 ms when any visible summary has
  `protection === "processing"`.
- Do not schedule while `document.hidden`.
- On `visibilitychange`, `pageshow`, or `focus`, run one visible refresh and
  reschedule.

## UI Text

- Live chip steady states:
  - initial: `连接中`
  - success: `实时已连接`
  - background error: `连接异常`
  - manual sync: `正在同步`
- Refresh button:
  - idle: `刷新`
  - manual in progress: `同步中`
  - manual success: `刚刚更新`
  - manual error: `同步失败`

## Safety

The page still renders only safe summaries already returned by executor APIs.
No raw keys, full hashes, request/response bodies, OAuth tokens, cookies, or
encrypted reasoning are added.
