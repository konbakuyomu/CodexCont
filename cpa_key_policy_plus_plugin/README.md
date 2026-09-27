# CPA Key Policy Plus Plugin

`cpa-key-policy-plus` is the self-owned replacement for the old Key Policy
plugin. It owns ordinary user CPA native `sk-`/`sk_` keys, per-key limits, usage projection,
soft resets, model allowlists, RPM policy, and rolling quota windows.

It does not modify CPA, CPAMP, or the old Key Policy source/image.

Concurrency and Codex active-window limits were intentionally retired because
normal Codex conversations can trip them too easily. Old database fields remain
for schema compatibility, but new saves force them to `0` and frontend auth does
not enforce them.

## Safety Boundary

Plus is safe to deploy as the exclusive user-key auth and quota authority. It
does not need CodexCont's HTTP dashboard API to render the user portal. Keep
`codexcont_enabled: false` during the native CPA cutover: the portal will not
probe CodexCont or open an executor summary database, and its protection tab
reports the source as disabled rather than an offline health failure. A legacy
executor-history SQLite read is available only when that setting is explicitly
true and `codex_summary_db_path` is configured.

## Local Test

```powershell
cd D:\Dev\20_Software\23_Reference\llm-gateway\CodexCont\cpa_key_policy_plus_plugin\go
go test ./...
```

## Linux Build

```bash
cd cpa_key_policy_plus_plugin/go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o cpa-key-policy-plus.so .
```

The production CPA host loads platform artifacts from the configured plugin
directory, for example:

```text
/CLIProxyAPI/plugins/linux/amd64/cpa-key-policy-plus.so
```

## Minimal CPA Config

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-key-policy-plus:
      enabled: true
      priority: 10
      exclusive_auth: true
      state_db_path: /CLIProxyAPI/plugin-state/cpa-key-policy-plus/policyplus.sqlite
      key_policy_state_path: /CLIProxyAPI/plugin-state/cpa-key-policy-state.json
      cpamp_price_db_path: /data/usage.sqlite
      session_secret: ${CPA_KEY_POLICY_PLUS_SESSION_SECRET}
      codexcont_enabled: false
      codexcont_route: false
      fail_mode: fallback
```

Disable the old `cpa-key-policy` only after Plus loads and current native keys
authenticate successfully.

## Weekly-only migration

Use the dedicated management endpoint once the Plus database has been backed
up and its normal Key/usage read-back is authoritative. Run one request per
existing key so each change has an independent read-back and rollback point:

```json
PUT /plugins/cpa-key-policy-plus/keys/weekly-only
{
  "id": "<existing-key-id>",
  "weekly_only": true,
  "weekly_usd": 500
}
```

The endpoint clears the 5-hour, 24-hour, and monthly limits without changing
the key ID, alias, RPM, model allowlist, usage history, or 7-day reset
watermark. If a weekly limit already exists, `weekly_usd` is only a fallback
and cannot overwrite it. Its response reports `quota_mode: "weekly_only"`;
the user portal then shows only the rolling 7-day card and queries the 7-day
range.

That mode is persisted in Plus. Subsequent old JSON and legacy SQLite imports
may continue identity synchronization, but cannot restore the stopped windows
or replace the 7-day watermark. Retire legacy quota import paths only after
this API read-back confirms each key's preserved weekly limit and history; do
not use hand-written SQL to perform the migration.

## Billing-pending safeguard

If a completed usage callback reports an actual model or service tier that is
not covered by the verified CPAMP price book, Plus keeps the event as
`billing_pending` rather than treating `$0` as its final cost. It also sets a
durable billing hold on that Key, so later requests return `billing_pending`
until the discrepancy is resolved. The user and admin Key responses expose the
pending reason and held model.

After synchronizing and verifying the CPAMP rule, resolve only that Key's
pending events:

```json
PUT /plugins/cpa-key-policy-plus/keys/billing/resolve
{
  "id": "<existing-key-id>"
}
```

The operation recalculates only rows already marked `billing_pending`. It
fails closed if any pending row still lacks an applicable price, and clears the
Key hold only after every pending row has been settled. It never reprices
ordinary history.

## User Page

`cpa-usage.konbakuyomu.us` should expose only the Plus user resource and
`/user/api/*` compatibility routes. User login uses the full native `sk-` or
`sk_` key in the `X-CPA-Key-Policy-Plus-Key` header. Retired `cpa_` keys and
shortened previews are rejected.
