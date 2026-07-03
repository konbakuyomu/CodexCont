# Design

## Data Flow

```text
Plus frontend_auth authenticates native sk- key
  -> returns Principal=Plus key id and safe Metadata
  -> CPA may pass AuthMetadata/AuthAttributes to executor
  -> executor falls back to hashing inbound Authorization when metadata is absent
  -> executor records safe key_identity in memory + SQLite
  -> admin monitor renders key alias/name + status
  -> Plus user API reads executor summary DB filtered by current key id
```

## Identity Contract

Plus must return metadata fields that are safe to persist and render:

- `provider = cpa-key-policy-plus`
- `key_id`
- `key_name`
- `key_alias`
- `preview`
- `source`
- `source_present`

Executor should normalize safe identity from, in order:

- `AuthMetadata`
- `AuthAttributes`
- `Metadata`
- inbound `Authorization` header, hashed to Plus-compatible native id/preview
  and optional CPAMP alias through `cpamp_alias_db_path`
- `AuthID` only as a final id fallback

The resulting `key_identity` JSON should include only non-empty safe fields:
`known`, `id`, `name`, `alias`, `preview`, `source`.

Live deployment note: current CPA self-executor routing can set `AuthID` to the
upstream Codex auth account and omit Plus metadata, so `AuthID` must not
overwrite `key_id` from metadata or the Authorization-derived native id.

## UI Contract

Executor admin request table:

- Main label: `displayKey(row.key_identity) · protectionLabel(row.protection)`.
- Secondary line: short request id, e.g. `resp_0ec222...fc0e`.
- Detail panel retains full request id and safe key identity fields.
- Unknown identity displays `未知 Key` and still keeps the short request id.

Plus user protection feed:

- First source remains `codex_summary_db_path`.
- Query by current `key.ID` whenever possible.
- After reading, still pass each summary through `safeCodexSummary`; matching
  by `identity.id == key.ID` or `identity.preview == key.Preview`.
- When the executor DB can be read and there are no records for the current
  key, return an empty list with a non-error source such as
  `codexcont_executor_store`.

## Security

Do not store or return raw `sk-...`, Authorization headers, full hashes,
request bodies, response bodies, cookies, OAuth data, or encrypted reasoning.
Short request ids are safe; full request ids remain admin-only.
