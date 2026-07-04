# Design

## Current Data Flow

CPAMP admin:

`executor summaryMonitor.Start -> monitor.Recent -> /admin/api/summaries -> admin.html`

Plus user page:

`executor SQLite codexcont_summaries -> Plus RecentCodexSummariesFromSQLite -> /user/api/codexcont -> user.html`

The admin page sees `processing` because it reads the executor's in-memory
monitor. The user page does not because the executor SQLite bridge is written
only by `saveFoldSummary` after a stream finishes.

## Target Data Flow

Executor writes a safe processing summary to SQLite when a folded stream starts.
On success it removes the placeholder when the final request id differs and
saves the final summary. On failure it updates the same row to `failed`.

Plus continues reading only the executor SQLite bridge by current key id, with
the existing safe identity fallback. It filters stale processing rows so a CPA
restart or executor crash does not leave the user page showing old active work.

## Contracts

- `codexcont_summaries.key_id` remains the primary safe filter for Plus.
- `summary_json.key_identity` remains safe display metadata only.
- `request_id` for processing placeholders may be `processing-<nanos>`.
- Terminal success may use the upstream `resp_...` id. If it differs from the
  placeholder id, the placeholder must be deleted before saving the final row.
- Terminal failure keeps the processing id and marks the row failed, because no
  final upstream response id may exist.

## Safety

No raw keys, full hashes, Authorization headers, OAuth tokens, request bodies,
response bodies, or encrypted reasoning are written to summaries. Existing
redacted diagnostics remain allowed.

## Compatibility

No CPA/CPAMP API changes. Existing admin UI still works because it continues to
read memory plus store rows. Existing Plus UI already understands `processing`;
the main missing piece is persisted live data.
