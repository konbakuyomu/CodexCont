# Align Plus CodexCont detail fields

## Goal

Align the `cpa-usage.konbakuyomu.us` current-key CodexCont protection detail
view with the CPAMP `CodexCont Executor` admin detail view without expanding
the user page trust boundary.

## Requirements

- `/user/api/codexcont` must continue to return only the current logged-in
  key's safe summaries.
- Plus must expose safe executor summary fields already needed by the user UI:
  `final_status`, `first_truncation_n`, `folded`, `passthrough`, and
  `failure_detail`.
- The user detail view must render per-round summaries from `rounds[]`, using
  the same readable shape as the executor admin page:
  `#<round> reasoning <tokens> / <decision>`.
- The user detail view must show current safe key display data and safe
  preview, plus start/update/end timestamps so admin and user views no longer
  appear to disagree by request duration.
- The user table time column should match the executor admin view by preferring
  `updated_at || started_at`.
- The page must not expose raw keys, full hashes, Authorization headers, OAuth
  tokens, cookies, request bodies, response bodies, or encrypted reasoning.

## Acceptance Criteria

- [x] The known `resp_09674e...` style auto-continued record shows all rounds
      in `cpa-usage`, including three `516 / continue` rounds and the final
      clean round.
- [x] `final_status` and `first_truncation_n` no longer display as `-` when
      present in the executor summary.
- [x] Other-key and unknown-identity records remain filtered out.
- [x] Go tests cover safe field projection and user page rendering hooks.
- [x] Production validation confirms `cpa-usage` detail parity while public
      plugin/admin paths remain `404`.

## Notes

- Evidence: Executor SQLite and Plus API both contained `rounds_count=4` for
  `resp_09674e...`; the primary missing piece is user-page rendering. Plus API
  was missing only `final_status` and `first_truncation_n` among the inspected
  safe fields.
- Out of scope: quota, key policy, executor continuation logic, and all-key
  admin monitoring behavior.
- Local validation:
  - `go test ./... -count=1 -timeout=120s` passed in
    `cpa_key_policy_plus_plugin/go`.
  - `git diff --check` passed.
- Production validation:
  - Deployed `cpa-key-policy-plus.so`
    `a916d4a251ea89a13d08199337ee53fd937fb76064bf6cdeb3077234d7dde43e`.
  - Remote backup:
    `/opt/codex-stacks/backups/plus-codexcont-detail-alignment-20260704-103927`.
  - `cpa-usage` HTML contains `roundSummaryText`, `无轮次摘要`, `更新时间`,
    and the `轮次` detail card.
  - Temporary current-key session for `kuma的官key` showed
    `final_status=completed`, `first_truncation_n=1`, `rounds_count=4`, and
    rounds `516/continue`, `516/continue`, `516/continue`, `94/clean`.
  - Public `cpa.konbakuyomu.us/v0/resource/plugins/.../admin` returned `404`
    for Plus and Executor admin paths.
