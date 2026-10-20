import { registerRenderer } from "./registry";
import LogsRenderer from "./logs/LogsRenderer";
import StatRenderer from "./stat/StatRenderer";
import { TableRenderer } from "./table/TableRenderer";
import TimeSeriesRenderer from "./timeseries/TimeSeriesRenderer";
import TracesRenderer from "./traces/TracesRenderer";

registerRenderer({
  kind: "table",
  label: "Table",
  capabilities: {},
  component: TableRenderer,
});
registerRenderer({
  kind: "timeseries",
  label: "Time series",
  capabilities: { compare: true, stack: true, streaming: true },
  component: TimeSeriesRenderer,
});
registerRenderer({
  kind: "stat",
  label: "Stat",
  capabilities: { streaming: true },
  component: StatRenderer,
});
registerRenderer({
  kind: "gauge",
  label: "Gauge",
  capabilities: { streaming: true },
  component: StatRenderer,
});
registerRenderer({
  kind: "bargauge",
  label: "Bar gauge",
  capabilities: { streaming: true },
  component: StatRenderer,
});
registerRenderer({
  kind: "logs",
  label: "Logs",
  capabilities: { streaming: true },
  component: LogsRenderer,
});
registerRenderer({
  kind: "traces",
  label: "Traces",
  capabilities: {},
  component: TracesRenderer,
});

export {
  LogsRenderer,
  StatRenderer,
  TableRenderer,
  TimeSeriesRenderer,
  TracesRenderer,
};
export { getRenderer, registeredRenderers, registerRenderer } from "./registry";
export { nextFrame, runInFrameSlices } from "./frame-budget";
export { appendedRows, frameSignature, reuseIfSame } from "./incremental";
export type { RendererDefinition, RendererKind, RendererProps } from "./types";
