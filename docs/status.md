# Delivery evidence

Updated: 2026-10-21. The aggregate pins independently versioned public server and Go SDK repositories.

| Boundary | Current evidence |
| --- | --- |
| Server module migration | All 649 source files were copied and verified before the original paths were removed. Imports and module now use `github.com/neko233-com/metrics233-server`; CLI and HTTP contracts are preserved. |
| Server checks | Windows full Go vet/tests passed after the module rename. Formal Docker runtime was rebuilt from the child and passed token/local auth, HTTPS proxy, roles, business ingest, restart and full-data restore; SQL browser cases are covered by the earlier repeated database run. See the server's `docs/docker-validation.md`. |
| Server v0.2.1 artifacts | Published from clean `f0244c3`; five-platform archives built twice, all seven delivery files byte-identical. Ten ZIP checks and five binary architecture/Go module checks passed. All seven official assets were downloaded again and matched local SHA-256 and GitHub asset digests. |
| Server v0.2.1 runtime | The actual Linux amd64 archive passed token authorization, three business samples, native query, PromQL sum and SIGTERM (exit 0 in 0.505 s, no OOM) as UID 10001 with read-only root/binary bind, no capabilities, one CPU and 256 MiB. Windows CLI version returned 0.2.1; macOS/ARM64 archives were inspected, not executed. |
| Server immutable CI | The v0.2.0 manual full-race run failed at the 20-minute package deadline during the real 5,000,001-row SQLite retention fixture; no completed race pass can be inferred. v0.2.1 preserves the fixture and raises the package/job budgets to 45/60 minutes. Its Docker job passed; full race and subsequent browser checks are still pending. |
| Go SDK v0.1.0 | Standard library only, Go 1.23+, fluent Counter/Gauge, atomic registration batches, bounded delivery, context deadlines/retry and explicit Run/Close. Tag CI passed Go 1.23/1.27 vet/race. A fresh public `go get` with isolated caches, the official module proxy/checksum database and no replace directive passed build and real HTTP Push/Close checks. |
| Aggregate checkout and CI | The rename and initial fresh recursive clone passed at `328c8ca`; its aggregate CI passed. This revision updates the server gitlink to approved `f0244c3` alongside SDK `5bb1f54`; fresh recursive checkout and CI are verified for each published composition separately. |
| Public release gate | Working files, staged blobs, reachable main history and exact submodule identities are checked without printing matched secrets. The current gate passed with required submodule HEADs and an external private deny-file; ignored user-owned files are preserved. |
| Private operations | Reviewed deployment changes were pushed only to the separate private repository. A real run failed the Node 24 requirement before building/publishing; corrected attempts passed toolchain and target preflight but then failed outbound source fetching. Private network-path correction and live deployment remain pending. No private deployment repository is a public submodule. |

This aggregate revision pins server `f0244c3da55821be845359f00318174242064a6f` and SDK
`5bb1f54eb4ab93f1d648ad2b73df8a17d686a6e0`; both child commits are publicly available with matching remote SHA
readback. The original repository is now `metrics233-all`. Its previously verified `328c8ca` composition pinned
server `6d8b8ea`; that CI result remains evidence for the earlier composition.

Public evidence:

- [Server v0.2.1 release](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.1),
  [provenance](https://github.com/neko233-com/metrics233-server/releases/download/v0.2.1/PROVENANCE.json) and
  [checksums](https://github.com/neko233-com/metrics233-server/releases/download/v0.2.1/SHA256SUMS).
- [Aggregate CI 39538200803](https://github.com/neko233-com/metrics233-all/actions/runs/39538200803): success at
  `328c8ca312bbf181c3a1b0cfbe741caf0604b857`.
- [SDK v0.1.0 release](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0) and
  [tag CI 39539185012](https://github.com/neko233-com/metrics233-lib-go/actions/runs/39539185012): success at `5bb1f54`.
- [Server v0.2.1 CI 39541420544](https://github.com/neko233-com/metrics233-server/actions/runs/39541420544): Docker
  success; overall result pending at this update. The hosted timeout and prior local result are separately recorded
  in [Docker validation](../metrics233-server/docs/docker-validation.md).

Private reverse-proxy/TLS changes, full backups, runtime identity, real business writes/queries and recovery must
be verified on the actual target. These are pending; local tests, published archives and successful Docker jobs do
not establish online deployment. Performance measurements and protocol subsets remain workload-specific; see the
server contracts for limits. No production hostname, host identity or control-plane URL is part of this ledger.
