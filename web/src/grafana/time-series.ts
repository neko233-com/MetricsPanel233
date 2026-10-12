import type { DataFrame, Field } from "@grafana/data";

function copyValue<T>(value: T): T {
  if (Array.isArray(value)) return value.map(copyValue) as T;
  if (value && typeof value === "object" && Object.prototype.toString.call(value) === "[object Object]")
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, copyValue(item)])) as T;
  return value;
}

/** The SDK plot preparation changes field values and custom config in place. */
export function cloneTimeSeriesFrames(frames: DataFrame[]): DataFrame[] {
  return frames
    .filter((frame) => frame.fields.some((field) => field.type === "time") &&
      frame.fields.some((field) => field.type === "number" || field.type === "enum"))
    .map((frame) => ({
      ...frame,
      fields: frame.fields.map((field) => ({
        ...field,
        values: Array.from(field.values),
        config: copyValue(field.config),
        labels: field.labels ? { ...field.labels } : undefined,
        state: field.state ? copyValue(field.state) : undefined,
      })),
    }));
}

export function prepareTimeSeriesFrames(
  frames: DataFrame[],
  alignComparison: (frame: DataFrame, diff: number) => DataFrame,
): DataFrame[] {
  return cloneTimeSeriesFrames(frames).map((frame) => {
    const comparison = frame.meta?.timeCompare;
    return comparison?.isTimeShiftQuery && Number.isFinite(comparison.diffMs)
      ? alignComparison(frame, comparison.diffMs)
      : frame;
  });
}

export function hasTimeSeriesSamples(frames: DataFrame[]): boolean {
  return frames.some((frame) => frame.length > 0 && frame.fields.some((field) =>
    field.type === "time" && field.values.some((value) => Number.isFinite(value))));
}

export function timeSeriesLegend(options: Record<string, unknown>) {
  const legend = (options.legend && typeof options.legend === "object"
    ? options.legend : {}) as Record<string, unknown>;
  return {
    ...legend,
    showLegend: legend.showLegend !== false && legend.displayMode !== "hidden",
    displayMode: legend.displayMode === "table" ? "table" : "list",
    placement: legend.placement === "right" ? "right" : "bottom",
    calcs: Array.isArray(legend.calcs) ? legend.calcs.filter((value) => typeof value === "string") : [],
  };
}

export function timeSeriesTooltip(options: Record<string, unknown>): {
  mode: "none" | "multi" | "single";
  sort: "none" | "asc" | "desc";
  hideZeros: boolean;
  maxHeight?: number;
  maxWidth?: number;
} {
  const tooltip = (options.tooltip && typeof options.tooltip === "object"
    ? options.tooltip : {}) as Record<string, unknown>;
  const dimension = (value: unknown) => typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
  return {
    mode: tooltip.mode === "none" ? "none" : tooltip.mode === "multi" ? "multi" : "single",
    sort: tooltip.sort === "asc" || tooltip.sort === "desc" ? tooltip.sort : "none",
    hideZeros: tooltip.hideZeros === true,
    maxHeight: dimension(tooltip.maxHeight),
    maxWidth: dimension(tooltip.maxWidth),
  };
}

/** null restores the saved visibility rather than rewriting the saved fields. */
export function changeSeriesVisibility(
  all: string[],
  visible: string[],
  labels: string | string[] | null,
  mode: "select" | "append" | "setExactly",
): string[] | null {
  if (labels === null) return null;
  const requested = new Set((Array.isArray(labels) ? labels : [labels]).filter((label) => all.includes(label)));
  if (mode === "setExactly") return all.filter((label) => requested.has(label));
  if (!requested.size) return visible;
  if (mode === "select") {
    if (visible.length === requested.size && visible.every((label) => requested.has(label))) return null;
    return all.filter((label) => requested.has(label));
  }
  const next = new Set(visible);
  requested.forEach((label) => next.has(label) ? next.delete(label) : next.add(label));
  return all.filter((label) => next.has(label));
}

export function tooltipFieldIndices(
  frame: DataFrame,
  seriesIndex: number | null,
  rowIndex: number,
  mode: "single" | "multi" | "none",
  hideZeros: boolean,
  sort: "asc" | "desc" | "none",
): number[] {
  if (mode === "none") return [];
  let indices = frame.fields.map((_, index) => index).filter((index) => {
    const field = frame.fields[index];
    const hide = field.config.custom?.hideFrom;
    return (field.type === "number" || field.type === "enum") && !hide?.viz && !hide?.tooltip &&
      (mode !== "single" || index === seriesIndex) && (!hideZeros || field.values[rowIndex] !== 0);
  });
  if (sort !== "none") indices = indices.sort((left, right) => {
    const a = frame.fields[left].values[rowIndex], b = frame.fields[right].values[rowIndex];
    if (a == null || b == null) return a == null ? b == null ? 0 : 1 : -1;
    return (sort === "desc" ? -1 : 1) * (Number(a) - Number(b));
  });
  return indices;
}

export function setFieldVisibility(field: Field, visible: boolean) {
  field.config.custom = {
    ...field.config.custom,
    hideFrom: { ...field.config.custom?.hideFrom, viz: !visible },
  };
}

export function annotationGeometry(
  event: { time: number; timeEnd: number },
  start: number,
  end: number,
  width: number,
  project: (time: number) => number,
) {
  if (![event.time, event.timeEnd, start, end, width].every(Number.isFinite) ||
    width <= 0 || end <= start || event.timeEnd < event.time || event.time > end || event.timeEnd < start) return undefined;
  const left = Math.max(0, Math.min(width, project(Math.max(start, event.time))));
  const right = Math.max(left, Math.min(width, project(Math.min(end, event.timeEnd))));
  if (!Number.isFinite(left) || !Number.isFinite(right)) return undefined;
  return { left, right, region: event.timeEnd > event.time };
}

/** uPlot interpolates fractional milliseconds; the range API requires integers. */
export function timeSeriesZoomSelection(range: { from: number; to: number }, timezone: string) {
  const from = Math.round(range.from), to = Math.round(range.to);
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from < 0 || to <= from)
    return undefined;
  return { from, to, timezone };
}
