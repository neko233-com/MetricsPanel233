# Renderer framework

This is the contract for MetricsPanel233's own visualization engine. Grafana packages are compatibility
adapters, not the renderer foundation.

## Goals

- One renderer registry for time series, tables, stats, gauges, logs, traces, heatmaps, vectors and future panels.
- Canvas-first hot paths (uPlot/Canvas2D now, optional WebGL later) with SVG/DOM for overlays and tables.
- Deterministic normalization before drawing: time alignment, stacking, comparison, null handling and field stats.
- Vector-native: 64-dimensional metric windows and embedding-like rows are first-class render inputs, not string blobs.
- Performance budgets are measured, not assumed.

## Data contract

The current query engine emits Grafana `DataFrame`/`Field` values. The renderer framework consumes that contract
during the transition. The renderer boundary must not import `@grafana/ui`.

- `frames[]`: one frame per query result; `fields[]` carry name, type, values, labels, display and config.
- Time series: one `time` field plus numeric fields; labels and `displayName` are preserved.
- Tables/logs/traces: string, number, time and labels; logs use `Line` + `Time`; traces use a table frame.
- Vectors: a frame with a 64-number field plus metadata; the vector renderer reads the same field values.

A future project-owned frame type can replace the Grafana data contract without changing renderers, because
renderers read only `frames`, `fields`, `values`, `labels`, `range` and `options`.

## Pipeline

1. **Deserialize** query frames into render inputs.
2. **Normalize**
   - sort and deduplicate timestamps;
   - align multiple frames by timestamp (nearest/union);
   - apply visibility (`hideFrom.viz`), null policy and unit/decimals;
   - compute stacking groups and comparison offsets;
   - compute min/max/current/first/last for legends and axes.
3. **Layout** panel chrome, viewport, legend, axes, annotations and overlays.
4. **Draw** Canvas for series; SVG for annotation markers/regions; DOM for tables and log rows.
5. **Interact** cursor/tooltip, legend toggle, drag zoom, keyboard and touch.
6. **Dispose** uPlot instances, observers, RAF and subscriptions on unmount or data signature change.

## Renderer registry

```ts
type RendererKind = "timeseries" | "table" | "stat" | "gauge" | "bargauge" | "logs" | "traces"
  | "heatmap" | "histogram" | "pie" | "candlestick" | "geomap" | "nodegraph"
  | "state-timeline" | "status-history" | "text" | "vector";

type RendererDefinition = {
  kind: RendererKind;
  label: string;
  capabilities: { compare?: boolean; stack?: boolean; streaming?: boolean; vector?: boolean };
  component: ComponentType<RendererProps>;
};
```

`web/src/render/registry.ts` maps kinds to definitions. `web/src/render/index.ts` resolves a panel's
`visualization` and falls back to the compatibility renderer only when an imported Grafana resource explicitly
requires it. Unknown kinds show a named error instead of silently rendering nothing.

## Visualization coverage matrix

| Kind | Status | Notes |
| --- | --- | --- |
| Time series | Implemented (core, opt-in) | Own uPlot renderer; lines, comparison, legend, tooltip, zoom, annotations; log/percent/multi-axis parity pending. Enable with `?renderer=own` or localStorage |
| Table | Implemented (core) | Own HTML table; sorting, pagination, page numbers, footer reducers, display formatting |
| Stat / Gauge / Bar gauge | Planned | Reuse normalized field stats and threshold colors |
| Text / Markdown | Planned | Sanitized HTML renderer |
| Logs | Planned | Loki frames; virtualized rows, labels, search |
| Traces | Planned | Tempo search table first, trace detail later |
| Heatmap / Histogram | Planned | Bucket and density renderers |
| Pie / Candlestick / Geomap / Node graph | Planned | Lower priority for SMB monitoring |
| State timeline / Status history | Planned | Alert/state transitions |
| Vector | Planned | 64-dim sparkline, similarity scatter and neighbor list |

Compatibility-only renderers for imported Grafana dashboards are tracked separately; they do not count as
first-party coverage.

## Performance budget

Reference targets on the product's single-node hardware:

- 1,000,000 raw points per panel are decimated to at most 2 × pixel width before drawing.
- 200 visible series per time-series panel; 2,000 buckets for native metric queries.
- 60 fps cursor/legend interaction on 100k visible points; no full-frame React re-render on pointer move.
- Table pages render at most 100 rows; footer reducers scan all rows without creating per-cell React nodes.
- Vector similarity uses bounded top-K; 10k vectors should stay in the interactive path on SQLite.
- Every renderer reports data size, decimation ratio and draw time in development mode.

### Frame slicing and incremental work

- `web/src/render/frame-budget.ts` provides `runInFrameSlices` and `nextFrame`: each slice processes at most
  `perFrame` items or `sliceMs` milliseconds, then yields to animation frames. AbortSignal cancels stale work.
- `web/src/render/incremental.ts` provides signature caching and append-row detection. Renderers reuse normalized
  models when the signature is unchanged and can inspect append-only growth for streaming sources.
- The TimeSeries renderer defers frame normalization with `useDeferredValue`, so cursor/legend input keeps priority
  over a heavy refresh.
- Next: move normalization/decimation into a Worker for >100k-point panels, and add a min/max/LTTB decimator that
  preserves spikes before drawing.

Measurements go in `docs/performance-baseline.md` or a dedicated renderer benchmark; do not claim universal
performance from a single workstation result. `web/scripts/render-bench.mjs` starts a temporary server, generates a
configurable panel (`RENDER_BENCH_POINTS`, `RENDER_BENCH_SERIES`) and reports first-frame timing and heap size.

## Migration

1. Own Table is the product default; own TimeSeries is opt-in until log/percent/multi-axis parity passes the
   time-series browser contracts. The Grafana bridge remains for compatibility resources and plugins.
2. Finish TimeSeries parity, then move Stat/Gauge/Logs/Traces/Vector.
3. Remove `@grafana/ui` from the default bundle and leave it only in the optional import/plugin chunk.
