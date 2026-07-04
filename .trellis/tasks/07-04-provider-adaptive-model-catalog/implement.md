# Provider Adaptive Model Catalog Implementation Plan

## Steps

1. Add `host.models.list` method constants and typed payload structs to
   CLIProxyAPI plugin ABI/API surfaces.
2. Implement the callback in the CPA plugin host using the global model
   registry and current auth manager, returning only safe projection fields.
3. Add/adjust CLIProxyAPI tests for callback availability, registry projection,
   per-auth metadata, dedupe behavior, and sensitive-field absence.
4. Add the new callback constant and response structs in CPA Key Policy+.
5. Change Plus `adminModelCatalog()` to call registry discovery first, merge
   with host auth hints and configured models, and keep warnings on fallback.
6. Extend `ModelOption` projection to carry provider/auth display metadata.
7. Update Plus admin UI model editor to show source/provider labels, missing
   price warnings, and an explicit "replace allowlist with discovered models"
   action.
8. Update Plus tests for registry-first catalog, fallback, UI labels, explicit
   replacement hook, and unknown configured model preservation.
9. Run required Go tests and `git diff --check`.

## Validation Commands

- `go test ./... -count=1 -timeout=120s` in `cpa_key_policy_plus_plugin/go`
- Targeted CLIProxyAPI Go tests for pluginhost/pluginapi changes
- `git diff --check`

## Rollback Points

- CPA host callback is additive. If Plus is deployed before CPA, it falls back
  to existing discovery behavior.
- Plus UI changes are management-only and do not affect `/v1/responses` or
  `cpa-usage` user APIs.
