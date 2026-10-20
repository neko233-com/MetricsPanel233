import {
  useCallback,
  useDeferredValue,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  alignTimeRangeCompareData,
  createTheme,
  formattedValueToString,
  getDisplayProcessor,
  getFieldDisplayName,
  getFieldSeriesColor,
  type DataFrame,
  type Field,
} from "@grafana/data";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { Pencil, Trash2 } from "lucide-react";
import { AnnotationEditor } from "../../components/AnnotationEditor";
import { getLocale, t } from "../../i18n";
import { annotationStateLabel, type Annotation } from "../../grafana/annotations";
import { resolveTimeRange } from "../../grafana/time-range";
import {
  annotationGeometry,
  changeSeriesVisibility,
  hasTimeSeriesSamples,
  prepareTimeSeriesFrames,
  timeSeriesLegend,
  timeSeriesTooltip,
  timeSeriesZoomSelection,
} from "../../grafana/time-series";
import type { RendererProps } from "../types";
import "./TimeSeriesRenderer.css";

const theme = createTheme({ colors: { mode: "dark" } });
const COLORS = ["#5794f2", "#73bf69", "#ff9830", "#f2495c", "#b877d9", "#56a3a6", "#fade2a"];

// uPlot and browser tests read pixels repeatedly. Mark 2D contexts so Chrome
// uses a readback-friendly backing store instead of emitting a warning.
if (typeof HTMLCanvasElement !== "undefined") {
  const prototype = HTMLCanvasElement.prototype as typeof HTMLCanvasElement.prototype & {
    __metricspanelReadFrequently?: boolean;
  };
  if (!prototype.__metricspanelReadFrequently) {
    const original = prototype.getContext;
    prototype.getContext = function (this: HTMLCanvasElement, ...args: any[]) {
      if (args[0] === "2d") {
        return original.call(this, "2d", {
          ...(args[1] || {}),
          willReadFrequently: true,
        }) as any;
      }
      return original.apply(this, args as any) as any;
    } as typeof prototype.getContext;
    prototype.__metricspanelReadFrequently = true;
  }
}

type PlotSeries = {
  id: string;
  name: string;
  field: Field;
  values: (number | null)[];
  color: string;
  dashed: boolean;
  axis: "y" | "y2";
  axisLabel?: string;
  log: boolean;
  stack: "none" | "normal" | "percent";
  stackGroup: string;
  defaultVisible: boolean;
};
type PlotModel = {
  data: uPlot.AlignedData;
  series: PlotSeries[];
  signature: string;
};
type CursorState = { index: number; left: number; top: number };
type PlotBounds = {
  left: number;
  top: number;
  width: number;
  height: number;
  start: number;
  end: number;
};

type Props = RendererProps;

function timestamp(value: unknown): number {
  if (value instanceof Date) return value.getTime();
  const result = Number(value);
  return Number.isFinite(result) ? result : NaN;
}

function makePlotModel(frames: DataFrame[]): PlotModel {
  const source = frames.flatMap((frame, frameIndex) => {
    const time = frame.fields.find((field) => field.type === "time");
    const values = frame.fields
      .map((field, fieldIndex) => ({ field, fieldIndex }))
      .filter(({ field }) => field.type === "number" || field.type === "enum");
    if (!time || values.length === 0) return [];
    return [{ frame, frameIndex, time, values, times: Array.from(time.values, timestamp) }];
  });
  const referenceTimes = source[0]?.times || [];
  const alignedAndSorted = source.every((entry) =>
    entry.times.length === referenceTimes.length &&
    entry.times.every((value, index) => value === referenceTimes[index] && Number.isFinite(value)) &&
    entry.times.every((value, index) => index === 0 || value > entry.times[index - 1]),
  );
  let x: number[];
  if (alignedAndSorted) {
    x = referenceTimes;
  } else {
    const allTimes = new Set<number>();
    source.forEach((entry) => entry.times.forEach((value) => {
      if (Number.isFinite(value)) allTimes.add(value);
    }));
    x = Array.from(allTimes).sort((left, right) => left - right);
  }
  const positions = new Map(x.map((value, index) => [value, index]));
  const names = new Set<string>();
  const series: PlotSeries[] = [];
  for (const entry of source) {
    const comparison =
      (entry.frame.meta as { timeCompare?: { isTimeShiftQuery?: boolean } } | undefined)
        ?.timeCompare?.isTimeShiftQuery === true;
    for (const { field, fieldIndex } of entry.values) {
      let name = comparison
        ? (field.name || getFieldDisplayName(field, entry.frame, frames)).replace(/\s*\((?:comparison|比较)\)$/i, "")
        : getFieldDisplayName(field, entry.frame, frames);
      if (comparison) name += ` (${t("Comparison")})`;
      if (names.has(name)) name += " (" + (entry.frame.refId || String(entry.frameIndex + 1)) + ")";
      names.add(name);
      const values: (number | null)[] = Array.from({ length: x.length }, () => null);
      for (let row = 0; row < entry.times.length; row += 1) {
        const position = positions.get(entry.times[row]);
        const raw = field.values[row];
        if (raw == null) continue;
        const value = Number(raw);
        if (position !== undefined && Number.isFinite(value)) values[position] = value;
      }
      let color = COLORS[series.length % COLORS.length];
      try {
        color = getFieldSeriesColor(field, theme).color || color;
      } catch {
        // Invalid plugin-provided color metadata should not prevent chart rendering.
      }
      const custom = (field.config.custom || {}) as {
        axisPlacement?: string;
        axisLabel?: string;
        scaleDistribution?: { type?: string };
        stacking?: { mode?: string; group?: string };
        hideFrom?: { viz?: boolean };
      };
      const stackMode = custom.stacking?.mode;
      series.push({
        id: entry.frame.refId + ":" + fieldIndex + ":" + name,
        name,
        field,
        values,
        color,
        dashed: comparison,
        axis: custom.axisPlacement === "right" ? "y2" : "y",
        axisLabel: custom.axisLabel,
        log: custom.scaleDistribution?.type === "log",
        stack: stackMode === "percent" ? "percent" : stackMode === "normal" ? "normal" : "none",
        stackGroup: custom.stacking?.group || "default",
        defaultVisible: !custom.hideFrom?.viz,
      });
    }
  }
  applyStacking(series, x.length);
  const signature = series.map((item) => item.id + ":" + item.color).join("|");
  return { data: [x, ...series.map((item) => item.values)], series, signature };
}

function applyStacking(series: PlotSeries[], length: number) {
  const groups = new Map<string, PlotSeries[]>();
  for (const item of series) {
    if (item.stack === "none") continue;
    const key = `${item.axis}:${item.stackGroup}`;
    groups.set(key, [...(groups.get(key) || []), item]);
  }
  for (const members of groups.values()) {
    const mode = members[0].stack;
    for (let index = 0; index < length; index += 1) {
      const raw = members.map((item) => item.values[index]);
      const total = mode === "percent"
        ? raw.reduce<number>((sum, value) => sum + Math.max(0, value ?? 0), 0)
        : 0;
      let cumulative = 0;
      for (let member = 0; member < members.length; member += 1) {
        const value = raw[member];
        if (value == null) continue;
        const part = mode === "percent"
          ? total > 0 ? Math.max(0, value) / total * 100 : 0
          : value;
        cumulative += part;
        members[member].values[index] = cumulative;
      }
    }
  }
}

function formatValue(field: Field, value: unknown, timezone: string): string {
  if (value == null) return "—";
  try {
    const display =
      field.display ||
      getDisplayProcessor({ field, timeZone: timezone, theme });
    return formattedValueToString(display(value));
  } catch {
    return String(value);
  }
}

function AnnotationOverlay({
  bounds,
  events,
  timezone,
}: {
  bounds?: PlotBounds;
  events: Annotation[];
  timezone: string;
}) {
  if (!bounds || !events.length) return null;
  return (
    <svg
      className="grafana-timeseries-annotations native-timeseries-annotations"
      width={bounds.width}
      height={bounds.height}
      style={{ left: bounds.left, top: bounds.top }}
      aria-label={t("Annotations")}
    >
      {events.map((event, index) => {
        const geometry = annotationGeometry(event, bounds.start, bounds.end, bounds.width,
          (time) => (time - bounds.start) / (bounds.end - bounds.start) * bounds.width);
        if (!geometry) return null;
        const color = event.color || "#5794f2";
        const date = new Date(event.time).toLocaleString(undefined, {
          timeZone: timezone === "browser" ? undefined : timezone === "utc" ? "UTC" : timezone,
        });
        const title = event.text +
          (event.newState
            ? "\n" + t("Alert state") + ": " +
              annotationStateLabel(event.prevState || "Normal", t) + " → " +
              annotationStateLabel(event.newState, t)
            : "") +
          "\n" + event.tags.join(", ") + "\n" + date;
        return (
          <g
            key={event.key || event.id || index}
            className="annotation-marker"
            data-annotation-key={event.key}
            data-annotation-id={event.id}
            data-alert-state={event.newState}
            role="img"
            aria-label={t("Annotation") + ": " + event.text}
          >
            <title>{title}</title>
            {geometry.region ? (
              <rect
                x={geometry.left}
                y={0}
                width={geometry.right - geometry.left}
                height={bounds.height}
                fill={color}
                opacity={0.12}
              />
            ) : null}
            <line x1={geometry.left} x2={geometry.left} y1={0} y2={bounds.height}
              stroke={color} strokeDasharray="3 3" opacity={0.8} />
            {geometry.region ? (
              <line x1={geometry.right} x2={geometry.right} y1={0} y2={bounds.height}
                stroke={color} strokeDasharray="3 3" opacity={0.6} />
            ) : null}
            <circle cx={geometry.left} cy={7} r={4} fill={color} className="native-timeseries-annotation-marker" />
          </g>
        );
      })}
    </svg>
  );
}

export default function TimeSeriesRenderer({
  panel,
  frames,
  range,
  timeInfo,
  controls,
  annotations = [],
  annotationDashboard,
  annotationError,
  queryError,
  loading = false,
  streaming = false,
  onRange = () => {},
  onEdit,
  onRemove,
}: Props) {
  const host = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | undefined>(undefined);
  const lastData = useRef<uPlot.AlignedData | undefined>(undefined);
  const onRangeRef = useRef(onRange);
  const rangeRef = useRef<ReturnType<typeof resolveTimeRange> | undefined>(undefined);
  const ready = useRef(false);
  const [bounds, setBounds] = useState<PlotBounds | undefined>(undefined);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [plot, setPlot] = useState<uPlot | undefined>(undefined);
  const [cursor, setCursor] = useState<CursorState | undefined>(undefined);
  const [visible, setVisible] = useState<string[] | null>(null);
  const [legendSort, setLegendSort] = useState<"name" | "value">("name");
  const resolved = resolveTimeRange(range);
  onRangeRef.current = onRange;
  rangeRef.current = resolved;
  const options = panel.config?.options || {};
  // Defer heavy frame normalization so cursor/legend input keeps priority.
  const deferredFrames = useDeferredValue(frames);
  const baseFrames = useMemo(
    () =>
      prepareTimeSeriesFrames(deferredFrames, (frame, diff) =>
        alignTimeRangeCompareData(frame, diff, theme),
      ),
    [deferredFrames, getLocale()],
  );
  const model = useMemo(() => makePlotModel(baseFrames), [baseFrames]);
  const ids = useMemo(() => model.series.map((item) => item.id), [model]);
  const defaultVisible = useMemo(
    () => model.series.filter((item) => item.defaultVisible).map((item) => item.id),
    [model],
  );
  const activeIds = visible ?? defaultVisible;
  const activeSet = useMemo(() => new Set(activeIds), [activeIds]);
  const legend = timeSeriesLegend(options);
  const tooltip = timeSeriesTooltip(options);
  const leftSeries = model.series.filter((item) => item.axis === "y");
  const rightSeries = model.series.filter((item) => item.axis === "y2");
  const percentStacked = model.series.some((item) => item.stack === "percent");
  const leftLabel = leftSeries.find((item) => item.axisLabel)?.axisLabel;
  const rightLabel = rightSeries.find((item) => item.axisLabel)?.axisLabel;
  const leftLog = leftSeries.some((item) => item.log);
  const rightLog = rightSeries.some((item) => item.log);
  const hasSamples = hasTimeSeriesSamples(baseFrames);
  const canRender = size.width > 0 && size.height > 0 && model.series.length > 0 && model.data[0].length > 0;
  const onZoom = useCallback((from: number, to: number) => {
    const next = timeSeriesZoomSelection({ from, to }, rangeRef.current?.timezone || "browser");
    if (!next) return;
    const current = rangeRef.current;
    if (current && Math.round(current.start) === next.from && Math.round(current.end) === next.to) return;
    onRangeRef.current?.(next);
  }, []);

  useEffect(() => {
    setVisible(null);
  }, [model.signature]);

  useLayoutEffect(() => {
    const element = host.current;
    if (!element || !canRender) return;
    const locale = getLocale() === "zh" ? "zh-CN" : "en-US";
    const axisTime = new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZone: resolved.timezone === "browser" ? undefined :
        resolved.timezone === "utc" ? "UTC" : resolved.timezone,
    });
    const chartOptions: uPlot.Options = {
      width: size.width,
      height: size.height,
      ms: 1,
      class: "native-timeseries-plot",
      scales: {
        x: { time: true, min: resolved.start, max: resolved.end },
        y: {
          auto: !percentStacked,
          range: percentStacked ? [0, 100] : undefined,
          distr: leftLog ? 3 : 1,
          log: 10,
        },
        ...(rightSeries.length
          ? {
              y2: {
                auto: !percentStacked,
                range: percentStacked ? [0, 100] : undefined,
                distr: rightLog ? 3 : 1,
                log: 10,
              },
            }
          : {}),
      },
      axes: [
        {
          scale: "x",
          stroke: "rgba(180, 190, 205, 0.65)",
          grid: { stroke: "rgba(180, 190, 205, 0.10)", width: 1 },
          ticks: { stroke: "rgba(180, 190, 205, 0.16)", width: 1 },
          values: (_plot, ticks) => ticks.map((value) => axisTime.format(new Date(value))),
        },
        {
          scale: "y",
          label: leftLabel,
          labelSize: 18,
          stroke: "rgba(180, 190, 205, 0.65)",
          grid: { stroke: "rgba(180, 190, 205, 0.10)", width: 1 },
          ticks: { stroke: "rgba(180, 190, 205, 0.16)", width: 1 },
          ...(percentStacked
            ? { values: (_plot: uPlot, ticks: number[]) => ticks.map((value) => `${Math.round(value)}%`) }
            : {}),
        },
        ...(rightSeries.length
          ? [{
              scale: "y2",
              side: 1 as const,
              label: rightLabel,
              labelSize: 18,
              stroke: "rgba(180, 190, 205, 0.65)",
              grid: { show: false },
              ticks: { stroke: "rgba(180, 190, 205, 0.16)", width: 1 },
            }]
          : []),
      ],
      series: [
        { label: t("Time") },
        ...model.series.map((item) => ({
          label: item.name,
          scale: item.axis,
          stroke: item.color,
          width: 1.5,
          dash: item.dashed ? [1, 5, 4, 5] : undefined,
          show: activeSet.has(item.id),
          points: { show: false },
          spanGaps: false,
        })),
      ],
      legend: { show: false },
      cursor: {
        drag: { x: true, y: false, setScale: true },
        focus: { prox: 24 },
      },
      hooks: {
        setScale: [(chart, scale) => {
          if (!ready.current || scale !== "x") return;
          const min = chart.scales.x.min;
          const max = chart.scales.x.max;
          if (min != null && max != null) onZoom(min, max);
        }],
        setCursor: [(chart) => {
          const index = chart.cursor.idx;
          const left = chart.cursor.left;
          const top = chart.cursor.top;
          if (index == null || left == null || top == null) {
            setCursor(undefined);
            return;
          }
          setCursor((previous) =>
            previous && previous.index === index && previous.left === left && previous.top === top
              ? previous
              : { index, left: Math.round(left), top: Math.round(top) },
          );
        }],
        draw: [(chart) => {
          const ratio = window.devicePixelRatio || 1;
          const next = {
            left: chart.bbox.left / ratio,
            top: chart.bbox.top / ratio,
            width: chart.bbox.width / ratio,
            height: chart.bbox.height / ratio,
            start: Number(chart.scales.x.min),
            end: Number(chart.scales.x.max),
          };
          if (![next.left, next.top, next.width, next.height, next.start, next.end].every(Number.isFinite) ||
            next.width <= 0 || next.height <= 0 || next.end <= next.start) return;
          setBounds((previous) =>
            previous && Object.keys(next).every((key) =>
              previous[key as keyof typeof previous] === next[key as keyof typeof next])
              ? previous
              : next,
          );
        }],
      },
    };
    ready.current = false;
    const next = new uPlot(chartOptions, model.data, element);
    plotRef.current = next;
    lastData.current = model.data;
    ready.current = true;
    setPlot(next);
    return () => {
      ready.current = false;
      plotRef.current = undefined;
      lastData.current = undefined;
      setPlot(undefined);
      next.destroy();
    };
  }, [canRender, model.signature]);

  useLayoutEffect(() => {
    const chart = plotRef.current;
    if (chart && lastData.current !== model.data) {
      chart.setData(model.data, false);
      lastData.current = model.data;
    }
  }, [model.data]);

  useLayoutEffect(() => {
    plotRef.current?.setSize({
      width: Math.max(1, size.width),
      height: Math.max(1, size.height),
    });
  }, [size.width, size.height]);

  useLayoutEffect(() => {
    const chart = plotRef.current;
    if (!chart) return;
    model.series.forEach((item, index) => {
      chart.setSeries(index + 1, { show: activeSet.has(item.id) });
    });
  }, [activeSet, model.signature]);

  useLayoutEffect(() => {
    const chart = plotRef.current;
    if (!chart) return;
    const min = chart.scales.x.min;
    const max = chart.scales.x.max;
    if (min !== resolved.start || max !== resolved.end)
      chart.setScale("x", { min: resolved.start, max: resolved.end });
  }, [resolved.start, resolved.end]);

  useEffect(() => {
    const element = host.current;
    if (!element) return;
    const measure = () => {
      const rect = element.getBoundingClientRect();
      setSize({ width: Math.floor(rect.width), height: Math.floor(rect.height) });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const tooltipItems = useMemo(() => {
    if (!cursor || cursor.index < 0 || tooltip.mode === "none") return [];
    const candidates = model.series
      .map((item, index) => ({
        item,
        index,
        value: item.values[cursor.index],
        y: item.values[cursor.index] == null
          ? Infinity
          : Math.abs((plot?.valToPos(item.values[cursor.index]!, "y") || 0) - cursor.top),
      }))
      .filter(({ item, value }) => activeSet.has(item.id) && value != null &&
        (!tooltip.hideZeros || value !== 0));
    const sorted = tooltip.sort === "none"
      ? candidates
      : [...candidates].sort((left, right) =>
          tooltip.sort === "desc"
            ? Number(right.value) - Number(left.value)
            : Number(left.value) - Number(right.value),
        );
    return tooltip.mode === "single"
      ? [...sorted].sort((left, right) => left.y - right.y).slice(0, 1)
      : sorted;
  }, [activeSet, cursor, model, plot, tooltip.hideZeros, tooltip.mode, tooltip.sort]);

  const sortedLegend = useMemo(() => {
    const result = [...model.series];
    return result.sort((left, right) => {
      if (legendSort === "name") return left.name.localeCompare(right.name);
      const last = (values: (number | null)[]) => {
        for (let index = values.length - 1; index >= 0; index -= 1)
          if (values[index] != null) return values[index]!;
        return Number.NEGATIVE_INFINITY;
      };
      return last(right.values) - last(left.values);
    });
  }, [legendSort, model]);

  return (
    <section
      className="chart-panel native-timeseries-panel"
      data-panel-id={panel.id}
      data-series-count={model.series.length}
      aria-label={t(panel.title)}
    >
      <div className="panel-heading">
        <h2>{t(panel.title)}</h2>
        <div className="panel-actions">
          {controls}
          {annotationDashboard ? (
            <AnnotationEditor
              dashboard={annotationDashboard}
              panelId={Number(panel.config?.id || 0)}
              time={Math.round((resolved.start + resolved.end) / 2)}
              events={annotations}
            />
          ) : null}
          {streaming ? <span className="status healthy" role="status"><i />{t("Live")}</span> : null}
          {onEdit ? (
            <button className="icon-button" aria-label={t("Edit") + " " + t(panel.title)} onClick={onEdit}>
              <Pencil size={15} />
            </button>
          ) : null}
          {onRemove ? (
            <button className="icon-button danger-hover" aria-label={t("Remove") + " " + t(panel.title)} onClick={onRemove}>
              <Trash2 size={15} />
            </button>
          ) : null}
        </div>
      </div>
      {timeInfo ? <p className="panel-time-info">{timeInfo}</p> : null}
      {annotationError || queryError ? (
        <p role="alert" className="form-error">{t(annotationError || queryError || "")}</p>
      ) : null}
      <div className="native-timeseries-viewport">
        <div className="native-timeseries-plot-host" ref={host} onDoubleClick={() => onZoom(resolved.start, resolved.end)} />
        {plot ? <AnnotationOverlay bounds={bounds} events={annotations} timezone={resolved.timezone} /> : null}
        {cursor && tooltipItems.length ? (
          <div
            className="grafana-timeseries-tooltip native-timeseries-tooltip"
            role="status"
            style={{
              left: Math.max(8, Math.min(size.width - 260, cursor.left + 14)),
              top: Math.max(8, Math.min(size.height - 120, cursor.top + 14)),
              maxHeight: tooltip.maxHeight,
              maxWidth: tooltip.maxWidth,
            }}
          >
            <time>
              {new Date(Number(model.data[0][cursor.index])).toLocaleString(undefined, {
                timeZone: resolved.timezone === "browser"
                  ? undefined
                  : resolved.timezone === "utc"
                    ? "UTC"
                    : resolved.timezone,
              })}
            </time>
            {tooltipItems.map(({ item, value }) => (
              <div className="native-timeseries-tooltip-row" key={item.id}>
                <i style={{ background: item.color }} />
                <span>{item.name}</span>
                <strong>{formatValue(item.field, value, resolved.timezone)}</strong>
              </div>
            ))}
          </div>
        ) : null}
        {!hasSamples ? (
          <div className="native-timeseries-empty" role="status">
            {loading ? t("Loading metrics…") : t("No samples in this time range")}
          </div>
        ) : null}
      </div>
      {legend.showLegend && model.series.length ? (
        <div className={"native-timeseries-legend " + (legend.placement === "right" ? "right" : "bottom")}>
          <div className="native-timeseries-legend-heading">
            <span>{t("Legend")}</span>
            <button
              type="button"
              aria-label={t("Sort by name or value")}
              onClick={() => setLegendSort((current) => current === "name" ? "value" : "name")}
            >
              {legendSort === "name" ? t("Name") : t("Value")}
            </button>
          </div>
          <div className="native-timeseries-legend-list">
            {sortedLegend.map((item) => {
              let last: number | null = null;
              for (let index = item.values.length - 1; index >= 0; index -= 1) {
                if (item.values[index] != null) {
                  last = item.values[index];
                  break;
                }
              }
              return (
                <div key={item.id} className="native-timeseries-legend-item">
                  <button
                    type="button"
                    aria-pressed={activeSet.has(item.id)}
                    onClick={(event) => setVisible((current) =>
                      changeSeriesVisibility(
                        ids,
                        current ?? defaultVisible,
                        item.id,
                        event.ctrlKey || event.metaKey ? "append" : "select",
                      ),
                    )}
                  >
                    <i style={{ background: item.color }} data-testid="series-icon" />
                    <span>{item.name}</span>
                  </button>
                  <strong>{formatValue(item.field, last, resolved.timezone)}</strong>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}
    </section>
  );
}
