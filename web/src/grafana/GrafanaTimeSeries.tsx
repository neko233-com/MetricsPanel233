import {
  Component,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  CoreApp,
  alignTimeRangeCompareData,
  createTheme,
  DashboardCursorSync,
  EventBusSrv,
  formattedValueToString,
  getDisplayProcessor,
  getFieldDisplayName,
  getFieldSeriesColor,
  ThemeContext,
  type DataFrame,
  type EventBus,
} from "@grafana/data";
import {
  EventBusPlugin,
  KeyboardPlugin,
  PanelContextProvider,
  SeriesTable,
  TimeSeries,
  TooltipPlugin,
  ZoomPlugin,
  type PanelContext,
  type UPlotConfigBuilder,
  type VizLegendOptions,
} from "@grafana/ui";
import { SortOrder, TooltipDisplayMode } from "@grafana/schema";
import type uPlot from "uplot";
import { Pencil, Trash2 } from "lucide-react";
import type { Dashboard, Panel } from "../api";
import { AnnotationEditor } from "../components/AnnotationEditor";
import { getLocale, t } from "../i18n";
import { annotationStateLabel, type Annotation } from "./annotations";
import { connectPanelEvents } from "./app-events";
import { resolveTimeRange, type TimeSelection } from "./time-range";
import {
  annotationGeometry,
  changeSeriesVisibility,
  cloneTimeSeriesFrames,
  hasTimeSeriesSamples,
  prepareTimeSeriesFrames,
  setFieldVisibility,
  timeSeriesLegend,
  timeSeriesTooltip,
  timeSeriesZoomSelection,
  tooltipFieldIndices,
} from "./time-series";
import "./GrafanaTimeSeries.css";

const theme = createTheme({ colors: { mode: "dark" } });

type Props = {
  panel: Panel;
  frames: DataFrame[];
  range: TimeSelection;
  timeInfo?: string;
  controls?: ReactNode;
  annotations?: Annotation[];
  annotationDashboard?: Dashboard;
  annotationError?: string;
  queryError?: string;
  loading?: boolean;
  streaming?: boolean;
  onRange: (range: TimeSelection) => void;
  onEdit?: () => void;
  onRemove?: () => void;
  eventBus?: EventBus;
  cursorSync?: DashboardCursorSync;
};

class PlotBoundary extends Component<{ children: ReactNode }, { error: string }> {
  state = { error: "" };
  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error ? error.message : String(error) };
  }
  render() {
    return this.state.error
      ? <p role="alert" className="form-error">{t("Chart error")}: {this.state.error}</p>
      : this.props.children;
  }
}

function AnnotationOverlay({ config, events, timezone }: {
  config: UPlotConfigBuilder;
  events: Annotation[];
  timezone: string;
}) {
  const [bounds, setBounds] = useState<{
    plot: uPlot; left: number; top: number; width: number; height: number; start: number; end: number;
  }>();
  useLayoutEffect(() => {
    let disposed = false;
    const update = (plot: uPlot) => {
      if (disposed) return;
      const ratio = window.devicePixelRatio || 1;
      const next = {
        plot,
        left: plot.bbox.left / ratio,
        top: plot.bbox.top / ratio,
        width: plot.bbox.width / ratio,
        height: plot.bbox.height / ratio,
        start: Number(plot.scales.x.min),
        end: Number(plot.scales.x.max),
      };
      if (![next.left, next.top, next.width, next.height, next.start, next.end].every(Number.isFinite) ||
        next.width <= 0 || next.height <= 0 || next.end <= next.start) return;
      setBounds((previous) => previous && Object.keys(next).every((key) =>
        previous[key as keyof typeof previous] === next[key as keyof typeof next]) ? previous : next);
    };
    config.addHook("init", update);
    config.addHook("draw", update);
    return () => { disposed = true; };
  }, [config]);
  if (!bounds || !events.length) return null;
  return (
    <svg
      className="grafana-timeseries-annotations"
      width={bounds.width}
      height={bounds.height}
      style={{ left: bounds.left, top: bounds.top }}
      aria-label={t("Annotations")}
    >
      {events.map((event, index) => {
        const geometry = annotationGeometry(event, bounds.start, bounds.end, bounds.width,
          (time) => bounds.plot.valToPos(time, "x"));
        if (!geometry) return null;
        const color = event.color || "#5794f2";
        const timestamp = new Date(event.time).toLocaleString(undefined, {
          timeZone: timezone === "browser" ? undefined : timezone === "utc" ? "UTC" : timezone,
        });
        const title = `${event.text}${event.newState ? `\n${t("Alert state")}: ${annotationStateLabel(event.prevState || "Normal", t)} → ${annotationStateLabel(event.newState, t)}` : ""}\n${event.tags.join(", ")}\n${timestamp}`;
        return (
          <g key={event.key || event.id || index} className="annotation-marker" data-annotation-key={event.key}
            data-annotation-id={event.id} data-alert-state={event.newState}
            role="img" aria-label={`${t("Annotation")}: ${event.text}`}>
            <title>{title}</title>
            {geometry.region ? <rect x={geometry.left} y={0} width={geometry.right - geometry.left}
              height={bounds.height} fill={color} opacity={0.12} /> : null}
            <line x1={geometry.left} x2={geometry.left} y1={0} y2={bounds.height}
              stroke={color} strokeDasharray="3 3" opacity={0.8} />
            {geometry.region ? <line x1={geometry.right} x2={geometry.right} y1={0} y2={bounds.height}
              stroke={color} strokeDasharray="3 3" opacity={0.6} /> : null}
            <circle cx={geometry.left} cy={7} r={4} fill={color} className="grafana-timeseries-annotation-marker" />
          </g>
        );
      })}
    </svg>
  );
}

export default function GrafanaTimeSeries({
  panel, frames, range, timeInfo, controls, annotations = [], annotationDashboard,
  annotationError, queryError, loading = false, streaming = false, onRange, onEdit, onRemove,
  eventBus, cursorSync = DashboardCursorSync.Off,
}: Props) {
  const host = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [visible, setVisible] = useState<string[] | null>(null);
  const [legendSort, setLegendSort] = useState<{ sortBy: string; sortDesc: boolean }>();
  const localBus = useMemo(() => new EventBusSrv(), []);
  const bus = eventBus || localBus;
  useEffect(() => connectPanelEvents(localBus), [localBus]);
  useLayoutEffect(() => {
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
  const resolved = resolveTimeRange(range);
  const selectRange = useRef({ onRange, timezone: resolved.timezone });
  selectRange.current = { onRange, timezone: resolved.timezone };
  const onZoom = useCallback((next: { from: number; to: number }) => {
    const selection = timeSeriesZoomSelection(next, selectRange.current.timezone);
    if (selection) selectRange.current.onRange(selection);
  }, []);
  const options = useMemo(() => panel.config?.options || {}, [panel.config?.options]);
  const baseFrames = useMemo(() => {
    const prepared = prepareTimeSeriesFrames(frames,
      (frame, diff) => alignTimeRangeCompareData(frame, diff, theme));
    prepared.forEach((frame) => {
      if (frame.meta?.timeCompare?.isTimeShiftQuery) frame.fields.forEach((field) => {
        if (field.type === "number" || field.type === "enum") {
          const displayName = getFieldDisplayName(field, frame, prepared).replace(/ \(comparison\)$/,
            ` (${t("Comparison")})`);
          field.config = { ...field.config, displayName };
          field.state = { ...field.state, displayName, multipleFrames: prepared.length > 1 };
        }
      });
    });
    // The SDK join clears cached names. These copies already use current-window
    // timestamps and explicit localized names, so suppress its automatic suffix.
    prepared.forEach((frame) => {
      if (frame.meta?.timeCompare?.isTimeShiftQuery) frame.meta = {
        ...frame.meta,
        timeCompare: { ...frame.meta.timeCompare, isTimeShiftQuery: false },
      };
    });
    return prepared;
  }, [frames, getLocale()]);
  const names = useMemo(() => [...new Set(baseFrames.flatMap((frame) => frame.fields
    .filter((field) => field.type === "number" || field.type === "enum")
    .map((field) => getFieldDisplayName(field, frame, baseFrames))))], [baseFrames]);
  const defaultVisible = useMemo(() => [...new Set(baseFrames.flatMap((frame) => frame.fields
    .filter((field) => (field.type === "number" || field.type === "enum") && !field.config.custom?.hideFrom?.viz)
    .map((field) => getFieldDisplayName(field, frame, baseFrames))))], [baseFrames]);
  const plotFrames = useMemo(() => {
    const cloned = cloneTimeSeriesFrames(baseFrames);
    if (visible !== null) cloned.forEach((frame) => frame.fields.forEach((field) => {
      if (field.type === "number" || field.type === "enum")
        setFieldVisibility(field, visible.includes(getFieldDisplayName(field, frame, cloned)));
    }));
    return cloned;
  }, [baseFrames, visible]);
  const legend = useMemo(() => ({ ...timeSeriesLegend(options), ...legendSort }) as VizLegendOptions,
    [options, legendSort]);
  const tooltip = timeSeriesTooltip(options);
  const sync = () => cursorSync;
  const context: PanelContext = {
    eventsScope: `panel-${panel.id}`,
    eventBus: bus,
    app: CoreApp.Dashboard,
    sync,
    onToggleSeriesVisibility: (labels, mode) => setVisible((current) =>
      changeSeriesVisibility(names, current ?? defaultVisible, labels, mode)),
    onToggleLegendSort: (sortBy) => setLegendSort((current) => ({ sortBy,
      sortDesc: current?.sortBy === sortBy ? !current.sortDesc : false })),
  };
  const renderTooltip = (aligned: DataFrame, seriesIndex: number | null, rowIndex: number | null) => {
    if (rowIndex === null) return null;
    const indices = tooltipFieldIndices(aligned, seriesIndex, rowIndex, tooltip.mode,
      tooltip.hideZeros, tooltip.sort);
    if (!indices.length) return null;
    const time = aligned.fields.find((field) => field.type === "time");
    const timestamp = time
      ? formattedValueToString((time.display || getDisplayProcessor({ field: time, timeZone: resolved.timezone, theme }))(time.values[rowIndex]))
      : undefined;
    return <div className="grafana-timeseries-tooltip" style={{ maxHeight: tooltip.maxHeight,
      maxWidth: tooltip.maxWidth }}><SeriesTable timestamp={timestamp} series={indices.map((index) => {
        const field = aligned.fields[index];
        const value = (field.display || getDisplayProcessor({ field, timeZone: resolved.timezone, theme }))(field.values[rowIndex]);
        return { label: getFieldDisplayName(field, aligned, plotFrames),
          color: value.color || getFieldSeriesColor(field, theme).color,
          value: formattedValueToString(value), isActive: index === seriesIndex };
      })} /></div>;
  };
  const populated = hasTimeSeriesSamples(plotFrames);
  return (
    <section className="chart-panel grafana-sdk-timeseries" data-panel-id={panel.id}
      data-series-count={names.length} aria-label={t(panel.title)}>
      <div className="panel-heading">
        <h2>{t(panel.title)}</h2>
        <div className="panel-actions">
          {controls}
          {annotationDashboard ? <AnnotationEditor dashboard={annotationDashboard}
            panelId={Number(panel.config?.id || 0)} time={Math.round((resolved.start + resolved.end) / 2)}
            events={annotations} /> : null}
          {streaming ? <span className="status healthy" role="status"><i />{t("Live")}</span> : null}
          {onEdit ? <button className="icon-button" aria-label={`${t("Edit")} ${t(panel.title)}`}
            onClick={onEdit}><Pencil size={15} /></button> : null}
          {onRemove ? <button className="icon-button danger-hover" aria-label={`${t("Remove")} ${t(panel.title)}`}
            onClick={onRemove}><Trash2 size={15} /></button> : null}
        </div>
      </div>
      {timeInfo ? <p className="panel-time-info">{timeInfo}</p> : null}
      {annotationError || queryError ? <p role="alert" className="form-error">{t(annotationError || queryError || "")}</p> : null}
      <div className="grafana-timeseries-viewport" ref={host}>
        {populated && size.width > 0 && size.height > 0 ? (
          <ThemeContext.Provider value={theme}>
            <PanelContextProvider value={context}>
              <PlotBoundary key={`${panel.id}:${JSON.stringify(panel.config)}:${names.join("\n")}`}>
                <TimeSeries frames={plotFrames} width={size.width} height={size.height}
                  timeRange={resolved.sdk} timeZone={resolved.timezone} legend={legend} options={options}>
                  {(builder, aligned) => <>
                    <KeyboardPlugin config={builder} />
                    {tooltip.mode !== "none" ? <TooltipPlugin config={builder} data={aligned} frames={plotFrames}
                      timeZone={resolved.timezone} mode={tooltip.mode as TooltipDisplayMode}
                      sortOrder={tooltip.sort as SortOrder} sync={sync} renderTooltip={renderTooltip} /> : null}
                    <ZoomPlugin config={builder} onZoom={onZoom} withZoomY />
                    <EventBusPlugin config={builder} eventBus={bus} frame={aligned} />
                    <AnnotationOverlay config={builder} events={annotations} timezone={resolved.timezone} />
                  </>}
                </TimeSeries>
              </PlotBoundary>
            </PanelContextProvider>
          </ThemeContext.Provider>
        ) : <div className="grafana-timeseries-empty" role="status">
          {loading ? t("Loading metrics…") : t("No samples in this time range")}
        </div>}
      </div>
    </section>
  );
}
