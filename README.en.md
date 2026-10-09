# MetricsPanel233

English · [中文](README.md)

Simple self-hosted observability with **Go 1.27, Vite 7, React 19 and a native agent CLI**.
Collect Prometheus text metrics, push business samples, persist data and build dashboards in one workspace.
The UI supports English and Chinese. Production UI assets are embedded in the Go executable.

## Run locally

```sh
cd web && npm ci && npm run build
cd ..
go build -tags webui -o bin/metricspanel ./cmd/metricspanel
./bin/metricspanel serve
```

On Windows build `bin/metricspanel.exe`. Open http://127.0.0.1:7333. Real internal metrics are collected every five seconds.
SQLite is the zero-configuration default. For Docker and ClickHouse, copy `.env.example` to `.env`, set both secrets and run `docker compose up -d --build`.
Compose persists both the SQLite control plane and ClickHouse metrics in named volumes. `down` preserves data; `down -v` deletes it.

## Collect and query

```sh
metricspanel targets add --name mysql --url http://localhost:9104/metrics --interval 15s
metricspanel targets add --name backend --url http://localhost:8080/metrics --interval 5s
metricspanel ingest --file samples.json
metricspanel query --metric business_orders --range 30m --aggregation sum
metricspanel query --expr 'sum(rate(business_http_requests_total[5m]))' --range 1h
metricspanel schema
```

All client commands output JSON; failures output JSON on stderr and exit 1. Set `METRICSPANEL_URL` and `METRICSPANEL_TOKEN` for a remote workspace.
Non-loopback listeners require a token of at least 16 characters. Use an HTTPS reverse proxy across machines.

Push format: `{"samples":[{"name":"business_orders","labels":{"service":"api"},"value":233}]}`.
Timestamp is Unix milliseconds; omitted / zero uses server time. Duplicate series/timestamp writes replace the prior value.
The [Go example](examples/go-service/main.go) demonstrates both exporter and push integration. MySQL uses the official `mysqld_exporter` with `.my.cnf` credentials.

## Storage and performance

ClickHouse uses vectorized columnar execution, partitioned ReplacingMergeTree, compression, TTL and acknowledged durable batch inserts.
It is an analytical columnar database, not an embedding similarity database. SQLite WAL stores metric data in lightweight mode and control data in both modes.
Both modes survive application restarts; ClickHouse data survives database restarts. Persist both stores and back them up.
Changing retention on an existing ClickHouse table requires an explicit `ALTER TABLE ... MODIFY TTL`.

Native queries support last / avg / sum / min / max / reset-aware rate, label equality filters, a maximum 31-day range, 200 series and 2,000 buckets.
PromQL uses the official engine with a controlled in-memory storage adapter (maximum one million samples / 10,000 selected series). It does not yet push PromQL into ClickHouse.
Each ingestion batch / exporter scrape is limited to 10,000 samples / 4 MiB; eight scrapes may run concurrently.
This is a single-instance implementation, with no claimed distributed or production-scale throughput guarantee. Integration tests report measured batch and query timings.

## Grafana compatibility

Grafana's Prometheus datasource can use `http://localhost:7333/prometheus` with the workspace Bearer Authorization header.
Instant/range query, labels, label values, series, metadata, targets and buildinfo endpoints are implemented using the upstream PromQL engine.

Import classic Grafana dashboard JSON through the UI or `metricspanel dashboards save --file grafana.json`.
Supported panels: graph / timeseries, stat / singlestat, table; gauge / bargauge render as stat. Prometheus targets, rows, label_values variables,
custom / constant / textbox / interval variables, multi-select, All and common interval/range macros are supported.
Original Grafana JSON is preserved and exportable with `dashboards export --id ID --format grafana`.

**Full Grafana ecosystem parity is not implemented.** Custom plugins, non-Prometheus datasources, Grafana backend APIs, alerts, annotations,
transformations, repeated panels, complex variable queries and exact Grafana layout semantics remain outside the current compatibility scope.
The importer reports unsupported features. Full compatibility remains a future objective, not a claim for this release.

## Automated verification

```sh
go test ./... -count=1
go vet ./...
CGO_ENABLED=1 go test -race ./... -count=1
go test -tags=integration ./tests/integration -count=2 -v -timeout=25m
```

Or on Windows: `pwsh -File scripts/test-docker.ps1 -Repeat 2`.
The testify integration suite verifies real Go push/scrape, real MySQL exporter collection, both storage backends, duplicate writes,
application and ClickHouse restarts, Grafana queries/templates and resource cleanup.
It uses a fixed isolated Compose project, random mapped ports and an automatically released concurrency lock.
Each run deletes its own containers, volumes, network and application image tags. Shared base images and BuildKit cache remain reusable.

After building the frontend and the CLI binary, run `cd web && npx playwright install chromium && npm run test:e2e`.
Browser tests use a temporary database and clean it after verifying both languages, panels, collectors, PromQL, templates and mobile layout.
GitHub Actions runs these checks plus two Docker integration runs.

Apache-2.0. See the [Chinese README](README.md) for API details, environment variables, architecture and limitations.
