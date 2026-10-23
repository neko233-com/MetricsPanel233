# Delivery evidence

Updated: 2026-10-22. Source, local tests, packages, hosted CI, browser evidence and production deployment are separate acceptance layers.

## Current composition

| Module | Source | Published/runtime boundary |
| --- | --- | --- |
| metrics233-server | 4d5df325a8876dbd1da772365f209248a4ce679f | Accepted private 0.2.20 runtime is e5f93cb41065d4d88c5605006cdc64c8edff72dd; the current pin adds acceptance documentation only. |
| metrics233-lib-go | 4f63688e023bce29b1e6b79c14733e246bc277b6 | Go SDK release 0.1.0; real game reporting remains active. |
| metrics-lib-rust | 3a58fff2901e001984b45791fb884d23e3d8f9b7 | Official crates.io metrics233 0.1.0 is available; this pin only updates its guidance. |
| metrics233-client-sdk-typescript | 152ee85e628cca8a3126cc61b2bb9593c062a1ba | GitHub v0.1.0 plus installable tgz; npm registry upload is separate. |
| metrics233-client-sdk-csharp | 95a93a7d1b19d1f5d14b555996d0afa7c37a1209 | GitHub v0.1.0 plus Metrics233.Client NuGet artifact; NuGet.org upload is separate. |
| metrics233-client-sdk-rust | 8c78e875f278e9d764733486367bb1afe260ee71 | Official crates.io 0.1.0 and GitHub v0.1.0 share identical immutable package bytes. Current pin records installation and real-server acceptance. |

## Accepted online behavior

- Private 0.2.17 cut over 16661384 real samples to embedded XOR/Zstd time series and local Badger/HNSW vectors with reopened exact float/count verification, including 40 normalized empty-label series. Independent control/identity/key files remained protected. Active native statistics initially occupied about 42 MB; the stopped legacy control file occupied 588 MB. Retained backups are separate from these active-storage figures.
- Private 0.2.19 completed the full fixed-source worker gates and exact target deployment. All six real game/gateway identities have explicit physical-host associations. Process CPU/RSS/uptime and the corresponding host CPU/memory/disk queries returned actual observations; five inherited business templates remained intact.
- Edge displayed the actual intranet instance resource gauges/capacity, last-hour curves and game process resources. The formal game SQL view sorted all 84 eligible rows before Top10; header primary sort, Shift-added secondary sort and the priority editor were exercised on the production page.
- Native-engine notification acceptance separately passed 24 transitions, one firing and one recovery provider ACK, second-cycle 1h cooldown suppression and owned-rule cleanup. The new readable signed card also passed a single explicit notification test with one HTTP 200 provider ACK. Temporary checks did not fabricate a production resource fault.
- Ingest-only credentials passed missing/wrong-token 401 and read/admin 403 boundaries. Account login, canonical source-IP policies and versioned 15-day retention remain independent controls.
- The failed 0.2.18 attempt stopped at an SSH handshake reset before any target mutation. It did not replace the healthy 0.2.17 service. Later 0.2.19 acceptance is recorded independently; no failed run is described as green.

## Client SDK evidence

- TypeScript: zero runtime dependencies, 10 local tests including a real native server, exact retry/partial ACK, finite bounds, redirect rejection, Retry-After and credential isolation. The official GitHub tgz was downloaded, SHA/provenance-checked, installed into a fresh cache/project, type-checked and successfully reported to an isolated real server.
- CSharp: netstandard2.0/net8.0/net10.0 build and NuGet pack, thread-safe cumulative counter/zero/ACK/retry/deadline tests and actual native-server ingestion/isolation. A fresh NuGet cache/project restored the package and reported successfully; the installed DLL hash matched the build. Official GitHub bytes matched that tested artifact. Unity/IL2CPP/device acceptance is not claimed.
- Rust: MSRV 1.88 tests and real-server correct/wrong-token checks; Clippy/doc/package checks. Both metrics233 0.1.0 and metrics233-client-sdk-rust 0.1.0 are officially published. A new Darwin arm64 project installed the alias through official cargo add, compiled and reported successfully; reopened embedded storage returned Counter 5 and Gauge 233, wrong/missing-token 401 and ingest-only management-read 403 passed. Official alias package SHA-256: 209401a867d0a0787e76eb36f6f481c96e24a43505743b45592ebf410b99eb2a. Windows fresh compilation hit OS error 1455 and is not counted as passing; the native consumer and Rust 1.88 tag CI are separate successful gates. See the child's docs/registry-v0.1.0.md for cache/source provenance.
- All three SDK tag workflows succeeded: [TypeScript](https://github.com/neko233-com/metrics233-client-sdk-typescript/actions/runs/39768659392), [CSharp](https://github.com/neko233-com/metrics233-client-sdk-csharp/actions/runs/39768665546), [Rust](https://github.com/neko233-com/metrics233-client-sdk-rust/actions/runs/39768672606). Hosted transport/package checks and local actual-server tests are distinct gates.

## Final follow-up gates

The current server pin adds authenticated Agent operation schemas for instance resources/host association/ranking, explicit stale-report labels and virtual-list scroll reset after sorting. Discovery/business tests and vet, 163 frontend units and production build passed. Its immutable 0.2.20 rollout passed all full-worker/readiness/archive gates and actual target deployment. Live discovery returned the four expected operations/roles; six instance resource associations and complete-set SQL sorting passed again (132 eligible rows at final readback). Aggregate composition ac55170e876781f561feb7c966f0cede0581b067 was pushed and independently cloned with all six submodules. Every pinned SHA matched, the new checkout was clean, and its six-module public-source/history gate passed. This subsequent receipt update changes documentation only.

The public-source checker now covers all six canonical children, CSharp/ESM source and lock files. Exact earlier two/three-module compositions are accepted only in history. Private deployment identities, addresses and receipts stay in the separate private repository. Overall server roadmap weighting remains 94%; no new game-account Login/Ready/Ping or nonzero player-lifecycle acceptance is claimed.

Earlier evidence is retained in [the historical ledger](history-20261022-before-native-cutover.md) and the server's own docs/status.md.
