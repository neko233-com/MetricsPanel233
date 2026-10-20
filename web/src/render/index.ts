import { registerRenderer } from "./registry";
import { TableRenderer } from "./table/TableRenderer";
import TimeSeriesRenderer from "./timeseries/TimeSeriesRenderer";

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

export { TableRenderer, TimeSeriesRenderer };
export { getRenderer, registeredRenderers, registerRenderer } from "./registry";
export { nextFrame, runInFrameSlices } from "./frame-budget";
export { appendedRows, frameSignature, reuseIfSame } from "./incremental";
export type { RendererDefinition, RendererKind, RendererProps } from "./types";
