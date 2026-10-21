# Delivery evidence

Updated: 2026-10-21. The aggregate pins independently versioned public server and Go SDK repositories.

| Boundary | Current evidence |
| --- | --- |
| Server module migration | All 649 source files were copied and verified before the original paths were removed. Imports and module now use `github.com/neko233-com/metrics233-server`; CLI and HTTP contracts are preserved. |
| Server checks | Windows full Go vet/tests passed after the module rename. Formal Docker runtime was rebuilt from the child and passed token/local auth, HTTPS proxy, roles, business ingest, restart and full-data restore; SQL browser cases are covered by the earlier repeated database run. See the server's `docs/docker-validation.md`. |
| Go SDK | Standard library only, Go 1.23+, fluent Counter/Gauge, atomic registration batches, bounded delivery, context deadlines/retry and explicit Run/Close. Tests, vet and a real server binary integration are maintained in its own repository. |
| Public release gate | Scans public worktrees and Git index/history without printing matched secrets. Checks the exact two public submodule identities and commits. Real deployment literals can be supplied through a private deny-file outside this checkout. |
| Private operations | Deployment source, production identities and environment configuration live in a separate private repository. Its pipeline is the only production release entry point; no deployment repository is a public submodule. |

The original GitHub repository is now `metrics233-all`. Server `6d8b8ea4242ef9181798d63a804fd2f98811db41` and SDK
`5bb1f54eb4ab93f1d648ad2b73df8a17d686a6e0` have been published with matching remote SHA readback and registered
as real gitlinks. The SDK also passed actual Go 1.23.12 Linux vet/tests/race with networking disabled and a real
renamed-server Windows binary integration. The public gate passed with reachable history, index, submodule HEAD
checks and an external private deny-file. Fresh recursive-clone, CI and the target HTTPS/runtime results are
recorded when completed. Local test evidence does not imply that a remote deployment has succeeded. Performance measurements and
protocol subsets remain workload-specific; see the server contracts for limits.
