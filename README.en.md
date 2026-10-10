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
The [Go example](examples/go-service/main.go) demonstrates both exporter and push integration. MySQL can use the official `mysqld_exporter` with `.my.cnf` credentials or the native SQL collector.

Use `targets catalog` for twelve implemented collection paths and `targets exporters` for eighteen exporter presets. `targets add --file FILE|-` accepts native connections and encrypted `secure_settings.password` / `bearer_token`. Responses contain `secure_fields` only; omitted credentials are retained, empty values clear them. Preserve both the database and `secrets.key` in backups. URL credentials are rejected. Examples under `examples/collectors` start with automatic collection disabled.

| Integration | Implemented metrics interface |
| --- | --- |
| MySQL / MariaDB | `SHOW GLOBAL STATUS` |
| Redis | `INFO ALL`, ACL authentication and verified `rediss` TLS |
| PostgreSQL | `pg_stat_database` |
| ClickHouse | HTTP SQL over current / asynchronous metrics and events |
| Elasticsearch | `/_nodes/stats` numeric fields, labeled by node |
| Hadoop / YARN / HDFS | `/jmx` numeric attributes labeled by MBean |
| Hive / Kafka | Jolokia wildcard `read`; requires a configured agent |
| Spark | Configured MetricsServlet `/metrics/json` |
| Flink | REST metrics discovery and bounded batch reads at the configured scope |

HTTP collectors support Basic or Bearer authentication (Bearer takes precedence). SQL TLS modes are disable, require (encryption) and verify-full (certificate and hostname validation). Native metric names do not reproduce every third-party exporter contract; use the original exporter for its dashboards. Presets cover Node, Windows, Process, MySQL, Redis, PostgreSQL, Elasticsearch, Kafka, JMX, cAdvisor, Kubernetes, MongoDB, RabbitMQ, Nginx, Apache, Memcached, Blackbox and SNMP. Exporters run on monitored hosts; presets do not install them. Custom URLs accept any compatible exporter.

Prometheus text, OpenMetrics 1.0 and delimited protobuf support scalar samples and classic histograms/summaries. Native histogram samples fail explicitly; exemplars are not stored. Kerberos/SPNEGO, Sentinel discovery and automatic cluster fan-out remain unfinished.

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

**Full Grafana ecosystem parity remains unfinished.** Additional core UI extension points, some app core services, legacy Angular plugins,
folders/organizations/permissions, library panels and the full backend API remain unfinished.
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

Additional core UI extension points, complete datasource configuration editors, legacy Angular and some core services are still pending.
The CLI schema reports implemented and pending capabilities. Full compatibility remains the objective.

## Grafana UI extensions

App plugins register links, components, functions and exposed components through the official AppPlugin APIs.
Consumers use `usePluginLinks`, `usePluginComponents`, `usePluginFunctions`, `usePluginComponent` and the link/component Observable APIs.
Declared `plugin.json` extensions autoload their providers without opening the provider page. Per-plugin limits, configure-based hiding and overrides,
component `.meta`, synchronous/asynchronous functions, provider PluginContext and theme are supported. Link contexts are read-only.
Configuration changes preserve component state. Disable, uninstall and package changes remove old registrations and overlays; retained functions check the current package.

Native locations currently include dashboard panel menus, AppChrome, top-bar actions/buttons, MegaMenu actions and the extension sidebar.
Panel context includes the original panel ID, dashboard UID, targets, variables, time range and query DataFrames.
Link helpers implement `openModal`, `openSidebar`, `closeSidebar` and `toggleSidebar`; modal bodies receive `onDismiss`.
Plugins can also consume their custom extension points. Other core locations and built-in exposed components remain outstanding.
State reconciles every five seconds. Limits are four concurrent provider loads, twenty seconds per load, 1024 registrations per kind/provider and 1024 requested points.
Agents can inspect declarations with `plugins get --id ID`; client callbacks require the browser runtime.
Tests use two providers and an independent consumer for metadata, limits, state preservation, updates, revocation, overlays and mobile layout.
See the [official API contract](https://grafana.com/developers/plugin-tools/reference/ui-extensions-reference/ui-extensions); this runtime targets the shared 13.2.3 SDK.

## Durable annotations

`/api/annotations` supports creation, queries, full and partial updates, deletion and tag enumeration, plus Graphite creation and explicit dashboard/panel mass deletion.
Times use Unix milliseconds; Graphite `when` uses seconds. Queries match overlapping points/regions and AND tags; `matchAny=true` uses OR.
Dashboard UID/deprecated numeric ID and panel scopes share the workspace's existing Bearer authentication and single local principal.
Records and indexed tags live in the SQLite WAL control plane for either metric backend. An optional creation `idempotencyKey` returns the same ID on retries across restarts; a different normalized payload for the key returns HTTP 409.

```sh
metricspanel annotations save --file annotation.json
metricspanel annotations list --dashboard-uid system --tags '["deploy"]' --start 1791590000000 --end 1791600000000
metricspanel annotations patch --id 1 --file annotation-patch.json
metricspanel annotations tags --name deploy
metricspanel annotations list --type alert --alert-uid RULE_UID
metricspanel annotations delete --id 1
```

Example creation JSON: `{"dashboardUID":"system","time":1791590000000,"timeEnd":1791590060000,"text":"Deployment complete","tags":["deploy"],"idempotencyKey":"deploy-233"}`.
Native charts offer a Chinese/English editor, point markers and clipped regions with plain-text tooltips.
Classic/V1/V2 builtin Grafana dashboard/tag queries execute with variable tags, enabled state and panel filter IDs. Template `hide` preserves visible events.
SDK panels receive public `PanelData.annotations` frames through official `arrayToDataFrame`; mixed sources retain all fields and annotation frames stay separate from metric series.
Queries return at most 1000 events; the frontend permits 32 annotation queries and 1000 merged events, text 8192 bytes and 32 tags.
Pending, firing, recovery, no-data/error transitions, and rule update/pause/deletion create point annotations with `prevState`/`newState`.
State, history, annotations and tags commit together. Unchanged states and stale evaluations produce no duplicates; queries by `type=alert`, `alertUID` and stable numeric `alertId` survive restarts.
Rule annotations `__dashboardUid__` and a positive `__panelId__` link events to template panels. Unlinked events can be queried by public label tags in `key:value` form.
Native charts use state colors and bilingual state labels; automatic rows are read-only in the editor. Mixed public SDK annotation frames retain the state fields.
Automatic annotations follow the newest 100000 transitions, while manual annotations are retained independently. Pre-upgrade history is not backfilled, and recording rules create no alert annotations.
Plugin sources execute public `AnnotationSupport` defaults, preparation, query and custom event processing, plus the legacy `annotationQuery` entrypoint.
Standard conversion supports case-insensitive fields, constant text, skipped fields and split tags; the official SDK merge operator combines frames, and old string queries migrate to targets.
Requests carry the actual panel window, timezone, variables, interval and `__annotation` scope, including variable datasource UIDs.
Streaming updates remain subscribed. Named query failures preserve other annotations and metric data. Limits are 60 seconds without an update, 10000 frame rows and 1000 events per query, with 200 shared cache entries.
Leaving the page cancels observable datasource work, including Go SDK HTTP/gRPC calls. Late legacy Promise results are ignored; plugins also receive an optional cancellation signal.
IDs from different sources remain separate. External rows are read-only and are not automatically ingested into local annotations. `annotations list` reads local records; agents use `datasources query --id UID --file FILE` to inspect raw datasource frames. Frontend SDK callbacks execute in the browser.
Testify and real browser coverage verify Go SDK frames, datasource settings after restart, classic/V1/V2 templates, mappings, colliding source IDs, isolated errors, variables and cancellation.
The dashboard toolbar's annotation query editor adds/removes, reorders, enables, colors and filters queries, and previews real results. Builtin queries select dashboard scope or AND/OR tags.
External sources prefer `AnnotationSupport.QueryEditor`, then the plugin's ordinary `components.QueryEditor`, with official datasource context, range and preview DataFrames. `onChange`, `onAnnotationChange` and `onRunQuery` are supported.
Prometheus offers a PromQL input. Standard mapping supports fields, constant text and skipped fields; custom event processing remains owned by its plugin. Advanced JSON edits other plugin fields.
Prometheus annotation requests honor the datasource `jsonData.timeInterval` minimum, defaulting to 15 seconds, following the [official interval configuration](https://grafana.com/docs/grafana/latest/datasources/prometheus/configure/).
Cancel and preview do not persist configuration. Save checks the version present when the editor opened and reports detected concurrent edits; stop or unmount cancels subscriptions.
Classic, V1 and V2 saves retain the original envelope and unrelated fields. Unchanged V2 query resources remain exact, with each resource's mappings and plugin extensions preserved.
Native dashboards use an optional `annotations` array in the existing SQLite extras (32 queries, 128 KiB); CLI dashboard save/export preserves it. Omission uses builtin defaults; `[]` explicitly disables all queries. No separate migration service is required.
Recurring regions use the builtin datasource's `target.queryType: "timeRegions"` and `target.timeRegion`, with simple weekday/time and advanced Cron editors.
Starts use `browser`, `utc` or IANA zones. An omitted zone uses the browser; newly created configurations default to the dashboard zone. Simple schedules support daily overnight spans, explicit weekday week rollover and inclusive end days for day-only definitions.
Croner 9.1.0 matches Grafana 13.2.3, including five/six fields, weekday names, `L` and `#`. This version normalizes DST gaps forward and runs folds once. Duration is converted by the SDK, then added as fixed milliseconds for every region rather than realigning each wall-clock end.
Queries use the actual panel window and retain full overlapping intervals; charts clip them. An end exactly at the window start is excluded; a start at its end is included. Invalid configurations and more than 1000 regions produce isolated named errors.
Native bands and SDK annotation frames share the events. Generated regions are read-only, never stored and absent from CLI `annotations list`; CLI dashboard save/export retains their configuration.
Chinese/English editors preview and save weekday/Cron/timezone/duration in native, classic, V1 and V2 formats while preserving unknown fields. Automated tests cover persistence after restart, DST, boundaries and real browser SDK frames.
Legacy Angular annotation editors and full organization permissions remain pending. See the [Grafana annotation guide](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/annotate-visualizations/) and [13.2.3 time-region contract](https://github.com/grafana/grafana/blob/v13.2.3/public/app/core/utils/timeRegions.ts).
Testify, real SDK/browser tests and two Docker restart rounds verify durability, retries, overlap, tags, CRUD and mobile layout.
See the [Grafana annotations API](https://grafana.com/docs/grafana/latest/developers/http_api/annotations/).
Plugin execution follows the [Grafana 13.2.3 query runner](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/annotations/executeAnnotationQuery.ts) and [standard annotation converter](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/annotations/standardAnnotationSupport.ts).
The host initializes official loggers, synchronizes the SDK datasource settings cache and registers its plugin importer; datasource service reloads synchronize edits across both SDK generations.
The published package omits the cache/expression core boot exports. Three host-only Vite aliases bind the pinned 13.2.3 core modules; revalidate these bindings when upgrading the SDK.

## Builtin Grafana datasource

Discovery and both public SDK generations expose the read-only `-- Grafana --` instance with UID `grafana` and numeric ID `-1`. UID, name, numeric ID and type-only `grafana` references resolve it; `metricspanel` remains the default metric source. Datasource variables discover actual instances, filter by plugin type/name regex and expose their UIDs in the selector.

The frontend executes `randomWalk`, `snapshot`, `timeRegions`, `annotations` and `measurements`. Random walks support start/min/max/spread/noise/dropPercent; snapshots use official DataFrame JSON conversion and retain original refIds. Live measurements support channel variables, field filters and buffering; the final unsubscribe cancels backend work. Native and classic/V1/V2 panels can mix these frames with Prometheus queries.

The backend and agent execute `randomWalk` and `list`, following the [Grafana 13.2.3 core backend split](https://github.com/grafana/grafana/blob/v13.2.3/pkg/tsdb/grafanads/grafana.go). `listFiles` only lists embedded public web assets and rejects absolute/traversal paths; list queries may omit the time range. Generated sequences, snapshots and recurring regions never enter metric or annotation storage. Random walks allow 10,000 points/frame, 128 frames and 1,000,000 points/query; invalid inputs and non-finite output produce explicit errors.

```json
{"from":"now-5m","to":"now","queries":[{"refId":"A","queryType":"randomWalk","intervalMs":1000,"startValue":233,"spread":0}]}
```

Save this as `query.json` and run `metricspanel datasources query --id grafana --file query.json`; add `--stream` for NDJSON. Frontend callbacks execute in the browser. Grafana scopes annotation queries remain unsupported and return an explicit error.

Legacy `DataSourceSrv.registerRuntimeDataSource` and the public `@grafana/runtime/unstable` registration share runtime instances, preserve discovery after settings reload and reject duplicate UIDs. These instances live in the browser session rather than durable collectors. Real SDK browser tests cover discovery, variables, registration, mixed frames and stream cancellation; Testify and two SQLite/ClickHouse Docker rounds verify agent output and restart behavior.

## Server expression queries

The read-only expression instance uses UID/type `__expr__`, legacy ID `-100` and name `Expression`, separate from installable datasource discovery. Both public SDK generations share its instance. Classic/V1/V2 templates retain envelopes and unknown fields. Hidden inputs execute without rendering; mixed sources run as one backend dependency graph.

Math, Reduce, Resample, Threshold and Classic conditions operate on actual SDK frames. Math supports `$A` / `${query name}`, documented operators and 13 functions. Grafana 13.2.3 exponentiation is left associative; unary operators bind more tightly. Labels join by equality/subset/unlabelled broadcast, and series intersect timestamps. A single unmatched item on each side joins without labels. Equal label-key sets use an indexed join, tested with 10,000 dimensions.

Reduce supports sum/mean/min/max/count/last/median and strict/dropNN/replaceNN modes. Resample supports sum/mean/min/max/last downsampling and pad/backfilling/fillna upsampling, from range start through the reachable inclusive endpoint. Threshold supports comparisons and exclusive/inclusive range checks over numbers or series, retaining nulls.

`classic_conditions` reads input RefIDs from condition `query.params[0]`, retaining legacy range parameters and unknown fields. Its 12 reducers include avg, diff/diff_abs, percent_diff/percent_diff_abs and count_non_null. Series reducers skip null/NaN and retain infinities; instant numbers bypass reduction. AND/OR fold in document order; logic-or stops further condition computation when already firing. Evaluators include no_value and reversed/inclusive ranges. Output is one unlabelled 1/0/null, with match diagnostics (string value, metric and labels) in `frame.meta.custom`. Comparison queries remap condition references. Browser tests cover Classic/V1/V2 templates.

Use real memory metrics with `metricspanel datasources query --id __expr__ --file examples/queries/classic-conditions.json`; add `--stream` to receive the same match metadata.

Grafana alert rules execute their persisted query graphs through the same backend expression engine, including multiple datasources and compound classic conditions. See alerting below for agent and persistence details.

```json
{"from":"now-5m","to":"now","queries":[{"refId":"A","hide":true,"datasource":{"uid":"metricspanel"},"expr":"sum(up)","instant":true},{"refId":"B","type":"math","expression":"$A*100"}]}
```

Save this as `expression.json` and run `metricspanel datasources query --id __expr__ --file expression.json`; add `--stream` for official NDJSON frames after graph computation. Both modes preserve successful results and exit nonzero for partial failures. RefIDs may arrive out of dependency order. Cycles, missing references and invalid nodes produce named errors while independent branches remain usable. Computation needs no browser, never alters inputs and never writes metrics or annotations.

Inputs are wide time series, one numeric column with string dimensions, or Prometheus instant vectors. Official DataFrame JSON preserves null/NaN/Inf. Panel comparisons remap references and retain historical timestamps. Limits: 32 queries, 10,000 joined items, 1,000,000 working points across inputs/intermediates, 20 seconds and 32 MiB response per request.

SQL expressions use the same embedded Go MySQL engine as Grafana 13.2.3, with JOINs, CTEs, subqueries, windows and its pinned function allowlist. Backend RefIDs are tables. Wide/multi numeric and time-series frames expand into full-long metric, value, optional display-name and label columns; ordinary tables retain numeric, string, boolean, time and JSON fields. Each graph permits one terminal SQL node, reading backend queries only; expressions cannot consume SQL results. Limits are 10000 query bytes, 100000 input/output cells and 10 seconds. Excess output truncates at a complete row with a warning; excess input fails. Writes, file access and session variables are rejected.

Run `metricspanel datasources query --id __expr__ --file examples/queries/sql-metrics.json --stream`; save `examples/alerts/graph-sql-memory.json` for an alert example. Alert/record results require exactly one numeric column and unique string-label combinations; NULL strings omit labels. Evaluation forces a transient alerting format while preserving saved JSON, state, timers and recorded samples across restarts. See the [pinned SQL contract](https://github.com/grafana/grafana/blob/v13.2.3/pkg/expr/sql_command.go).

SQL panels support fixed-offset and previous-period comparisons. Independent current/historical backend requests keep original RefIDs and SQL text, then mark returned frames with `-compare` and `timeCompare` metadata. CTEs, aliases and quoted text need no rewriting. Native plots align display timestamps while SDK frames retain historical timestamps. SQL uses actual scoped dashboard variables and time macros, including variables named after input RefIDs. Uniform `__display_name__` values restore numeric field aliases; mixed names remain per-row, without mutating cached frames. Native time inputs support explicit `DATETIME(6)` casts; default MySQL `TIMESTAMP` output precision is seconds. See `examples/grafana-sql-comparison.json`.

Panel and alert graph editors support all six expression operations and installed backend SDK QueryEditor callbacks; unsupported operations return explicit errors. Testify, real SDK/browser coverage, bilingual mobile checks and two SQLite/ClickHouse restart rounds verify behavior.
See the [expression contract](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/query-transform-data/expression-queries/), [classic conditions](https://grafana.com/docs/grafana/latest/alerting/fundamentals/alert-rules/queries-conditions/) and [pinned parser](https://github.com/grafana/grafana/blob/v13.2.3/pkg/expr/mathexp/parse/parse.go).

## Dashboard time ranges

Saved `time` / `timeSettings` and timezone defaults from classic, V1 and V2 dashboards drive real queries.
URLs accept `from/to`, `time/time.window` and `timezone`. Explicit bounds take precedence; centered windows use Unix milliseconds.
Changing ranges preserves `var-*` values; reloads and browser history restore the selected window.
The custom picker accepts Unix milliseconds, ISO dates and SDK date math such as `now-1h/h` and `now/d`, including IANA zones and DST.
One now anchors both endpoints. Fixed windows remain fixed across refreshes; ranges are limited to 31 days.

SDK `PanelProps.onChangeTimeRange` and native chart drag selection default to updating the workspace and URL, then execute real queries.
Query requests, SDK panel ranges, variable queries, date formatting and pattern capture use the selected bounds and timezone.
`$__from`, `$__to` and `$__range_ms` retain exact milliseconds, and TemplateSrv.updateTimeRange is connected.
Panel `timeFrom` supports date math and variables and overrides relative dashboard ranges. `timeShift` also applies to fixed ranges, respects calendar/timezone shifts and accepts rounding such as `1d/d`.
The query snapshot supplies SDK PanelProps, panel data, macros, transformations, chart bounds and panel menus. `hideTimeOverride` hides the time description.
SDK zoom and native drag selection reverse a panel's shift before updating the dashboard, avoiding a second application of the same shift.
Classic `refresh` / V2 `timeSettings.autoRefresh` and interval choices drive timers; imported dashboards default to off, native workspaces to five seconds.
Controls offer off, explicit intervals and automatic mode. URL `refresh` overrides saved defaults; an empty value means off, and browser history preserves the choice.
The minimum interval is five seconds. Automatic mode uses the range and viewport width. Hidden pages pause requests, resume catches up once, and unmount clears timers.
The standard Grafana `timeCompare` field issues separate historical queries. Classic/V1 panels save it directly; V2 saves `data.spec.queryOptions.timeCompare`.
Explicit intervals and `__previousPeriod` are supported. Query `timeRangeCompare: false` opts out; V2 retains it in `DataQuery.spec`.
Early MetricsPanel `compareWith` templates remain supported. The standard field takes precedence; an explicit empty string disables comparison, including when a legacy alias is present.
Comparison frames carry idempotent `-compare` refIds and SDK `timeCompare.diffMs` metadata. SDK panels retain historical timestamps; native curves use the official alignment helper and dashed styling.
Comparison `1d` / `1M` offsets follow the SDK's fixed 24-hour / 30-day durations, whereas `timeShift` uses calendar date math. Relative raw ranges advance on refresh.
Choosing “Zoom scope → This panel” creates a native local view: SDK zoom and drag selection query that panel while preserving the URL and other panels. Refresh retains the local window; reset or parent range/variable changes restore the dashboard window.
Local zoom is browser view state and preserves original template exports. Comparison errors are visible while primary query data remains available.
Twelve model tests, Testify import tests and browser tests verify historical requests, opt-out, frame metadata, alignment, refresh, local zoom and bilingual mobile layout.
Browser tests import standard classic/V1/V2 resources and verify real queries and lossless agent CLI exports, explicit disable and legacy aliases.
Grafana 13.2 keeps `zoomBehavior` in Scenes runtime state; its classic/V2 serializers do not persist it. Native local zoom remains a browser view operation.
Full Scenes lifecycle and additional time interactions remain outstanding.
See the [Grafana time URL contract](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/create-dashboard-url-variables/).
Overrides follow the [Grafana 13.2 PanelTimeRange implementation](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/scene/panel-timerange/PanelTimeRange.tsx).
Comparison fields follow the [classic serializer](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/serialization/transformSceneToSaveModel.ts) and [V2 serializer](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/serialization/transformSceneToSaveModelSchemaV2.ts).

## Grafana application events

`getAppEvents()` returns the shared official SDK `EventBusSrv`: typed `publish/getStream/subscribe`, scoped buses
and legacy `emit/on/off` work, including custom events between applications and extensions.
Plugin `RefreshEvent` and legacy `refresh` trigger real workspace queries. Native refresh buttons and the five-second poll publish the same event.
Concurrent refreshes are coalesced per workspace, with at most one pending follow-up while a request is running.
Native range selection publishes `TimeRangeUpdatedEvent` with SDK DateTime values and raw `now-RANGE / now`.
Independent panel buses relay refresh, time-range and theme events in both directions without echoing the source panel.
Panel teardown removes only its own subscriptions; sibling panels and applications continue receiving events.

`AppEvents.alertSuccess/alertWarning/alertError/alertInfo` show four native notification severities with title, detail and Error.message.
Text is rendered without HTML and bounded to 8192 characters. Errors/warnings use `role=alert`; dismiss controls support Chinese and English.
These are browser notifications; server alert notification delivery remains outstanding. The theme is currently dark-only.
Native CopyPanelEvent handling, global hover/select from native charts and other app core services still need implementation.
An independent app and two SDK panels test real query updates, duplicate prevention, unmount/remount, coalescing, notifications and mobile layout.
See the [Grafana event subscription contract](https://grafana.com/developers/plugin-tools/how-to-guides/panel-plugins/subscribe-events).

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

Grafana provisioning rule CRUD and group GET/PUT/DELETE persist new rules with `execution=grafana`. The backend runs the original query graph through the shared expression engine: configured Prometheus, built-in Grafana and SDK backend sources, multi-reference Math and label joins, all Reduce modes, Resample, Threshold and compound Classic conditions. Every query derives its own relative range from one scheduled timestamp, up to 31 days. Plugins receive FromAlert=true, X-Cache-Skip=true and organization headers through the existing encrypted instance settings.

Conditions return one numeric value per labelled frame. Nulls apply per-instance no-data policies; datasource NoData takes priority over a healthy condition. Nonzero NaN/Inf follow Grafana alert truth and persist safely as value_text. Recording rules reject nonfinite values before ingesting any partial batch. Classic match diagnostics persist in runtime.instances[].matches, limited to 1 MiB per frame. Timers, state, matches and graph definitions survive restart.

The bilingual visual alert graph editor offers datasource SDK QueryEditor with UnifiedAlerting context, all six expression operations, condition selection, individual query windows, add/remove/reorder and advanced JSON. Edits preserve plugin models, opaque fields and rule metadata. Create a native graph with `metricspanel alerts save --file examples/alerts/graph-memory.json`, or import standard provisioning JSON with alerts import-grafana. GET output can be updated with alerts save using its current version. Switching to native PromQL discards the old graph; existing rules without execution=grafana keep their previous PromQL path.

Group PUT replaces rules, schedules, state, history and annotations in one SQLite control-plane transaction. Omitted/null rules update only the interval; an explicit empty array clears the group. URL folder/name override the body. Identical PUT and GET/PUT preserve versions and runtime. Interval, ordering, provenance and move changes retain timers; evaluation changes reset state. Array order survives restart and moved UIDs normalize their source group. Active affected evaluations return 409 before writing. Group intervals are 5–86400 seconds, with at most 1000 total rules.

```powershell
metricspanel alerts group-save --folder-uid general --group infrastructure --file examples/alerts/grafana-group.json
metricspanel alerts group-get --folder-uid general --group infrastructure
metricspanel alerts group-save --folder-uid general --group infrastructure --file interval-only.json
metricspanel alerts group-delete --folder-uid general --group infrastructure
```

An interval-only file can contain `{"interval":60}`. Reconcile with returned UIDs; new rules without UIDs receive generated IDs. X-Disable-Provenance or CLI --disable-provenance marks the whole group editable. Provenance is metadata here and does not restrict native editing. Deletion returns HTTP 204 and CLI JSON null. Configure a MySQL collector for the example; missing metrics follow its no-data policy.

Labels and annotations support Grafana Go text templates. $labels contains query labels and the pinned reserved context, excluding configured rule labels. $values.A.Value / .Labels expose numeric instant and expression captures; range frames are excluded until reduced. Captures match exact/subset/superset instance labels, preferring exact matches. Classic conditions expose indexed matches C0/C1/etc and remove query instance labels. With exactly one datasource capture, $value is numeric; otherwise it is a stable evaluation string.

The upstream Prometheus text functions are extended with Grafana filterLabels/removeLabels and regex variants, mergeLabelValues and graphLink/tableLink. query() is a Grafana-compatible no-op. externalURL/pathPrefix use --root-url or METRICSPANEL_ROOT_URL. Parse, runtime and budget failures retain original field text and report template_errors without changing alert truth or health. Outputs are capped at 512/4096 bytes per label/annotation and 50000 execution steps per field, including empty nested loops.

Instance details show rendered summary/description and expandable warnings as plain text. Source templates, instance annotations and transition annotations persist, while Prometheus alerts returns rendered annotations. Use `metricspanel alerts save --file examples/alerts/template-memory.json` and alerts get/evaluate/history to inspect source and results. See the [template reference](https://grafana.com/docs/grafana/latest/alerting/alerting-rules/templates/reference/) and [pinned implementation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/state/template/template.go).

Unavailable datasources, malformed nodes and cycles fail before save. External notifications/Alertmanager and remaining core datasource editors remain pending, along with folder/organization permissions, App Platform rule APIs and file-format exports. Real SDK, CLI, mobile editing and two MySQL/SQLite/ClickHouse restart rounds verify graph execution and persistence. See the [provisioning API](https://grafana.com/docs/grafana/latest/alerting/set-up/provision-alerting-resources/http-api-provisioning/), [alert rules](https://grafana.com/docs/grafana/latest/alerting/fundamentals/alert-rules/) and [pinned evaluator](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/eval/eval.go).

## Automated verification

The alert editor's Preview action calls `/api/v1/alerts/preview` for real SDK frames, labelled condition values, truth and errors. It evaluates fresh recovery dimensions without saving rules, advancing timers or writing recordings. Preview and the scheduler share four slots and a 20-second deadline; stop, query edits and dialog close cancel work. Agents use `metricspanel alerts preview --file examples/alerts/graph-memory.json [--at UNIX_MS]`; omitted timestamps use server time. Native graph rules and provisioning graphs are accepted. Partial errors retain JSON frames and exit 1.

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
The Docker suite uses one `suite` container and one volume, limited to 2 CPUs, 2 GiB memory and 512 PIDs. It runs real MySQL-compatible MariaDB, Redis, PostgreSQL, ClickHouse, the official MySQL exporter and Go services. JVM/big-data endpoints use protocol fixtures, not real clusters. Full-container restarts verify durable samples and encrypted connections. Cleanup removes this project's container, network, volume and test image. Runtime images omit Node/Go toolchains; the real ClickHouse executable alone is approximately 762 MiB, and tests print actual image size. `compose.test.yml` accepts `METRICSPANEL_TEST_CLICKHOUSE_IMAGE` and `METRICSPANEL_TEST_EXPORTER_IMAGE` build-source overrides; production Compose has its own configuration.

Apache-2.0. See the [Chinese README](README.md) for API details, environment variables, architecture and limitations.
