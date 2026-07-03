# Fix CodexCont key identity display and user protection feed

## Goal

Make CodexCont protection records understandable and correctly scoped:

- The CPAMP `CodexCont Executor` page should identify the caller by the CPA
  Key alias/name plus live protection status, not by a long `resp_...` id.
- The `cpa-usage.konbakuyomu.us` user page should show realtime protection
  records for the currently logged-in native Key when those records belong to
  that Key.

## Requirements

- Keep the user page privacy boundary: ordinary users only see protection
  records for their current Key.
- Admin may see all executor protection records, but only safe key identity:
  id/name/alias/preview/source, never raw keys or full hashes.
- Plus `frontend_auth` must provide a safe identity bundle in metadata so the
  executor can persist enough context for display and filtering; because the
  live CPA self-executor route may not pass that metadata, the executor must
  also derive only safe native-key identity from the inbound Authorization hash.
- Executor summaries must persist the key id in the SQLite row and the safe key
  identity inside the summary JSON.
- Existing records that have no key identity must not be guessed into a user
  page. They may remain visible to admin as unknown caller records.
- Executor admin table should use a human readable request label:
  `<Key alias/name> · <protection status>`, with a short request id below it.
- Plus user page empty state should distinguish "no current-key records" from
  a broken protection feed.

## Acceptance Criteria

- [x] Trellis task records the screenshot-driven issue, privacy decision, and
  test/deploy checklist.
- [x] Executor unit tests cover `AuthMetadata` / `AuthAttributes` identity
  extraction into summaries and SQLite `key_id`.
- [x] Executor unit tests cover Authorization fallback deriving native
  id/preview/alias without raw key/full hash leakage.
- [x] Executor admin HTML/JS tests cover human-readable request labels and short
  request id display, without raw key/full hash leakage.
- [x] Plus tests cover current-key records returned from executor summary DB by
  key id and by safe preview match.
- [x] Plus tests cover other-key records being filtered out.
- [x] Plus tests cover executor DB available but current key has no records:
  empty response, clear source, no API failure.
- [x] Local `go test ./... -count=1 -timeout=120s` passes in both plugin
  packages.
- [x] Linux plugin builds pass with the existing WSL Go toolchain and
  `-tags cliproxy_plugin -buildmode=c-shared`.
- [x] SJC smoke: Executor admin page shows key alias/name plus protection
  status; `cpa-usage` current Key protection tab shows new current-key requests.

## Notes

- Chosen user-page scope: current Key only.
- Chosen admin request label: Key alias/name plus protection status as the main
  title, short request id as secondary text.
- Old summary rows without identity are not safely attributable and should not
  be forced into a user's feed.
- SJC deployment evidence:
  - executor `.so` SHA256:
    `14763ebbb43e2505aae381e0970e09c2a07bc913d424d8e4313e7170c0383919`
  - plus `.so` SHA256:
    `993eef2f68889e13879cb7bcb493135cb41b7d48caa1ce77dc7dcf4a57f35d84`
  - final backup:
    `/opt/codex-stacks/backups/codexcont-identity-feed-v4-20260704-074046`
  - live `/v1/responses` returned 200 and produced a current-key summary for
    `native_81197850_d80b6d` with display name `kuma的官key`.
