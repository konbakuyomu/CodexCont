# Provider Adaptive Model Catalog Design

## Architecture

CPA remains the source of truth for provider/auth model availability. Add a
small plugin-host callback, `host.models.list`, that projects the global model
registry into a safe JSON catalog. CPA Key Policy+ consumes that callback from
its existing management `/models` endpoint.

## Data Flow

CPA auth/provider registration -> CPA global model registry ->
`host.models.list` safe projection -> Plus `adminModelCatalog()` merge ->
Plus admin model editor.

Merge precedence is registry first, then existing `host.auth.list` hints, then
Plus configured models/prices. This preserves old allowlists if live discovery
is empty, stale, or unavailable.

## Contracts

- `host.models.list` returns safe model entries only:
  `id`, `display_name`, `type`, `owned_by`, `provider`, `auth_id`,
  `auth_name`, `source`.
- Plus labels registry entries as `cpa_registry`.
- Existing source labels remain valid: `host_auth`, `plus_configured`.
- Empty per-key model list still means allow all.
- Model discovery never mutates key settings by itself.

## Compatibility

Old CPA hosts without `host.models.list` must keep Plus working through the
existing `host_auth + plus_configured` fallback. Unknown configured models must
stay visible until an admin removes them.

## Security

The callback must not return raw key material, auth storage JSON, headers,
tokens, cookies, request/response bodies, encrypted reasoning, or full secret
hashes. Plus UI must not introduce any new public/admin route exposure.
