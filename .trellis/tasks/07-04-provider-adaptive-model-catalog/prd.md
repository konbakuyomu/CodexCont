# Provider Adaptive Model Catalog

## Goal

Make CPA Key Policy+ model selection follow the models currently known by CPA
providers instead of relying on stale Plus configuration or weak
`host.auth.list` hints. Admins should see provider-discovered model candidates
when editing a key, while existing per-key allowlists remain explicit policy.

## Requirements

- Add a safe, read-only CPA plugin host model discovery callback for loaded
  plugins to query the CPA model registry.
- CPA Key Policy+ `/admin/api/models` must prefer registry-discovered models,
  then fall back to current host auth hints, then preserve existing Plus
  configured models and price-only models.
- The model catalog must expose only safe metadata such as model id,
  display name, type, owner, provider, auth id/name, and source. It must not
  expose raw keys, auth storage JSON, OAuth tokens, cookies, request bodies,
  response bodies, encrypted reasoning, or full secret hashes.
- Discovery updates selectable candidates only. It must not automatically
  change existing key allowlists or prices.
- The Plus admin model editor must show provider/source labels, keep manual
  model entry support, and provide an explicit one-click action to replace the
  current key allowlist with discovered registry models.
- Newly discovered models without configured prices must be visibly marked so
  admins understand quota cost accounting may not cover them.
- Keep public API compatibility: no public `/v1/*` API change and no change to
  the meaning of an empty model allowlist (`models=[]` means allow all).

## Acceptance Criteria

- [x] CPA unit tests cover `host.models.list` returning safe registry models
      and not leaking sensitive auth data.
- [x] Plus unit tests cover registry-first catalog behavior, fallback behavior,
      and preservation of unknown configured/manual models.
- [x] Plus admin HTML/JS tests cover source/provider labels, explicit allowlist
      replacement, and missing-price warning text.
- [x] `go test ./... -count=1 -timeout=120s` passes for
      `cpa_key_policy_plus_plugin/go`.
- [x] Relevant CLIProxyAPI Go tests pass for the changed plugin host/model
      callback packages.
- [x] `git diff --check` passes.

## Notes

- User approved the plan and task creation on 2026-07-04.
- Out of scope: CodexCont Executor model routing changes and provider price
  auto-discovery.
- Verification completed: Plus `go test ./... -count=1 -timeout=120s`,
  CLIProxyAPI full `go test ./... -count=1 -timeout=180s`, and `git diff
  --check` in both repositories.
