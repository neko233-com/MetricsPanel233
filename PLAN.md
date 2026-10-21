# metrics233-all execution plan

Updated: 2026-10-21. This aggregate owns repository composition, SDK/server integration and public/private delivery boundaries. The server's detailed product roadmap remains in `metrics233-server/PLAN.md`.

- [x] Preserve a complete Git bundle and SHA-256-checked working-tree backup before the split.
- [x] Move the server snapshot into `metrics233-server` and update its Go module/import path; retain CLI/HTTP compatibility.
- [x] Publish the two independent public child repositories and pin them as real Git submodules.
- [~] Rename the existing GitHub repository to `metrics233-all`, publish the aggregate and verify a fresh recursive clone.
- [x] Deliver the fluent standard-library Go SDK; independently review retries, memory bounds and shutdown; test against the actual renamed server binary.
- [x] Run the public-source and history checks before publication; keep production configuration in the private deployment repository.
- [~] Finish renamed-server Docker/browser/release-CI verification; retain the measured pre-split baseline separately.
- [~] Finish the single private Express233 production pipeline, explicit target identity, consistent backup/rollback and protected configuration.
- [ ] Complete the real target's Nginx/TLS, runtime, business metric and recovery acceptance.

Optional Vite+/ReactBits experiments remain optional. Do not use them to delay the monitoring workflow. Do not mark queued CI, compiled scripts or a listening port as successful production acceptance.
