# Dashboard Visual Refresh Design

## Visual System

Both dashboards use a shared CPAMP-inspired dark operations aesthetic:

- Dark page background and slightly lighter panels.
- 8px or smaller panel radius, compact spacing, no marketing hero layout.
- Blue primary buttons, green success chips, red failure chips, amber warning
  chips, neutral muted chips.
- Stable card and table dimensions with explicit column widths to prevent
  vertical text compression.
- Top toolbar instead of left navigation.

The pages do not need pixel-perfect parity with CPAMP. They should feel like
they belong in the same operational family.

## CPA Usage Portal

The portal remains a static page served by `cpa_usage_portal`.

Data flow is unchanged:

```text
browser -> cpa-usage-portal -> Key Policy state + CPAMP analytics
```

The main event table changes from raw detail display to a scan-first view:

- visible row fields: time, status, model, latency, tokens, reasoning, cost,
  details action.
- hidden detail row: endpoint, request id, status code, service tier,
  reasoning effort, quota hints, and redacted failure details.
- `failure_brief` is a short human-readable projection intended for detail
  headers and table hints.

Backend projection remains the safety boundary. The frontend formats safe
fields but must not receive secret material.

## CodexCont Dashboard

The CodexCont dashboard keeps the current admin APIs:

- `GET status`
- `GET requests?limit=100`
- `GET logs?limit=200`
- `GET logs/stream`

Only the static HTML/CSS/JS changes. The first screen becomes:

- top toolbar: title, stream status, refresh action.
- metrics row: total requests, protected requests, auto continuations,
  truncation hits, failures.
- recent requests table with protection chips and expandable protection detail.
- advanced logs as a lower-priority collapsible panel.

## Safety And Compatibility

- Existing API fields remain compatible.
- `failure` stays available for compatibility, but bounded and redacted.
- `failure_brief` is additive.
- No new public route is introduced.
- The admin/user split is unchanged:
  `cpa-usage.konbakuyomu.us` for users, `cpa-admin.../codexcont/` for admin.

## Rollback

- If the usage portal page fails, redeploy the previous
  `/opt/codex-stacks/cpa-usage-portal` backup and restart only that container.
- If the CodexCont dashboard fails, redeploy the previous
  `/opt/codex-stacks/codexcont` backup and restart only `codexcont`.
- If public admin route checks fail, revert the touched Caddy route only after
  confirming this task changed Caddy; expected implementation does not change
  Caddy.
