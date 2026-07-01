# Dashboard Visual Refresh

## Goal

Make the two custom dashboards feel like a polished CPAMP-style operations
surface while keeping the ordinary-user and admin boundaries clear.

The affected pages are:

- `cpa-usage-portal`: ordinary users inspect their own Key Policy key usage.
- `CodexCont` admin dashboard: administrators inspect 516/518n-2 protection
  status and live diagnostics.

## Requirements

- Use a shared dark, dense operations style inspired by CPAMP: dark shell,
  compact top toolbar, status chips, metric cards, segmented controls, and
  scan-friendly tables.
- Do not add a left admin sidebar. The user usage portal must not look like it
  grants access to CPAMP or CPA administration.
- Keep CPA, CPAMP, and CPA Key Policy official artifacts untouched. Only custom
  pages and custom safe projections may change.
- The CPA usage page must remove long failure summaries from the main table.
  Main rows show only time, status, model, latency, tokens, reasoning, cost,
  and an action.
- Long failure details must be available only in an expanded detail row and
  must be short, redacted, and bounded.
- The CodexCont dashboard must keep the recent request protection result as the
  primary first-screen signal.
- Tables must not compress short fields into vertical text on desktop or mobile.
  Long fields may truncate, wrap in detail rows, or move behind expand actions.
- No React/Vue/npm build chain. Keep static HTML/CSS/JS.
- Do not expose additional public routes or weaken current public/admin
  separation.

## Acceptance Criteria

- [x] CPA usage page uses a dark CPAMP-like layout with no side navigation.
- [x] CPA usage page main table has no long summary column and does not render
      response headers or raw failure blobs in the first screen.
- [x] CPA usage events include `failure_brief`; full `failure` remains redacted
      and is shown only in an expanded row.
- [x] CodexCont dashboard uses the same visual language and keeps protection
      states visually distinct.
- [x] Desktop and 390px mobile screenshots show no incoherent overlap or short
      fields rendered vertically.
- [x] `tests/test_cpa_usage_portal.py`, `tests/test_middleware.py`, and
      compile checks pass.
- [x] Server rollout only restarts `cpa-usage-portal` and `codexcont`; CPA,
      CPAMP, and CPA Key Policy remain untouched.
- [x] Public `https://cpa.konbakuyomu.us` still blocks admin/dashboard paths.

## Out Of Scope

- Replacing CPAMP or changing CPAMP source.
- Adding a frontend build system.
- Creating new auth flows, user management, or key migration behavior.
- Changing CodexCont `/v1/responses` folding logic.
