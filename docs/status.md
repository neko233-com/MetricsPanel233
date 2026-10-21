# Delivery evidence

Updated: 2026-10-21. The aggregate composes independently versioned public server, Go SDK and Rust SDK repositories.

## Current source: panel readability after server v0.2.6

The approved public child sources are:

| Child | Exact source |
| --- | --- |
| Server | [`ed444317090daa1c7cd6b76c70e0ca8ed4d208f0`](https://github.com/neko233-com/metrics233-server/commit/ed444317090daa1c7cd6b76c70e0ca8ed4d208f0), final local UI readability correction; no new release tag |
| Go SDK | [`4f63688e023bce29b1e6b79c14733e246bc277b6`](https://github.com/neko233-com/metrics233-lib-go/commit/4f63688e023bce29b1e6b79c14733e246bc277b6) |
| Rust SDK | [`f38673c6c90ccda1bcd2503be3e20ad7a0d65efa`](https://github.com/neko233-com/metrics-lib-rust/commit/f38673c6c90ccda1bcd2503be3e20ad7a0d65efa), README-only correction; code/tag `v0.1.0` remains `bda4716538a366379f69300cb816e272f1db11de`, package `metrics233` |

The server UI source above has been pushed; the Go and Rust pins are unchanged. The latest public five-platform
release remains server `85630554` / `v0.2.6`. The current UI source `ed444317` completes the local known-metric
correction; its private deployment `0.2.8` **failed before service stop** during release rollback-archive preparation.
The healthy `0.2.7` service remains online without package replacement or runtime rollback. A public Express233
archive fix has passed isolated Linux script validation; its CI remains pending. New immutable deployment `0.2.9`
has prepared parameters but has **not been triggered**.
The preceding `c79971b8` / `0.2.7` succeeded with intermediate actual version/source, account/session, runtime and
fresh-report evidence, but its real self-metric legends still exposed raw names because the Go heap-allocation,
goroutine-count and cumulative HTTP-request mappings were absent
from the known-metric map. Final live legend acceptance remains **pending**. No new tag, public archive or CI
success is claimed for the current UI source.
The preceding aggregate source [`77796f9c51dc2c63222abbfadefb6fc0d09eea82`](https://github.com/neko233-com/metrics233-all/commit/77796f9c51dc2c63222abbfadefb6fc0d09eea82)
publishes server `85630554` with the same Go/Rust pins; its completed checkout and CI evidence below belongs to that
earlier composition. Server v0.2.6 fixes a notification timeout-classification race: an HTTP client
timeout can arrive before the request context reports its deadline. Wrapped deadline and network timeout errors
now retain the timed-out outcome. The final alerting vet/race evidence below covers that correction.

The preceding `37bfa961` / v0.2.5 source added only two lines over `527494a6` / v0.2.4 in the bootstrap permission
fixture: a comment and an explicit `Chmod(0644)` for the intentionally unsafe sidecar. Linux bootstrap checks under
umasks `022` and `077` passed **20 executions, zero skips**. Runtime/web were unchanged by that fixture correction.
The earlier browser/database/runtime evidence below keeps its original input attribution; it is not a fresh
execution of the v0.2.6 notification fix.

The Rust SDK is now a real third public submodule. Its Git/tag publication and
[tag CI 39579220148](https://github.com/neko233-com/metrics-lib-rust/actions/runs/39579220148) passed at code/tag
`bda47165`. The current `f38673c6` changes only README installation guidance to that immutable Git revision; runtime
code is unchanged. Official crates.io upload and identity checks returned HTTP 403: registry publication and a fresh
registry consumer remain **blocked**, not completed. The public Git source is available independently of that
registry boundary. The original Go SDK v0.1.0 consumer/CI evidence below retains its original `5bb1f54` attribution;
it is not silently reassigned to the newer Go source pin.

The server already supports separate local-account administration and five-path ingest authorization, protected
offline administrator bootstrap, durable configurable 15-day SQLite retention, authenticated agent discovery,
native numeric uptime and editable risk thresholds. Business instances are discovered from accepted reports;
full identity scope, advisory reported IP and canonical observed source IP remain distinct. Live template
inheritance and independent copies are supported. Player population/regions and current cumulative sessions/SQL
use the latest authoritative population snapshot, exposing missing, partial or stale data instead of inventing
zero. Historical SQL windows retain their counter-reset semantics. Full region and Entity result lists use cursor
pagination and a bounded virtual viewport, without a Top10 cut. Feishu interactive cards/templates retain encrypted
secrets, durable delivery and successful-ACK cooldown semantics. The separate production rows below record real
deployment, business-instance, browser and notification acceptance without treating source checks as live proof.

| Current UI source acceptance layer | Evidence and limits |
| --- | --- |
| Final source and build | Panel-readability source `ed444317` passed **141/141 frontend unit tests**, TypeScript/Vite and version `0.2.8` embedded webui builds. The three self-metric mappings cover Go heap allocation, goroutines and cumulative HTTP requests, including native derived-rate naming without relabeling raw-counter totals as rates. This is source/build acceptance, not a new public release. |
| Final focused browser run | The same **9 cases passed without retries in 29.4 s**, with actual visibility of all three friendly default-overview self-metric legends. Coverage includes stat/gauge/bargauge, per-value time/null/zero, simple 12-series charts and explicit units, 390/768/1280 widths, percent/bytes dual axes, 20-series interaction, SQL Classic/V1/V2 comparison and bilingual workspace views. The full suite was **not rerun**; earlier CI/Docker results are not reassigned to this source. |
| Intermediate source and complete browser run | The preceding `c79971b8` passed 139 frontend units and TypeScript/Vite/embedded builds. That UI-patch cycle's first complete browser run recorded **98 cases: 93 passed, 2 SQL-environment skipped, 3 failed**. Corrections addressed an SDK comparison suffix, a fixture request ID and an obsolete raw-PromQL main-view assertion. Its nine focused browser cases subsequently passed without retries in **23.6 s**; the complete run was not a success and was not rerun. |
| Intermediate private deployment | Fixed-source formal-pipeline build and deployment version **0.2.7 succeeded**, including the same-source native token/local gate, full Go tests, 139 frontend units and embedded UI build. Actual checks confirmed health version `0.2.7`, build source `c79971b8`, non-root runtime and protected environment/control/identity files, the preserved Admin session, five business instances reporting within 9–15 s, fifteen-day retention, Chinese uptime help and risk lines. Private operational identities and artifact details remain outside this ledger. |
| Failed private deployment 0.2.8 | Deployment `0.2.8` failed during preparation of the release rollback archive **before service stop**. The healthy `0.2.7` service was not stopped or replaced, and no runtime rollback occurred. The failure stage is established; stderr is unavailable, so no specific tar warning is asserted. This failure is not complete live readability acceptance. |
| Express233 delivery dependency | Public fix [`c80ce429`](https://github.com/neko233-com/express233/commit/c80ce429972b02cbdd41861dd4170179c46ff008) was pushed. The release rollback archive's `data/`, `logs/` and `run/` exclusions are anchored to the release root, retaining nested release resources; consistent database backup still uses the separate stopped-data snapshot. Isolated real Linux scripts passed **10 executions across two repetitions, zero skips, 4.301 s**, including actual code/config rollback without reverting data. These fixtures are not a production rollback. Its six CI checks remain **pending**. |
| Prepared next private deployment | Parameters for new immutable deployment **0.2.9** are prepared, **not triggered**, pending the Express233 CI gate. Final deployment and actual main-page legend/runtime/reporting acceptance remain **pending**. This does not reassign the `0.2.7` intermediate proof or claim a new public server tag/archive/CI outcome. |

| Preceding v0.2.6 delivery acceptance layer | Completed evidence and limits |
| --- | --- |
| Embedded browser and frontend, preceding baseline | The preceding runtime/web passed the final Chromium suite: **93 passed, 2 SQL-environment skipped, 0 failed**, no retries. TypeScript and 120 frontend unit tests passed. Business-instance snapshot/restart/partial-data cases and Feishu previews are included. These preceded the fixture and notification corrections; the two skips are not browser passes. Aggregate CI at `f83d328e` separately passed its 120 frontend units/build. |
| Actual databases and SQL browser | Formal Dockerfile combined command, two real database rounds: **620.300 s**, with database rounds **224.30 / 192.50 s**. All three SQL browser cases ran each round: **6/6 passed**, no skips/retries. These cover actual MariaDB/PostgreSQL and the documented collector/storage regressions. |
| Preceding final runtime | After the server-only current-counter correction, the formal Dockerfile was rebuilt for two additional runtime/Chromium rounds: **173.509 s**, rounds **77.43 / 96.03 s**. A 571-file build-input SHA manifest stayed unchanged. Both passed token/local roles, HTTPS Origin over an HTTP upstream, ingestion/query, restart persistence and SHA-checked fresh-volume recovery as UID 10001 with read-only root, dropped capabilities, one CPU and 256 MiB. All OOM checks were false; owned resources were cleaned. This input preceded the v0.2.6 notification fix. |
| Database source attribution | The last correction touched only the business API's current cumulative query. Storage, collector, database schema and database regression code were unchanged. The earlier two database rounds retain that earlier-input attribution; the final runtime was rebuilt and repeated, without claiming a third database run. See [Docker validation](../metrics233-server/docs/docker-validation.md). |
| Focused Linux race | Notification/template/cooldown vet and race checks passed **80 top-level executions across two repetitions**, no failures/skips/races. This was a focused scope; the full 45-minute Docker validation target was **not** rerun locally in this cycle. The older complete-race evidence below belongs to its older source. |
| Final v0.2.6 alerting race | Exact final `85630554` source passed Linux Go 1.27.0/GCC vet and `internal/alerting` race tests with count 2: **320 top-level executions, zero skips/races**, package **101.870 s**, total **115.273 s**. This covers the timeout-classification fix; it is separate from the earlier 80-execution notification/store scope. |
| Earlier v0.2.4 clean archives | Two independent clean-source five-platform builds at `527494a6` produced all seven delivery files identically. ZIP CRC, binary architecture, BUILDINFO and Go-module provenance checks passed. That actual Linux amd64 archive passed confined authentication, business ingestion/query and graceful stop. This remains v0.2.4 artifact evidence; foreign-platform archives were inspected, not executed. |
| Earlier v0.2.5 actual archives | The official Windows and Linux amd64 v0.2.5 archives provided real minimal-health/authentication evidence for the private readiness contract. The corrected formal Linux deployment hook passed token/local modes twice each, with UID/GID 10001, application capabilities 0, exact PID/executable, full data/environment/key snapshot checks and TERM exit 0. This is isolated archive acceptance, not a v0.2.6 artifact or live upgrade result. |
| Preceding v0.2.6 archives | Two independent clean-source five-platform builds at `85630554` passed in **83.478 / 74.324 s**, with both checkouts clean before and after. All five ZIPs plus PROVENANCE and SHA256SUMS matched: **7/7 identical hashes**. Both rounds passed ZIP CRC, four-entry layout, BUILDINFO/source/frontend/binary hash, Go module and five-architecture inspection. Foreign-platform binaries were inspected, not executed. |
| Actual v0.2.6 Linux archive | The actual Linux amd64 A archive passed local Admin login, ingest-only credentials across all five protocols, 20 negative privilege checks including blocked-source self-unblock 403, source policy, persistence/restart and TERM exit 0. It ran as UID 10001, read-only root, dropped capabilities, no-new-privileges, one CPU and 256 MiB; no OOM. Sampled cgroup memory.current was 66,396,160 bytes, not peak RSS. Owned container/volume and the newly pulled Alpine tag were removed. This bounded fixture does not claim database/browser reruns or production acceptance. |
| Preceding official checkout at 77796f9c | A fresh recursive clone from official GitHub matched exact aggregate `77796f9c`, server `85630554`, Go `4f63688e` and Rust `f38673c6`; all four worktrees were clean. Its required-submodule public source/history checker completed with **exit 0**, without private configuration, a mirror or a local replacement. This verifies that earlier composition and documentation-only Rust pin; it does not verify the new UI source or repeat the runtime suites. |
| Preceding independent official checkout | A fresh recursive clone from the official GitHub aggregate matched exact `f83d328e`, server `85630554`, Go `4f63688e` and Rust `bda47165`; the parent and all three children were clean. Its required-submodule public source/history checker completed with **exit 0**, without private configuration, a mirror or a local replacement. This remains evidence for that earlier Rust pin. |
| Earlier independent official checkout | A fresh recursive official GitHub clone at aggregate `1e4bccd` matched server `37bfa961`, Go `4f63688e` and Rust `bda47165`; all worktrees were clean. Its independent `go run ./tools/check-public/main.go --root . --history --require-submodules --allow-synthetic-test-ips` completed with exit 0. No mirror/local replacement or private deployment configuration was used. This checkout evidence retains its earlier composition attribution. |
| Preceding aggregate CI at 77796f9c | [Aggregate CI 39596963802](https://github.com/neko233-com/metrics233-all/actions/runs/39596963802) at exact `77796f9c` **passed**, including real Go and Rust SDK integration against pinned server `85630554`. The preceding [aggregate CI 39593382915](https://github.com/neko233-com/metrics233-all/actions/runs/39593382915) at `f83d328e` / Rust `bda47165` also **passed**: public checker, 120 frontend units/build, Go vet/race and real Go SDK/pinned-server integration, Rust 1.88 lint/tests and real Rust SDK/pinned-server integration. Its result retains that earlier composition attribution. |
| Preceding v0.2.6 CI: Go and build | [Server v0.2.6 CI 39593305372](https://github.com/neko233-com/metrics233-server/actions/runs/39593305372) at `85630554` completed **2/2 jobs successfully**. Full vet/race passed **20 tested packages**, with six packages reporting no test files; this is a package count, not an individual-test count. Alerting took **147.768 s**, store **1576.623 s**, full step **32 min 02 s**. Frontend **120/120** and release-script **3/3** checks passed. |
| Preceding v0.2.6 CI: browser and Docker | The same v0.2.6 CI ran **95 browser cases: 93 passed, 2 real SQL-environment skipped, zero failed/flaky**, in **7.6 min**. Those skips do not replace the separate earlier actual-database acceptance. Docker count 2 passed **6 top-level / 16 including subtests**, zero skips, **463.792 s**. These results belong to `85630554`, not the current UI source, and remain separate from the older local focused checks and archive/production layers. |
| Superseded CI outcomes | [Aggregate CI 39590069771](https://github.com/neko233-com/metrics233-all/actions/runs/39590069771) at `1e4bccd` **passed**. [Server v0.2.5 CI 39589926276](https://github.com/neko233-com/metrics233-server/actions/runs/39589926276) was **cancelled as superseded**, with Docker successful. [v0.2.4 CI 39587248305](https://github.com/neko233-com/metrics233-server/actions/runs/39587248305) **failed** on the alerting timeout-classification race; its Docker job and 20 min 59 s store race package passed. These are not full CI successes. |
| Release download | [Server v0.2.6](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.6) is **public**, published **2026-10-21 12:26:23 UTC**. All seven official asset URLs downloaded successfully without authentication using normal TLS. **7/7 SHA-256 hashes matched both local build A and GitHub asset digests**. SHA256SUMS contained six verified entries; PROVENANCE contained five artifacts with version `0.2.6`, source `85630554` and `sourceDirty=false`. This is completed public-download acceptance, separate from archive inspection and production deployment. |
| Production upgrade and dashboards | The corrected private pipeline completed its same-source native token/local gate, full source tests/build and immutable **v0.2.6** deployment from public `85630554`. Actual health/version and the service manager confirmed the non-root runtime. Exactly one gateway and four logic instances reported fresh data; persisted 15-day retention, numeric uptime, host CPU/memory warning/critical 85/95 and disk 80/90 curves, and five live-template bindings were applied. Provisioning made 12 intended changes; a second run made zero API mutations. All five instances continued reporting every 15 seconds after account cutover and restart. The preceding failed readiness upgrade and healthy v0.2.3 restoration remain historical rollback evidence. |
| Real Feishu delivery and cooldown | Live, real-clock acceptance completed **24 transitions** across two cycles for three owned temporary resource rules: exactly one grouped firing and one grouped recovery were sent, with HTTP 200 and explicit successful business ACK. The second cycle within the configured one-hour cooldown sent neither another firing nor an orphan recovery. All three owned rules were deleted and independently checked absent; formal resource thresholds/durations were unchanged. Read-only API/SQLite reconciliation confirmed two groups of three resources and the current Feishu interactive format. Final signed wire JSON is not persisted, so this is not a claim of a stored historical card body or visual inspection of the group. |
| Production account separation and persistence | The account-mode cutover completed with version/CAS checks and preserved the encryption key and ingest credential; the legacy management token was cleared. HTTPS checks returned two expected 200s, nine expected 401s, one expected 403 and one expected rate-limit 429. Each of the five public ingest paths rejected anonymous requests with 401. A normal restart preserved database/WAL/SHM/identity permissions at 0600, data directory 0700, service-owned runtime directory 0750, stored data and account login. No production account name or credential is recorded here. |
| Actual production browser and business data | A real Admin account logged in through the normal browser flow. The Chinese overview showed 15-day retention, numeric uptime and the configured CPU/disk threshold lines. The SQL Entity list loaded **106/106 actual rows**, with descending average duration and current/window definitions checked. The current authoritative population was zero and the region list explicitly showed its empty snapshot; no player locations were invented. These are real scoped UI/data checks, not a sustained-load or nonzero-geography capacity claim. |
| Real player lifecycle scope | Real test-account login/logout is **not performed at the user's request**; no test account is required to continue. Nonzero player-lifecycle and regional-aggregation acceptance is not claimed. Fresh game-instance reports and an accepted zero-player snapshot do not establish that end-to-end player exercise. |

The preceding runtime image, before the v0.2.6 notification fix, measured 44,862,017 content bytes (42.8 MiB) and a 163 MB local Docker footprint.
These are different image-store measurements. Resource snapshots and bounded ingestion tests are workload-specific,
not a universal capacity claim. Local expiry fixtures advance a controlled clock; live cooldown acceptance used
real time across two cycles within one hour, not a one-hour waiting period. Production addresses, host identities, protected configuration and control-plane URLs stay outside
all four public repositories.

## Earlier release and deployment evidence

| Boundary | Current evidence |
| --- | --- |
| Server module migration | All 649 source files were copied and verified before the original paths were removed. Imports and module now use `github.com/neko233-com/metrics233-server`; CLI and HTTP contracts are preserved. |
| Server checks | Windows full Go vet/tests passed after the module rename. Formal Docker runtime was rebuilt from the child and passed token/local auth, HTTPS proxy, roles, business ingest, restart and full-data restore; SQL browser cases are covered by the earlier repeated database run. See the server's `docs/docker-validation.md`. |
| Server v0.2.1 artifacts | Published from clean `f0244c3`; five-platform archives built twice, all seven delivery files byte-identical. Ten ZIP checks and five binary architecture/Go module checks passed. All seven official assets were downloaded again and matched local SHA-256 and GitHub asset digests. |
| Server v0.2.1 runtime | The actual Linux amd64 archive passed token authorization, three business samples, native query, PromQL sum and SIGTERM (exit 0 in 0.505 s, no OOM) as UID 10001 with read-only root/binary bind, no capabilities, one CPU and 256 MiB. Windows CLI version returned 0.2.1; macOS/ARM64 archives were inspected, not executed. |
| Server immutable CI | The earlier v0.2.0 manual run timed out at the 20-minute package deadline during the real 5,000,001-row SQLite retention fixture. v0.2.1 preserves that fixture with 45/60-minute package/job budgets; its complete vet/race, subsequent Playwright and repeated Docker integration jobs all passed. |
| Go SDK v0.1.0 | Standard library only, Go 1.23+, fluent Counter/Gauge, atomic registration batches, bounded delivery, context deadlines/retry and explicit Run/Close. Tag CI passed Go 1.23/1.27 vet/race. A fresh public `go get` with isolated caches, the official module proxy/checksum database and no replace directive passed build and real HTTP Push/Close checks. |
| Aggregate checkout and CI | Both the initial `328c8ca` composition and the updated `813c381` composition passed fresh recursive checkout and aggregate CI. The updated gitlinks are approved server `f0244c3` and SDK `5bb1f54`. |
| Public release gate | Working files, staged blobs, reachable main history and exact submodule identities are checked without printing matched secrets. The current gate passed with required submodule HEADs and an external private deny-file; ignored user-owned files are preserved. |
| Private operations | The single private pipeline built fixed source `f0244c3`, verified the immutable content and provenance, and completed an actual Express233 deployment to the reviewed unique target. Toolchain, source transport, Unix socket test paths and readiness API issues found by real execution were corrected without removing tests. No private deployment repository is a public submodule. |
| Actual target acceptance | HTTPS served the token-login UI and public health 200; anonymous queries returned 401. The process ran as a non-root systemd service with a matching PIDFile and a loopback-only listener. Published SDK v0.1.0 pushed synthetic counter snapshots 100/104/113 and gauge 42/42/50; authenticated PromQL returned 113/50. A normal restart changed PID and preserved those same stored values, encryption key and protected environment. This is synthetic acceptance, not evidence of production game instrumentation or sustained-load capacity. |

The earlier v0.2.2 aggregate revision pinned server `ecdf079d5224600e49566ceac5040980ad4e76d4` and SDK
`4f63688e023bce29b1e6b79c14733e246bc277b6`; both child commits are publicly available with matching remote SHA
readback. The original repository is now `metrics233-all`. Its previously verified `328c8ca` composition pinned
server `6d8b8ea`; that CI result remains evidence for the earlier composition.

Public evidence:

- [Server v0.2.1 release](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.1),
  [provenance](https://github.com/neko233-com/metrics233-server/releases/download/v0.2.1/PROVENANCE.json) and
  [checksums](https://github.com/neko233-com/metrics233-server/releases/download/v0.2.1/SHA256SUMS).
- [Aggregate CI 39538200803](https://github.com/neko233-com/metrics233-all/actions/runs/39538200803): success at
  `328c8ca312bbf181c3a1b0cfbe741caf0604b857`.
- [Updated aggregate CI 39542900770](https://github.com/neko233-com/metrics233-all/actions/runs/39542900770): success at
  `813c38131ac6798b85eba3642cb2e7b2411b8eca`.
- [SDK v0.1.0 release](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0) and
  [tag CI 39539185012](https://github.com/neko233-com/metrics233-lib-go/actions/runs/39539185012): success at `5bb1f54`.
- [Server v0.2.1 CI 39541420544](https://github.com/neko233-com/metrics233-server/actions/runs/39541420544): complete
  success for both test and Docker jobs. The hosted timeout and prior local result are separately recorded
  in [Docker validation](../metrics233-server/docs/docker-validation.md).

Private reverse-proxy replacements have their own configuration, backup and per-site acceptance boundary;
the monitoring stack's runtime acceptance does not establish correctness for unrelated sites or gateways.
Performance measurements and protocol subsets remain workload-specific; see the server contracts for limits.
No production hostname, host identity, credential or control-plane URL is part of this ledger.

## Reporting access and notification controls

The new pins add independent ingest-only credentials, durable canonical-IP source observations and Admin policies,
the Reporting sources tab, encrypted Feishu contacts and durable firing cooldowns. The SDK API remains compatible
with v0.1.0; its README now directs senders to the restricted credential.
Final Windows Go tests passed; frontend build and 118 unit tests passed; the final embedded-UI browser suite completed
with **86 passed, 2 SQL-environment skipped, no retries or failures**. These counts do not convert the SQL skips into passes.
The v0.2.1 evidence above remains attributed to that earlier release. New v0.2.2 evidence:

- Five-platform builds from clean `ecdf079` ran twice; all seven files match byte-for-byte. All ten ZIPs passed CRC,
  BUILDINFO, binary architecture and Go module inspection. All seven official uploaded assets were downloaded and
  matched the local hashes and GitHub asset digests. Foreign macOS/ARM64 binaries were inspected, not executed.
- Frozen-source Linux targeted vet/race ran twice: 74 executions passed, no failures/skips/races. The actual Linux
  amd64 release archive separately passed 17 confined-runtime checks, including ingest-only authorization,
  block/no-self-unblock, query, persistence catalog and SIGTERM; sampled memory was 16.89 MiB for that bounded fixture.
- Fresh public recursive checkout matched both new pins. Updated aggregate
  [CI 39563273080](https://github.com/neko233-com/metrics233-all/actions/runs/39563273080) passed at `c306889`.
- The same fixed application source completed the real private production pipeline, public HTTPS restricted-token
  SDK push and management query (113/50), then normal-restart readback with unchanged protected configuration/key.
  Six management GET requests returned 200, eight restricted-credential privilege probes returned 403, and two
  invalid-credential probes returned 401; source policy was unchanged.
- The encrypted Feishu receiver was configured privately as the default, and one real test notification reached
  explicit HTTP/business success. There were zero production alert rules at acceptance: new UI defaults are one hour,
  but no CPU/disk thresholds or game instrumentation were invented. Deterministic scheduling tests establish CD
  timing/restart semantics; this is not a claim of an hour-long live alerting exercise.
- Tagged server [CI 39562946865](https://github.com/neko233-com/metrics233-server/actions/runs/39562946865) completed
  successfully: full vet/race (store 1564.678 s), embedded Chromium 86 passed / 2 SQL-environment skipped in 7.1 min,
  and repeated real Docker database/production runtime integration. No flaky/failing browser case was reported.
  The [v0.2.2 release](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.2) is public with five archives,
  checksums and provenance. The production artifact uses a separate immutable operations version for the same source.
