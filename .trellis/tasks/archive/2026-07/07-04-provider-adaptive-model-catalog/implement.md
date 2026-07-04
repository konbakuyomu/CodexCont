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

## Deployment Evidence

- Built Linux amd64 CLIProxyAPI binary:
  `8c3f98d64ed77c8ad4a9792ffbb7412b5af2e7da9a883677d559f9955049cbbf`.
- Built Linux amd64 CPA Key Policy+ plugin:
  `cabb9baa6f27801f73700f9ed0e1fa15743b0b1344945f8ef56789ab48a9229f`.
- Deployed on `sjc-snap` by bind-mounting
  `/opt/codex-stacks/cpa/bin/CLIProxyAPI` into the `cpa` container and
  replacing `/opt/codex-stacks/cpa/plugins/linux/amd64/cpa-key-policy-plus.so`.
- Remote CPA version after restart:
  `v7.2.50-provider-adaptive`, commit `2eafde3c`.
- Internal Plus model API returned `7` models, all from `source=cpa_registry`
  and `provider=codex`, with no warnings.
- Internal `/v1/responses` smoke test for `gpt-5.5` returned HTTP `200` and
  streaming smoke test ended with `response.completed`.
- Public plugin admin/resource paths returned HTTP `404`; public
  `cpa-usage.konbakuyomu.us` returned HTTP `200`.
