# Delivery evidence

Updated: 2026-10-21. The aggregate pins independently versioned public server and Go SDK repositories.

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

This aggregate revision pins server `ecdf079d5224600e49566ceac5040980ad4e76d4` and SDK
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
New v0.2.2 archives, tag CI, recursive aggregate acceptance and production configuration are separate pending gates;
the v0.2.1 evidence above remains attributed to that earlier release.
