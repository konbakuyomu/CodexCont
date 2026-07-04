# Design

## Data Flow

Executor writes safe request summaries to its SQLite store. Plus reads that
store through `codex_summary_db_path`, filters by current key identity, and
returns a safe projection from `/user/api/codexcont`. The user page then renders
the current-key table and detail rows.

The inspected production record already proves `rounds[]` survives through the
SQLite bridge and Plus API. The remaining gaps are a too-narrow safe field
projection and a user-page detail layout that summarizes rounds instead of
showing them.

## Safe Projection

Extend the existing `safeCodexSummary` allowlist with safe scalar flags and
diagnostic fields only:

- `final_status`
- `first_truncation_n`
- `folded`
- `passthrough`
- `failure_detail`

Do not add body/header/auth/encrypted-content fields.

## User Rendering

Keep the current `cpa-usage` layout but add detail parity:

- Table timestamp uses `updated_at || started_at`.
- Request detail shows key name and safe preview from `key_identity`.
- Detail cards include start/update/end time.
- Add a `轮次` card that renders each `rounds[]` item as
  `#<round> reasoning <reasoning_tokens> / <decision>`.
- Empty rounds render `无轮次摘要`.

## Compatibility

No route or API path changes. The JSON response is an additive safe-field
extension, so old clients keep working.
