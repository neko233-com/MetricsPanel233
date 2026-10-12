# Grafana core coverage audit — 2026-10-12

Baseline: Grafana **v13.2.3** source tag. This is a read-only source inspection of MetricsPanel233's current implementation, not a claim that all supported features were exercised in this audit. SQL real MariaDB/PostgreSQL checks and browser checks are tracked separately in docker logs.

Official baseline lists: [core data sources](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/datasources/_index.md#built-in-core-data-sources), [core visualizations](https://github.com/grafana/grafana/blob/v13.2.3/docs/sources/visualizations/panels-visualizations/visualizations/_index.md).

## Data sources

The tagged documentation lists 19 core sources. MetricsPanel233 has local query implementations for three. The other 16 can potentially use the installed-plugin host, but their official packages, query editors, resource APIs, authentication, platform executables, and real-service behavior are not bundled or individually established. Collector health/metrics for a product is a different capability from its Grafana datasource query implementation.

The SQL plugins moved to separate official repositories before this Grafana tag; the old monorepo SQL paths return 404 at v13.2.3. Separate-repository links below identify inspected official main branches as of this audit, not an invented package revision bundled by the Grafana tag.

| Official source | Official type ID | Local implementation status | Direct official source |
|---|---|---|---|
| Prometheus | prometheus | Builtin local/external query adapter; not full official editor parity | [plugin manifest](https://github.com/grafana/grafana-prometheus-datasource/blob/main/src/plugin.json) |
| MySQL | mysql | Builtin raw SQL, typed frames, time macros, variables; live MariaDB checks | [manifest](https://github.com/grafana/grafana-mysql-datasource/blob/main/src/plugin.json), [macros](https://github.com/grafana/grafana-mysql-datasource/blob/main/pkg/mysql/macros.go) |
| PostgreSQL | grafana-postgresql-datasource; alias postgres | Builtin raw SQL, typed frames, time macros, variables; live PostgreSQL checks | [manifest](https://github.com/grafana/grafana-postgresql-datasource/blob/main/src/plugin.json), [macros](https://github.com/grafana/grafana-postgresql-datasource/blob/main/pkg/postgresql/macros.go) |
| AWS CloudWatch | cloudwatch | Missing builtin datasource | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/cloudwatch/plugin.json) |
| Azure Monitor | grafana-azure-monitor-datasource | Missing builtin datasource | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/azuremonitor/plugin.json) |
| Google Cloud Monitoring | stackdriver | Missing builtin datasource | [manifest](https://github.com/grafana/grafana-cloudmonitoring-datasource/blob/main/src/plugin.json) |
| Graphite | graphite | Missing builtin datasource | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/graphite/plugin.json) |
| InfluxDB | influxdb | Missing builtin datasource | [manifest](https://github.com/grafana/grafana-influxdb-datasource/blob/main/src/plugin.json) |
| OpenTSDB | opentsdb | Missing builtin datasource | [manifest](https://github.com/grafana/grafana-opentsdb-datasource/blob/main/src/plugin.json) |
| Elasticsearch | elasticsearch | Missing builtin datasource; existing collector is not search/log query parity | [manifest](https://github.com/grafana/grafana-elasticsearch-datasource/blob/main/src/plugin.json) |
| Loki | loki | Missing builtin log query datasource | [manifest](https://github.com/grafana/grafana-loki-datasource/blob/main/src/plugin.json) |
| Jaeger | jaeger | Missing builtin tracing datasource | [manifest](https://github.com/grafana/grafana-jaeger-datasource/blob/main/src/plugin.json) |
| Tempo | tempo | Missing builtin tracing datasource | [manifest](https://github.com/grafana/grafana-tempo-datasource/blob/main/src/plugin.json) |
| Zipkin | zipkin | Missing builtin tracing datasource | [manifest](https://github.com/grafana/grafana-zipkin-datasource/blob/main/src/plugin.json) |
| Parca | parca | Missing builtin profiling datasource | [manifest](https://github.com/grafana/grafana-parca-datasource/blob/main/src/plugin.json) |
| Pyroscope | grafana-pyroscope-datasource; alias phlare | Missing builtin profiling datasource | [manifest](https://github.com/grafana/grafana-pyroscope-datasource/blob/main/src/plugin.json) |
| Microsoft SQL Server | mssql | Missing builtin SQL datasource | [manifest](https://github.com/grafana/grafana-mssql-datasource/blob/main/src/plugin.json) |
| Alertmanager | alertmanager | Local alertmanager-compatible routes exist; arbitrary remote Alertmanager datasource/editor is not builtin | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/alertmanager/plugin.json) |
| TestData | grafana-testdata-datasource | Grafana randomWalk is implemented; full TestData scenarios are missing | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/grafana-testdata-datasource/plugin.json) |

Special sources, outside that list:

| Source | Existing behavior | Gap / source |
|---|---|---|
| Grafana | Builtin randomWalk/list; SDK snapshots, annotations, timeRegions and live measurements | Covered subset is real; [official module](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/grafana/module.ts) |
| Mixed | engine.ts groups each target by datasource UID and combines responses | No discoverable builtin Mixed instance or dedicated official editor; [official module](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/mixed/module.ts) |
| Dashboard | No sourcePanel/panel-output reuse implementation found | Needs lifecycle/cache/results sharing and query editor; [official datasource](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/datasource/dashboard/datasource.ts) |
| Expressions | Builtin __expr__ server graph and SDK editor are present | This audit does not certify every expression operation or SQL-on-frames rule |

SQL annotation mapping and editing are implemented in the builtin SDK datasource, and backend metadata advertises annotations=true. The fresh single-container browser suite passed all three desktop cases: both real SQL engines, dynamic variable labels/multiple selections, native SQL query editing, and English/Chinese annotation point/range, variable, field mapping, preview/save/reload and opaque-field preservation. No page/console warnings or errors were recorded. SQL-specific remaining gaps include the official visual SQL builder, schema/table/column completion, and a real certificate/TLS endpoint integration check. Query execution is bounded and runs read-only transactions; explicit limits can reject a very large imported query. Read-only prevents updates but is not a sandbox against resource-intensive SELECT functions; server timeout handles cancellation.

## Visualizations

The tagged documented list has 25 visualizations. Six have local renderers: table and time series now use pinned official SDK components, and stat/gauge/bar gauge/text retain their local implementations. The other 19 go through PluginPanel -> loadPlugin, which requires an installed package; no bundled official core renderer/import map was found. A community panel with a different ID does not make the original core ID compatible automatically.

| ID | Local renderer | Direct official manifest |
|---|---|---|
| timeseries | Pinned SDK TimeSeries; desktop 20-series, log axes, percent stacks, legend, tooltip, annotations, zoom and comparison checks passed | [manifest](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/timeseries/plugin.json), [SDK component](https://github.com/grafana/grafana/blob/v13.2.3/packages/grafana-ui/src/graveyard/TimeSeries/TimeSeries.tsx) |
| stat | Present, numeric reduction only | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/stat/plugin.json) |
| gauge | Present, simplified SVG | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/gauge/plugin.json) |
| bargauge | Present, simplified progress element | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/bargauge/plugin.json) |
| table | Pinned SDK Table; desktop 1,000-row pagination and sorting verified | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/table/plugin.json) |
| text | Present, sanitized Markdown/HTML/code | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/text/plugin.json) |
| state-timeline | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/state-timeline/plugin.json) |
| status-history | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/status-history/plugin.json) |
| barchart | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/barchart/plugin.json) |
| histogram | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/histogram/plugin.json) |
| heatmap | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/heatmap/plugin.json) |
| piechart | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/piechart/plugin.json) |
| candlestick | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/candlestick/plugin.json) |
| trend | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/trend/plugin.json) |
| xychart | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/xychart/plugin.json) |
| logs | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/logs/plugin.json) |
| nodeGraph | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/nodeGraph/plugin.json) |
| traces | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/traces/plugin.json) |
| flamegraph | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/flamegraph/plugin.json) |
| canvas | Missing builtin renderer/editor | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/canvas/plugin.json) |
| geomap | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/geomap/plugin.json) |
| dashlist | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/dashlist/plugin.json) |
| alertlist | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/alertlist/plugin.json) |
| annolist | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/annolist/plugin.json) |
| news | Missing builtin renderer | [source](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/news/plugin.json) |

Additional tagged manifests outside the documented 25: debug, gettingstarted, live, logstable, welcome. A row is rendered as a heading/layout element; full row collapse/section editing is a separate dashboard behavior.

Concrete scope and remaining limitations of the six existing renderers:

- Time series: GrafanaTimeSeries.tsx passes every temporal numeric/enum series to the pinned SDK uPlot component. Saved fields support standard units/colors and the SDK's stacking, log/symlog distributions, multiple axes, bars/points/lines, null spacing and thresholds; legend placement/calculations/filtering and tooltip mode/sorting/zero filtering/dimensions are adapted. Mouse/keyboard range selection and annotation points/regions retain local dashboard integration. Eight helper tests cover cloning, series count, visibility, tooltip filtering, annotation geometry, integer range selection and official comparison alignment. Three desktop browser cases verify actual 20-color canvas pixels, 12-series log left/right axes and percent stacks, legend selection/restoration, tooltip bounds, clipped annotation points/regions, panel/dashboard zoom, and a time-aligned dashed comparison curve with untouched historical source timestamps. No page/console warnings or errors were recorded. The exported SDK component lives in Grafana's graveyard API and is not the complete current Time series PanelPlugin/editor. Full data-link/actions UI, persisted legend color editing, plugin migrations and cross-panel cursor forwarding remain gaps. The legacy native Chart.tsx path now renders all queried series, while retaining its simplified single linear SVG axis and native queryPanel aggregation semantics. [SDK graph configuration](https://github.com/grafana/grafana/blob/v13.2.3/packages/grafana-ui/src/graveyard/TimeSeries/utils.ts), [official option registration](https://github.com/grafana/grafana/blob/v13.2.3/public/app/plugins/panel/timeseries/module.tsx).
- Stat: numeric fields only; no full string/boolean stat behavior or all sparkline/background/display modes.
- Gauge/bar gauge: simple SVG/progress and thresholds; lacks complete official display modes, tick layout, sizing and orientation configuration.
- Table: GrafanaTable.tsx renders complete frames with the pinned SDK Table, including saved field configuration, sorting, pagination, footer calculations, cell height and header/type-icon settings. Desktop verification establishes pagination through the complete 1,000-row frame, numeric sorting and footer calculations. Page size follows the available panel height. This is SDK rendering rather than the complete core Table PanelPlugin option editor; it does not establish every cell mode, action, migration or editor interaction.
- Text: sanitization is deliberate and real; imported interactive actions/embedded iframe content does not execute.
- These builtin visualization paths lack the complete official PanelPlugin option schemas/editors because they bypass loadPanelPlugin. The installed third-party PluginPanel host has a separate SDK option editing path.

Audit evidence in repository: internal/server/plugins.go, internal/server/expressions.go, internal/server/sql_datasources.go, internal/server/grafana_source.go, internal/server/native_plugin_routes.go, web/src/grafana/plugin-runtime.ts, web/src/grafana/engine.ts, web/src/grafana/GrafanaPanel.tsx, web/src/grafana/GrafanaTable.tsx, web/src/grafana/GrafanaTimeSeries.tsx, web/test/time-series.test.cjs, web/e2e/time-series.spec.ts, web/src/components/Chart.tsx.

Priority suggested by dependencies: core renderer registry and faithful typed options; Missing core query adapters/editors for major logs/traces/storage sources; Dashboard datasource result reuse; then remaining editor/advanced options. This report only covers sources and visualizations, not an exhaustive Grafana product audit.
