# metrics233-all execution plan

Updated: 2026-10-22. The aggregate owns composition, SDK/server integration and public/private delivery boundaries. Detailed server work remains in metrics233-server/PLAN.md.

- [x] Six independently versioned public Git submodules; retain original Go/Rust server libraries and add TypeScript/CSharp/Rust client SDK entries.
- [x] Compressed TSDB and embedded vectors in production, with separately protected control/identity/key data. Actual 16661384-point migration/reopen checks and fresh reporting passed.
- [x] Reporting-server dashboards show explicit physical-host resources, actual process CPU/RSS/uptime and business data; self Overview remains separate. All six actual identities and production Edge pages accepted.
- [x] SQL/region tables support up to five ordered sort rules, global ordering before pagination/Top10, stable missing-last ties and frozen cursor scope. Formal game SQL 84-row complete-set sorting and browser primary/secondary rules accepted.
- [x] Host sustained thresholds, recovery confirmation and 1h cooldown; native-engine firing/recovery/CD acceptance and the new signed readable Feishu card provider ACK passed.
- [x] Client SDK source, GitHub v0.1.0 packages, local real-server auth tests and fresh consumer installs accepted. Three hosted tag workflows passed; no device or browser-public-client trust is inferred from .NET/TypeScript builds.
- [x] Original crates.io metrics233 0.1.0 availability and clean-cache registry dependency consumption verified. New Rust alias remains usable through its fixed Git tag.
- [ ] Publish the new metrics233-client-sdk-rust alias to crates.io when valid publish-new credentials are available; the existing credential was rejected with 403. GitHub delivery is complete. npm/NuGet registry uploads likewise are separate from the published installable GitHub artifacts.
- [x] Private 0.2.20 operation-discovery/staleness follow-up deployed; live operations/roles, all six resource associations and 132-row complete-set SQL ranking read back successfully.
- [x] Final aggregate composition pushed at ac55170e876781f561feb7c966f0cede0581b067. A fresh recursive checkout matched all six pinned modules and passed the public-source/history gate; the following receipt update changes documentation only.

Source pushes, package installation, registry publication, hosted CI and online deployment are distinct. No production addresses or credentials are stored in this aggregate. Real player-account login/logout is omitted at the user's request; nonzero player-lifecycle acceptance is not claimed. Overall server roadmap weighting remains 94%, with exact per-layer evidence in docs/status.md.
