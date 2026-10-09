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
It also persists 64-dimensional metric-window embeddings with an HNSW similarity index. SQLite WAL stores metric data and exact-search vectors in lightweight mode, and control data in both modes.
Both modes survive application restarts; ClickHouse data survives database restarts. Persist both stores and back them up.
Changing retention on an existing ClickHouse table requires an explicit `ALTER TABLE ... MODIFY TTL`.

Native queries support last / avg / sum / min / max / reset-aware rate, label equality filters, a maximum 31-day range, 200 series and 2,000 buckets.
PromQL uses the official engine with a controlled in-memory storage adapter (maximum one million samples / 10,000 selected series). It does not yet push PromQL into ClickHouse.
Each ingestion batch / exporter scrape is limited to 10,000 samples / 4 MiB; eight scrapes may run concurrently.
This is a single-instance implementation, with no claimed distributed or production-scale throughput guarantee. Integration tests report measured batch and query timings.

## Grafana compatibility

Grafana's Prometheus datasource can use `http://localhost:7333/prometheus` with the workspace Bearer Authorization header.
Instant/range query, labels, label values, series, metadata, targets and buildinfo endpoints are implemented using the upstream PromQL engine.

Import Classic, V1 and V2 dashboard resources through the UI or `metricspanel dashboards save --file grafana.json`.
Panel targets, refIds, instant/range queries, legends, gridPos, fieldConfig and transformation contracts are retained.
Limits are 500 panels and 32 queries per panel. Panels query only near the visible viewport; the official renderer dependency loads only for templates.
Import tests include a pinned community Node Exporter Full dashboard.
Supported renderers include graph / timeseries, stat / singlestat, table, gauge, bargauge, sanitized HTML / Markdown text and rows.
The official `@grafana/data` package executes standard transformations, reducers, units, threshold colors and value mappings.
Variables support label_values, label_names, metrics, query_result, regex/sort, custom/constant/textbox/interval, multi-select, All,
repeated panels, common interval/range macros, variable formats and var-NAME URL parameters.
Original Grafana resources are preserved and exportable with `dashboards export --id ID --format grafana`.
The [Go runtime template](examples/dashboards/go-runtime.json) shows real metrics with an instant table transform, gauge and timeseries.

Dashboard-as-code tooling can use POST /api/dashboards/db, GET / DELETE /api/dashboards/uid/{uid} and GET /api/search.
Version conflicts and overwrite are supported. Datasource discovery, health and /api/datasources/proxy/uid/metricspanel/... are available.
These endpoints use the same workspace Bearer token. Dashboard writes currently support the root folder.

**Full Grafana ecosystem parity remains unfinished.** UI extension points, some app core services, legacy Angular plugins,
annotations, folders/organizations/permissions, library panels and the full backend API remain unfinished.
V2 Grid/AutoGrid become grids, Rows expand, Tabs display in document order; conditional visibility and row repeat are not evaluated.
Custom plotting options such as stacking and multiple axes are not all executed. Unknown transforms and plugin renderers are visible errors.
Full compatibility remains an objective, not a claim for this release.

## Grafana plugins and datasources

The Plugins page installs original ZIP packages or exact versions from the official catalog, enables/disables plugins and manages datasources.
Grafana's PGP signature and every file SHA-256 are verified by default. Reinstalling an identical package preserves its timestamp and creates no extra directories.
React panels share Grafana 13.2.3's public data/runtime/ui packages; AMD and SystemJS bundles are supported.
The unchanged official signed Clock 3.2.4 package is tested for rendering, live clock updates, reloads and mobile layout.

Application plugins support official `AppPlugin` roots, React configuration pages and routes under `/a/PLUGIN_ID/`.
Use Plugins → Configure to save ordinary JSON, encrypted secrets and a pinned navigation entry.
Saved secrets return only field-presence markers; omitted secrets are preserved. Native configuration writes require the current `version`
(initially 0), with HTTP 409 on conflicts. Grafana's legacy settings endpoint also accepts versionless writes.
Upgrades preserve enabled flags, child preferences, configuration and secrets. Disabling an app stops its whole package; disabling one child stops only that child.

```sh
metricspanel plugins settings --id APP_ID
metricspanel plugins configure --id APP_ID --file examples/plugins/app-settings.json
```

Replace the example's version with the current value returned by `plugins settings`. Supply new secrets through `secureJsonData`;
set individual `secureJsonFields` entries to `false` to clear them. Resources, health, Live and bundled datasource requests receive
official `AppInstanceSettings` with decrypted configuration and the update timestamp. Tests run real Go SDK app and bundled datasource processes,
covering routes, plugin-owned configuration pages, persisted settings, masked credentials and disable isolation.

```sh
metricspanel plugins catalog --id grafana-clock-panel --plugin-version 3.2.4
metricspanel plugins install --file grafana-clock-panel-3.2.4.zip
metricspanel plugins list
metricspanel plugins disable --id grafana-clock-panel
metricspanel plugins enable --id grafana-clock-panel
metricspanel datasources save --file examples/datasources/prometheus.json
metricspanel datasources health --id remote-prometheus
```

Backend plugins use the public Grafana Go SDK's gRPC protocol 2 for QueryData, QueryChunkedData, CheckHealth, CallResource, SubscribeStream, RunStream and PublishStream, including official DataFrame JSON/Arrow conversion.
Packages need `<executable>_<GOOS>_<GOARCH>[.exe]` for the host platform. Plugins using dynamic system libraries need those libraries installed.
At most eight backend requests run concurrently. Plugin processes do not inherit workspace tokens/database passwords and exit on server shutdown, disable or uninstall.
The datasource proxy supports HTTP(S), Basic Auth and secure custom headers. Plugin queries use `/api/ds/query`.

Datasource and application secrets are encrypted with AES-256-GCM. **Back up the control database, adjacent `secrets.key` and `plugins/` together.**
An existing datasource or application configuration database without its original key fails to open with a restore instruction.
Plugin assets use a short-lived cookie restricted to asset paths; that cookie cannot authorize data APIs.
`--plugins-dir` changes the package location; `--root-url` is checked for private signatures.
Development packages require an explicit `--allow-unsigned-plugin PACKAGE_ID` startup allowance.

UI extension points, complete datasource configuration editors, legacy Angular and some core services are still pending.
The CLI schema reports implemented and pending capabilities. Full compatibility remains the objective.

## Grafana Live and agent subscriptions

`/api/live/ws` implements the Centrifuge JSON WebSocket protocol. Channels use `ds/UID/path` or `plugin/ID/path`.
Plugins can call `getGrafanaLiveSrv()`; `DataSourceWithBackend` automatically connects frames with `meta.channel`.
The browser uses the official `StreamingDataFrame`, bounded buffers and the existing transforms/field formatting.
Streaming queries continue through dashboard refreshes; ordinary queries still refresh periodically.
Mixed panels merge response keys and refresh only the static queries while retaining active subscriptions.
Each channel shares one SDK RunStream, each page shares one socket, and the final unsubscribe cancels the backend stream.
Datasource updates discard the old context and trigger resubscription; deletion, plugin disable and server shutdown stop streams.

```sh
metricspanel live channels
metricspanel live watch --channel ds/MY_DATASOURCE/path --limit 10 --duration 1m
metricspanel live watch --channel ds/MY_DATASOURCE/path --metadata '{"key":"value"}' --limit 0 --duration 0
metricspanel live publish --channel ds/MY_DATASOURCE/path --file packet.json
```

Watch emits NDJSON with `type`, `channel`, `timestamp` and `data`. The initial frame counts toward the limit.
Defaults are 10 frames and one minute; zero disables either limit. Ctrl+C releases the subscription.
Subscribe failures and disconnects produce JSON errors on stderr and a nonzero exit code; agents decide whether to reconnect.
The plugin defines publication payloads and permissions. Subscribers on the same channel must use matching metadata.

Browser cookies last 15 minutes, authorize only the WebSocket path, and renew before reconnecting. The CLI uses a Bearer upgrade header.
Limits are 256 active channels, 128 channels per connection, 1 MiB per packet and 10,000 buffered frontend rows.
Live transport is transient; plugin publications are not automatically ingested. Collector/ingest SQLite and ClickHouse storage remains durable.

## Chunked queries and agent NDJSON

POST `/apis/{pluginId}.datasource.grafana.app/v0alpha1/namespaces/default/connections/{uid}/query` accepts the official SDK request DTO.
Global `from`/`to` support date math; queries may supply individual `timeRange` values. The default response is ordinary JSON.
`Accept: text/jsonl` calls SDK QueryChunkedData, converts Arrow frames to DataFrame JSON and immediately flushes each record.
The existing `/api/ds/query` also accepts this header with its original request format. Both routes enforce Bearer authentication and Origin checks.

```sh
metricspanel datasources query --id metricspanel --file examples/datasources/query.json
metricspanel datasources query --id MY_DATASOURCE --file query.json --stream
```

Stream records contain `refId`, `frameId`, `frame` or `error`/`errorSource`. The first record for each `(refId, frameId)` includes schema;
later records append data. The CLI runs until EOF without a default frame-count cutoff. Partial errors preserve stdout data and finish with JSON stderr and exit 1.
Ctrl+C cancels the backend request. Older plugins fall back to QueryData only when the streaming RPC is unimplemented before any record.
The browser exposes the public BackendSrv.chunked raw-byte Observable; plugins own parsing and appending. Finite queries are not restarted by dashboard refreshes while active.
Limits are 32 queries, 1024 frames, 8 MiB per chunk, 32 MiB per request, 60 seconds on the server and four concurrent datasources.
The CLI's `--duration` defaults to one minute; zero disables its deadline while server limits still apply.

## Vector pattern analysis

Run a metric query in Explore, save its current window, select a saved reference and find similar windows.
`shape` removes mean/scale; `raw` preserves measurement levels. Counter windows can use reset-aware `rate`.
Windows become 64-dimensional Float32 vectors; comparison charts align vectors to the reference window's timeline.
This is numerical waveform analysis, without an LLM embedding model. L2 distance is smaller for closer matches; it is not a probability or an automatic anomaly verdict.

```sh
metricspanel patterns capture --metric metricspanel_memory_bytes --range 30m --normalization shape
metricspanel patterns list --limit 1000
metricspanel patterns search --id PATTERN_ID --metric metricspanel_memory_bytes --limit 10
metricspanel patterns search --id PATTERN_ID --exact
metricspanel patterns get --id PATTERN_ID
metricspanel patterns delete --id PATTERN_ID
```

Explicit `--start` / `--end` are Unix milliseconds. Identical windows/data produce the same content ID, so repeated saves do not add logical duplicates.
Windows must span 64 seconds–31 days, contain observations in at least 48/64 buckets, and have no gap larger than eight buckets.
Small gaps are interpolated only for the embedding; raw metrics remain unchanged. Native metric/label capture is supported; arbitrary PromQL-derived capture is not yet available.
ClickHouse 26.8 uses persistent HNSW with candidate rescoring. Selective filters can trigger exact scans or yield fewer approximate results.
`--exact` disables approximate indexing. SQLite uses exact top-K, capped at 50,000 filtered vectors per search.
Saved vectors remain until explicitly deleted, independently of metric TTL. Both vector stores and the ClickHouse index survive tested restarts.

## Durable alerts and recording rules

The Alerts page creates, edits, pauses and evaluates PromQL rules, with individual label-instance states and recent history.
The scheduler executes at most four queries concurrently. Configuration, pending/recovery timers and the latest 100,000 transitions persist in the SQLite WAL control database for both metric backends.
API updates require the returned `version`; edits reset instances and record a resolution transition.

```sh
metricspanel alerts save --file examples/alerts/runtime-memory.json
metricspanel alerts evaluate --id runtime-memory
metricspanel alerts history --id runtime-memory --limit 100
metricspanel alerts import-grafana --file grafana-rule.json
```

`condition=presence` follows Prometheus semantics: returned samples fire, including zero-valued samples.
`condition=nonzero` evaluates booleans; the UI uses expressions such as `up == bool 0`.
Rules support pending and keep-firing periods, no-data/error policies and durable per-instance history.
Prometheus-compatible `/prometheus/api/v1/rules` and `/alerts` expose the rules and active instances.
Set `record` to a metric name to store query results as a recording rule.

Grafana provisioning rule CRUD and group GET accept local Prometheus queries, strict reduce, threshold, single-reference math and one classic condition.
Map datasource UIDs to `metricspanel` before import. Unknown nodes, query cycles, notification settings and recovery thresholds are rejected.
The original query graph is preserved for export. Full Grafana alerting compatibility is still incomplete: multiple-reference math and label-subset joins, compound classic conditions, atomic group updates, annotation templates, external notifications and Alertmanager remain outstanding.
Native expression/condition/record changes discard an obsolete Grafana graph. The CLI accepts GET output directly when updating with `alerts save --id UID --file rule.json`, retaining its optimistic-concurrency version.

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
Four concurrent writers additionally ingest 100,000 SQLite samples and 1,000,000 ClickHouse samples; the suite reports batch p50/p95 and 24h aggregation time.
These bounded workloads do not establish million-series or sustained production performance.
The suite also stores 10,000 x 64-dimensional vectors, verifies HNSW use through EXPLAIN, measures recall@10 against exact search, and rechecks the index plan after database restart.
It uses a fixed isolated Compose project, random mapped ports and an automatically released concurrency lock.
Each run deletes its own containers, volumes, network and application image tags. Shared base images and BuildKit cache remain reusable.

After building the frontend and the CLI binary, run `cd web && npx playwright install chromium && npm run test:e2e`.
Browser tests use a temporary database and clean it after verifying both languages, panels, collectors, PromQL, resource templates,
official transforms, percentage units, repeated panels, pattern capture/search, special-character variables, text sanitization and mobile layout.
GitHub Actions runs these checks plus two Docker integration runs.
CI pulls test images by pinned digest from [Google Cloud's public cache](https://docs.cloud.google.com/artifact-registry/docs/pull-cached-dockerhub-images) to reduce Docker Hub rate-limit failures.
`compose.test.yml` accepts `METRICSPANEL_TEST_CLICKHOUSE_IMAGE`, `METRICSPANEL_TEST_MYSQL_IMAGE` and `METRICSPANEL_TEST_EXPORTER_IMAGE` source overrides; production Compose has its own configuration.

Apache-2.0. See the [Chinese README](README.md) for API details, environment variables, architecture and limitations.
