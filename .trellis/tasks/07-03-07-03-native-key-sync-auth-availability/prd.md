# Fix native key sync and auth availability

## Goal

Restore production usability for the CPA-first plugin chain:

- `CPA Key Policy+` must mirror the official CPA/CPAMP native API key list.
- The Plus admin page must show only currently-present official native keys by default.
- Alias changes and deletion from the official CPAMP key panel must be reflected promptly.
- Requests using all enabled official keys must stop failing with
  `host_call_failed: auth_unavailable: no auth available (providers=codex, model=gpt-5.5)`.

## User Value

The operator should manage API keys in the official CPAMP panel only. Plus is a
policy overlay and should not show stale old `cpa_...` records or stale aliases.
Users should be able to call supported Codex models with any enabled official
key.

## Confirmed Evidence

- Current production Plus DB contains 7 rows: 3 `legacy_plus` rows and 4
  `native_cpa` rows.
- CPAMP official alias DB contains 5 alias rows, including the new names
  `alice` and `alicea`.
- Plus admin UI currently shows stale legacy rows and one native row with hash
  preview instead of the CPAMP alias.
- Current production executor config has only one upstream alias:
  `gpt-5.4 -> gpt-5.3-codex-spark`.
- User-reported failure is for `model=gpt-5.5`, which is not mapped to a
  provider-registered upstream model in current executor config.

## Requirements

- Plus admin list must default to current official native keys only.
- Old `legacy_plus` / `cpa_...` rows must not appear in the ordinary Plus admin
  key strategy table.
- Official native key alias updates must be visible after refresh/re-entering
  the Plus page.
- Official native key deletion must remove the row from the ordinary table on
  the next sync/read. Historical usage may remain in DB, but stale key rows must
  not be part of the active strategy list.
- Sync must accept CPAMP `api_key_aliases.api_key_hash` whether stored as bare
  SHA256 hex or `sha256:<hex>`.
- Admin APIs that read keys should trigger native sync before listing so the UI
  does not wait for CPA restart or a user login.
- Executor model routing must map client-visible `gpt-5.5` to the currently
  available Codex upstream model, preserving downstream/user-visible model name.
- Public admin/resource paths must remain blocked.
- Do not modify official CPA/CPAMP source.
- Do not store or print raw `sk-...` keys, bearer tokens, cookies, or encrypted
  reasoning.

## Acceptance Criteria

- [ ] Production Plus admin table shows exactly the official active native keys
      by default, matching the official panel count and aliases.
- [ ] Creating or aliasing a native key in CPAMP is reflected in Plus after
      refresh without restarting CPA.
- [ ] Deleting a native key in CPAMP hides/removes it from the Plus default
      table after refresh without showing stale old `cpa_...` rows.
- [ ] Plus tests cover admin list native-sync refresh, legacy rows hidden from
      default strategy list, and bare-hash CPAMP alias reads.
- [ ] Executor tests cover `gpt-5.5` aliasing to the configured upstream Codex
      model while preserving downstream model `gpt-5.5`.
- [ ] Production `/v1/responses` smoke with `model=gpt-5.5` succeeds with an
      enabled official key.
- [ ] Production `cpa-usage.konbakuyomu.us` still works.
