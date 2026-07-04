# CPA Key Policy+ Admin Slim UX

## Goal

Make the CPA Key Policy+ administrator page feel like a focused policy editor
instead of a noisy diagnostics table. The page should let an admin quickly see
which native CPA keys are enabled, whether each key has RPM/quota configured,
and whether the key has a model allowlist, then edit the details in the right
panel without text clipping or redundant tags.

## Confirmed Facts

- The admin UI is embedded in `cpa_key_policy_plus_plugin/go/assets/admin.html`.
- The backend key/model/quota API shape is already sufficient; this task should
  not change CPA/CPAMP APIs or Plus JSON schemas.
- `models=[]` means "allow all models"; non-empty `models` is a strict model
  allowlist.
- Current page noise comes from list/detail tags such as `CPA 原生`, `已继承`,
  and price coverage like `6/7 已计价`, plus the four quota mini-cards squeezed
  into the left table.

## Requirements

- Replace the large top metric cards with a compact status strip.
- Change the left key list to a scan-friendly summary:
  key alias/preview, enabled state, quota configured state, RPM configured
  state, and model allowlist count.
- Remove ordinary display of source/inheritance tags and price coverage from
  the key list and detail header.
- Move full quota numbers to the right detail panel using a stable 2x2 or
  compact layout that does not clip text.
- Keep model allowlist editing and price editing in the existing modal, but
  make the actions clearer and add button feedback.
- Keep security and ownership boundaries unchanged: raw keys, full hashes,
  OAuth data, request/response bodies, and encrypted reasoning stay hidden.

## Acceptance Criteria

- [ ] Left table no longer renders four quota mini-cards or a price coverage
      badge.
- [ ] Left table still renders `7 个模型` and `允许全部模型` states.
- [ ] Ordinary active native keys no longer show `CPA 原生` or `已继承` in the
      always-visible UI.
- [ ] Right detail panel shows quota `已用 / 限额 / 剩余` without clipping and
      keeps editable limits.
- [ ] `保存策略`, `刷新`, `重新获取模型`, and model modal actions show visible
      busy/complete/active feedback.
- [ ] `go test ./... -count=1 -timeout=120s` passes in
      `cpa_key_policy_plus_plugin/go`.
- [ ] `git diff --check` passes.
- [ ] Production deployment preserves `cpa-usage.konbakuyomu.us`,
      `/v1/responses`, and public admin/resource 404 boundaries.

## Out of Scope

- User usage page changes.
- CPA/CPAMP official UI or API changes.
- Backend schema changes for keys, quotas, models, or prices.
- Changing quota enforcement semantics.
