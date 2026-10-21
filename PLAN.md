# metrics233-all execution plan

Updated: 2026-10-21. This aggregate owns repository composition, SDK/server integration and public/private delivery boundaries. The server's detailed product roadmap remains in `metrics233-server/PLAN.md`.

- [x] Preserve a complete Git bundle and SHA-256-checked working-tree backup before the split.
- [x] Move the server snapshot into `metrics233-server` and update its Go module/import path; retain CLI/HTTP compatibility.
- [x] Publish the two independent public child repositories and pin them as real Git submodules.
- [x] Rename the existing GitHub repository to `metrics233-all`, publish the aggregate and verify a fresh recursive clone; aggregate CI passed at `328c8ca`.
- [x] Deliver the fluent standard-library Go SDK; independently review retries, memory bounds and shutdown; test against the actual renamed server binary.
- [x] Publish SDK v0.1.0 with successful Go 1.23/1.27 tag CI and a fresh public module consumer installation/build/Push/Close check.
- [x] Publish server v0.2.1 from clean `f0244c3`; compare two five-platform builds, inspect all archives and binaries, run the actual Linux archive and verify official asset downloads.
- [x] Run the public-source and history checks before publication; keep production configuration in the private deployment repository.
- [x] Complete immutable server v0.2.1 CI: full vet/race, Playwright and repeated Docker integration passed. Keep the failed hosted 20-minute attempt and measured local baseline separate.
- [x] Update the aggregate to the approved server `f0244c3` pin and release documentation; verify the published revision with a fresh recursive checkout.
- [x] Complete the single private Express233 production pipeline, explicit target identity, consistent backup/rollback and protected configuration; the fixed-source real deployment succeeded.
- [x] Complete the actual HTTPS endpoint, non-root loopback runtime, SDK business push/query and normal-restart persistence acceptance.

Optional Vite+/ReactBits experiments remain optional. Do not use them to delay the monitoring workflow. Do not mark queued CI, compiled scripts or a listening port as successful production acceptance.

- [x] Implement restricted ingest credentials, persistent source control and one-click source UI; final Windows Go and 86-pass embedded browser suite completed.
- [x] Implement encrypted Feishu notifications and durable firing cooldowns; API/TLS/queue/recovery and UI checks passed.
- [x] Publish and verify server v0.2.2, exact updated aggregate pins, and actual production reporting/Feishu configuration. Tagged full vet/race, 86-pass browser suite and repeated Docker passed; new evidence remains separate from v0.2.1.
