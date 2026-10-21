# metrics233-all

**English** · [简体中文](README-CN.md)

A single-node performance and business monitoring stack, with independent Go and Rust instrumentation SDKs.

| Submodule | Purpose |
| --- | --- |
| [metrics233-server](https://github.com/neko233-com/metrics233-server) | Collection, SQLite time-series storage, dashboards, Explore and alerts in one native executable; optional ClickHouse storage. |
| [metrics233-lib-go](https://github.com/neko233-com/metrics233-lib-go) | Zero third-party dependencies; fluent Gauge/Counter instrumentation, bounded batching, explicit push lifecycle and retries. |
| [metrics-lib-rust](https://github.com/neko233-com/metrics-lib-rust) | Rust crate `metrics233`: fluent Gauge/Counter, bounded HTTPS batching and explicit start/flush/shutdown; Rust 1.88+. |

## Get the complete stack

```sh
git clone --recurse-submodules https://github.com/neko233-com/metrics233-all.git
cd metrics233-all
```

For an existing clone, run `git submodule update --init --recursive`. The aggregate pins exact child commits; each child also works independently.

## Run the server

The latest public five-platform release remains [server v0.2.6](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.6).
Its two clean five-platform builds match, and archive inspection plus actual Linux archive acceptance passed.
All seven public assets passed official download verification against both the local build and GitHub asset digests.
Verify your downloads with the attached `SHA256SUMS` and `PROVENANCE.json`, or build the pinned source below.
The independently released [Go SDK v0.1.0](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0)
requires Go 1.23+ and has no third-party dependencies.

The [Rust SDK v0.1.0 Git release](https://github.com/neko233-com/metrics-lib-rust/releases/tag/v0.1.0)
is verified separately. The registry upload is blocked by HTTP 403; use the immutable Git dependency below.
The current Rust submodule `f38673c6` only corrects its README; the v0.1.0 code/tag remains `bda47165`:

```toml
metrics233 = { git = "https://github.com/neko233-com/metrics-lib-rust", rev = "bda4716538a366379f69300cb816e272f1db11de" }
```

```sh
cd metrics233-server/web
npm ci
npm run build
cd ..
go build -tags webui -o bin/metricspanel ./cmd/metricspanel
./bin/metricspanel serve --auth=local --open
```

The executable embeds the UI and uses SQLite by default. Its existing `metricspanel` CLI, `METRICSPANEL_*` environment variables and HTTP endpoints retain their compatibility contracts. See the [server guide](metrics233-server/README.md), [business monitoring](metrics233-server/docs/business-monitoring.md), and [SDK quick start](metrics233-lib-go/README.md).

The server supports a **Reporting sources / 上报来源** tab with one-click block, reset and allowlist policies.
Use a separate reporting-only token for business senders. Encrypted Feishu contact points and durable per-instance
notification cooldowns are supported; newly created UI rules default to one hour. See
[reporting access](metrics233-server/docs/reporting-access.md),
[source policies](metrics233-server/docs/reporting-sources.md) and
[alert cooldowns](metrics233-server/docs/alert-notification-cooldown.md).

Local account/password login is separate from reporting credentials. Retention defaults to fifteen days.
Business instances retain their complete scope, inherit live dashboard templates or use independent copies;
player regions and SQL Entity statistics use full virtualized lists. Feishu notifications support cards,
editable bounded templates and no-send previews. Agent HTTP discovery documents roles, schemas and CAS updates.

## Verification

Run Go/frontend commands inside the owning child. The [execution plan](PLAN.md) and [status ledger](docs/status.md) distinguish unit, browser, Docker, archive and online acceptance.

The current server source is [`ed444317`](https://github.com/neko233-com/metrics233-server/commit/ed444317090daa1c7cd6b76c70e0ca8ed4d208f0), the panel-readability correction verified in private deployment `0.2.9`; Go SDK `4f63688e` and Rust SDK `f38673c6` are unchanged.
Published aggregate [`8a63870b`](https://github.com/neko233-com/metrics233-all/commit/8a63870b9e4ba39df275b532a25a6a6115a95ef1) pins this exact composition.
An independent official recursive checkout matched all four clean worktrees and passed the required-submodule
public-source/history checker, exit 0; this is composition evidence, not a new CI or runtime result.
Its **141/141 frontend units**, TypeScript/Vite and version `0.2.8` embedded webui builds passed. The same **9 focused
browser cases passed without retries in 29.4 s**, including friendly default-overview legends for Go heap allocation,
goroutines and cumulative HTTP requests, with native derived-rate naming where applicable. Native rate labels are
not applied to cumulative raw-counter totals. The private deployment `0.2.8` attempt failed while preparing the
release rollback archive **before stopping the service**. The healthy `0.2.7` service was not replaced or subjected
to a runtime rollback. The public [Express233 archive fix](https://github.com/neko233-com/express233/commit/c80ce429972b02cbdd41861dd4170179c46ff008)
passed ten isolated Linux script checks across two repetitions, with zero skips, in 4.301 s. All six latest Express233
CI checks now passed: [CI attempt 2](https://github.com/neko233-com/express233/actions/runs/39612617923/attempts/2) and
[Lint](https://github.com/neko233-com/express233/actions/runs/39612617925) succeeded. The initial Windows API/store
packages hit a 600 s cumulative timeout; with those Git trees unchanged, one same-source retry of the failed Windows
job passed in 376 s of tests / 6 min 41 s for the job. The reviewed script was atomically installed with SHA verification,
root ownership and mode 0755, preserving a root-protected backup. Immutable deployment **0.2.9 succeeded** from `ed444317`,
passing same-source native token/local, full Go and embedded-UI gates. Actual HTTPS health/version/source, protected
non-root runtime and the existing Admin session were verified. The live overview now shows all three friendly
self-metric legends; Details exposes full identifiers, units, latest valid values and query time explicitly distinguished
from collection time. Numeric uptime, Chinese restart/time-zone help, CPU 85/95 and disk 80/90 lines, and fifteen-day
retention remained intact. All five business instances reported within **9–15 s** after reload; this is continued
reporting, not a real-player lifecycle test. Runtime readiness passed with the stopped-data/key snapshot retained,
and the successful deployment required no runtime rollback. The latest public five-platform server release remains v0.2.6.

The intermediate `c79971b8` source passed 139 frontend units, TypeScript/Vite and embedded webui builds. Its UI-patch cycle's first full browser run recorded
**98 cases: 93 passed, 2 SQL-environment skipped, 3 failed**. After correcting an SDK comparison suffix, a fixture request ID
and an obsolete raw-PromQL main-view assertion, the final **9 focused cases passed without retries in 23.6 s**.
They cover stat/gauge/bargauge, per-value time/null/zero, simple 12-series charts and explicit units, 390/768/1280 widths,
percent/bytes dual axes, 20-series interaction, SQL Classic/V1/V2 comparison and bilingual workspace views.
The complete browser suite was not rerun after those narrow corrections. The fixed-source private pipeline successfully
built and deployed version `0.2.7`, passing its same-source native token/local gate, full Go tests, 139 frontend units
and embedded UI build. Actual checks confirmed version/source, protected non-root runtime, preserved Admin session,
five fresh business instances, fifteen-day retention, Chinese uptime help and risk lines. This is **intermediate**
acceptance: the live self-metric legends still showed raw names because those three self metrics were missing from the known-metric map.
That missing-map correction is included in `ed444317` and was verified live in `0.2.9` as recorded above.
No new tag, public five-platform archive or server/aggregate CI success is claimed for this UI source.

The preceding published aggregate `77796f9c` pins server `85630554` (v0.2.6), Go SDK `4f63688e` and Rust SDK `f38673c6`.
Its [exact-composition CI](https://github.com/neko233-com/metrics233-all/actions/runs/39596963802) passed, including real Go/Rust SDK integration against server `85630554`.
The earlier `f83d328e` / Rust `bda47165` [aggregate CI](https://github.com/neko233-com/metrics233-all/actions/runs/39593382915)
passed, including both SDKs against the same pinned server; that success retains its earlier composition attribution.
The preceding v0.2.6 server fixes a notification timeout-classification race; targeted Linux vet/race passed **320 top-level executions**, zero skips/races.
The preceding runtime/web baseline passed Chromium **93 / 2 SQL-environment skipped**, two real database rounds with **6/6 SQL browser passes**,
two final Docker runtime rounds and 80 focused Linux race executions. Bootstrap checks under umasks 022/077 passed 20 executions,
zero skips. Those results retain their earlier source attribution. [v0.2.4 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39587248305)
failed on the notification timeout race; [v0.2.5 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39589926276)
was cancelled as superseded after its Docker job passed. The preceding [v0.2.6 server CI](https://github.com/neko233-com/metrics233-server/actions/runs/39593305372)
passed both jobs: full vet/race across 20 tested packages, 120 frontend units, three release-script checks,
Chromium **93 passed / 2 SQL-environment skipped**, and two Docker rounds. The browser skips are not passes;
the separate real-database results above retain their source attribution. The v0.2.6 release is public, and all seven official downloads match the verified build and asset digests.

The preceding production v0.2.6 acceptance passed deployment, account/password cutover, restart persistence and a real authenticated browser check.
The legacy management token was cleared while the reporting-only credential and encryption key were preserved.
One gateway and four logic instances continued reporting every fifteen seconds after restart; live templates,
fifteen-day retention, numeric uptime and host thresholds were verified. The SQL Entity list loaded all **106/106**
rows in descending average-duration order. An authoritative zero-player snapshot displayed an empty region list.
At the user's request, no real test-account login/logout exercise will be performed, and no test account is required to continue.
Nonzero player-lifecycle or geographic-distribution acceptance is not claimed.
A real-clock Feishu exercise sent one grouped firing and one grouped recovery with successful ACKs; its second cycle
within the one-hour cooldown sent neither another firing nor an orphan recovery. These production results are
separate from release publication and sustained-load capacity. The earlier failed upgrade and v0.2.3 rollback remain historical evidence.
The earlier v0.2.4 reproducible archives retain their own version attribution.

Server v0.2.2 at `ecdf079` passed [complete tagged CI](https://github.com/neko233-com/metrics233-server/actions/runs/39562946865):
full vet/race, Chromium (86 passed / 2 SQL-environment skipped) and repeated real Docker database/runtime checks.
Both five-platform builds and all seven official asset downloads matched. The deployed service passed restricted-token
SDK push/query, normal-restart persistence and a real encrypted Feishu notification test. Updated aggregate CI passed at `c306889`.

The earlier server v0.2.1 baseline was built twice from clean commit `f0244c3`; all seven delivery files were byte-identical, and all
official release assets passed download hash verification. Its actual Linux amd64 archive passed authentication,
business ingestion/query and graceful shutdown. Aggregate CI passed at `813c381`, SDK v0.1.0 tag CI passed, and
[server v0.2.1 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39541420544) passed full vet/race,
browser and Docker checks. The private deployment separately passed real SDK writes/queries and normal-restart
persistence; see the status ledger for scope and evidence.

```sh
cd metrics233-lib-go && go test ./... && go vet ./...
# From the aggregate root:
go run ./tools/check-public/main.go --root . --history --require-submodules --allow-synthetic-test-ips
```

Production deployment configuration is maintained in a separate private operations repository. Keep hostnames, SSH details, control-plane configuration, `.env`, keys and local databases out of all four public repositories. Public examples contain placeholders only.

Licensed under [Apache-2.0](LICENSE).
