# Daily Report Deployment Note

Deploy the current SJC `daily-update-report.py` runtime script to LAX with only this host-specific state path changed:

```text
/var/lib/sjc-auto-upgrade-governor/state.json
-> /var/lib/lax-auto-upgrade-governor/state.json
```

The script already has host-label-specific behavior for LAX:

- `--host-label LAX` checks `1pctl version`.
- `--host-label LAX` resolves `/etc/vps-capacity-guard/lax.json`.
- Docker Engine and Compose become `host-adapter` items once adapter-state support is present.
- 1Panel stays report-only/manual rather than mutating through this governor.
