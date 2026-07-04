# CPA Key Policy+ Admin Slim UX Design

## UI Contract

The admin page stays a single embedded HTML/JS/CSS asset. It continues to read
`/keys` and `/models`, keeps all edits client-side until `保存策略`, then saves
the same key payload shape as before.

## Layout

- Top metrics become a compact strip with four small inline facts rather than
  large cards.
- The main board keeps a left list and right sticky detail panel, but the left
  list gets more horizontal room by removing quota mini-cards and redundant
  columns.
- The left list displays only durable scan fields:
  alias/preview, enabled, quota/RPM readiness, and model allowlist summary.
- The right detail panel owns quota numbers, limit inputs, model editing entry,
  and soft resets.

## Rendering Rules

- Source/inheritance fields remain in data but are not rendered for ordinary
  current keys.
- Removed/conflict/error states may still render explicit warnings because
  those states are actionable.
- Price coverage is not a list badge. Missing model prices are summarized in
  the detail panel and fully edited in the modal.
- Button feedback is state-driven through small helpers: set busy text, disable
  during async work when appropriate, then briefly show completion text.

## Compatibility

- No backend payload or database shape changes.
- Existing tests should be updated from old marker expectations to the new UX
  contract.
- Deployment only needs rebuilding `cpa-key-policy-plus.so` and restarting CPA.
