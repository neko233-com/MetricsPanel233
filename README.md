# metrics233-all

**English** · [简体中文](README-CN.md)

A single-node performance and business monitoring stack, with a small Go SDK for application instrumentation.

| Submodule | Purpose |
| --- | --- |
| [metrics233-server](https://github.com/neko233-com/metrics233-server) | Collection, SQLite time-series storage, dashboards, Explore and alerts in one native executable; optional ClickHouse storage. |
| [metrics233-lib-go](https://github.com/neko233-com/metrics233-lib-go) | Zero third-party dependencies; fluent Gauge/Counter instrumentation, bounded batching, explicit push lifecycle and retries. |

## Get the complete stack

```sh
git clone --recurse-submodules https://github.com/neko233-com/metrics233-all.git
cd metrics233-all
```

For an existing clone, run `git submodule update --init --recursive`. The aggregate pins exact child commits; each child also works independently.

## Run the server

Download the five-platform [server v0.2.2 release](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.2)
and verify it with the attached `SHA256SUMS` and `PROVENANCE.json`, or build from source below.
The independently released [Go SDK v0.1.0](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0)
requires Go 1.23+ and has no third-party dependencies.

```sh
cd metrics233-server/web
npm ci
npm run build
cd ..
go build -tags webui -o bin/metricspanel ./cmd/metricspanel
./bin/metricspanel serve --auth=local --open
```

The executable embeds the UI and uses SQLite by default. Its existing `metricspanel` CLI, `METRICSPANEL_*` environment variables and HTTP endpoints retain their compatibility contracts. See the [server guide](metrics233-server/README.md), [business monitoring](metrics233-server/docs/business-monitoring.md), and [SDK quick start](metrics233-lib-go/README.md).

The current source adds a **Reporting sources / 上报来源** tab with one-click block, reset and allowlist policies.
Use a separate reporting-only token for business senders. Encrypted Feishu contact points and durable per-instance
notification cooldowns are supported; newly created UI rules default to one hour. See
[reporting access](metrics233-server/docs/reporting-access.md),
[source policies](metrics233-server/docs/reporting-sources.md) and
[alert cooldowns](metrics233-server/docs/alert-notification-cooldown.md).

## Verification

Run Go/frontend commands inside the owning child. The [execution plan](PLAN.md) and [status ledger](docs/status.md) distinguish unit, browser, Docker, archive and online acceptance.

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

Production deployment configuration is maintained in a separate private operations repository. Keep hostnames, SSH details, control-plane configuration, `.env`, keys and local databases out of all three public repositories. Public examples contain placeholders only.

Licensed under [Apache-2.0](LICENSE).
