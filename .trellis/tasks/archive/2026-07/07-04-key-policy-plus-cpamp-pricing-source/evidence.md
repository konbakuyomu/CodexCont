# Evidence

## Implementation

- Added read-only CPAMP `model_prices` SQLite loading through optional `cpamp_price_db_path` / `cpamp_price_db_paths`, falling back to existing CPAMP alias DB paths when no explicit price path is configured.
- Added cached CPAMP price snapshots in Plus `settings`; live pricing uses `cpamp_price_book`, fallback uses `cpamp_cached_price_book`, and cold-start unavailable pricing records `cpamp_price_unavailable`.
- Changed `usage.handle` to calculate request cost from the global CPAMP price book instead of per-key `KeyRecord.Prices`.
- Added CPAMP-compatible `service_tier=priority/fast` multipliers: `gpt-5.5 = 2.5x`; `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.3-codex = 2x`.
- Added current-month `usage_events` cost recalculation after a fresh live CPAMP price snapshot is loaded.
- Simplified Plus admin UI so ordinary policy editing no longer exposes per-key price editing; model allowlists remain editable and missing CPAMP prices are shown as warnings.

## Validation

- Windows local:
  - `go test ./... -count=1 -timeout=120s` in `cpa_key_policy_plus_plugin/go`
  - Result: passed.
- WSL local using existing toolchain:
  - `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go version`
  - Result: `go version go1.22.6 linux/amd64`
  - `/mnt/d/Dev/20_Software/_LocalRuntime/go/go1.22.6-linux-amd64/go/bin/go test ./... -count=1 -timeout=120s`
  - Result: passed.
- Linux plugin ABI build:
  - Output: `/mnt/d/Dev/20_Software/_LocalRuntime/CodexCont/build/cpa-key-policy-plus/cpa-key-policy-plus.so`
  - `file`: `ELF 64-bit LSB shared object, x86-64`
  - SHA256: `ee4caa28724eefbbaa2fc0c1351a0aafcca54955c762965e03100805328c0c5c`
- `git diff --check`
  - Result: passed; only repository line-ending warnings were printed.

## Deployment

- Remote target: `sjc-snap`
- Previous remote plugin SHA256:
  `f2a3f36c8314ceecee7177c49845dd9830d61e942f85b186a4e83c4aaf1b0303`
- Deployed plugin SHA256:
  `ee4caa28724eefbbaa2fc0c1351a0aafcca54955c762965e03100805328c0c5c`
- Remote backup:
  `/opt/codex-stacks/backups/key-policy-plus-cpamp-pricing-20260704-062850`
- Restarted only the `cpa` container.
- CPA logs after restart show `plugin_id=cpa-key-policy-plus` loaded and
  registered from `/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so`.
- Container plugin SHA matched the deployed artifact:
  `ee4caa28724eefbbaa2fc0c1351a0aafcca54955c762965e03100805328c0c5c`.

## Production Smoke

- Internal Plus keys API returned `4` current keys and pricing status:
  `source=cpamp_price_book`, `model_count=5`.
- Internal Plus models API returned `19` model options and the same pricing
  status.
- CPAMP price cache exists in Plus settings with these models:
  `codex-auto-review`, `gpt-5.3-codex-spark`, `gpt-5.4`,
  `gpt-5.4-mini`, `gpt-5.5`.
- Public `https://cpa-usage.konbakuyomu.us/` returned `200`.
- Public management boundary checks returned `404` for:
  `https://cpa.konbakuyomu.us/v0/resource/plugins/cpa-key-policy-plus/admin`,
  `https://cpa.konbakuyomu.us/key-policy-plus/`, and
  `https://cpa.konbakuyomu.us/admin/`.
- Authenticated internal `gpt-5.5` Responses smoke returned HTTP `200`.
- The latest Plus usage event from that smoke recorded:
  `model=gpt-5.5`, `service_tier=priority`, `cost=0.004162500`,
  `cost_breakdown.source=cpamp_price_book`, and
  `service_tier_multiplier=2.5`.

## Notes

- One WSL build attempt exposed the known PowerShell-to-bash CRLF/BOM quoting hazard. The `.so` still built successfully; final `file` and `sha256sum` were rerun with direct WSL commands.
- One remote smoke script attempt exposed a PowerShell/shell quote hazard around
  Python string literals. The final smoke used `python3 -` over stdin and did
  not print API keys or response bodies.
- No build artifacts were written into the git worktree.
