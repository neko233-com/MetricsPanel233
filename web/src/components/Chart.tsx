import { t as tr } from "../i18n";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useId,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Pencil, Trash2 } from "lucide-react";
import {
  queryPanel,
  formatValue,
  message,
  type Panel,
  type QueryResult,
  type Dashboard,
} from "../api";
import {
  useAnnotations,
  annotationStateLabel,
  type Annotation,
} from "../grafana/annotations";
import { AnnotationEditor } from "./AnnotationEditor";
import { rawSelection, type TimeSelection } from "../grafana/time-range";
const palette = [
  "#ff9457",
  "#39d99c",
  "#85aaff",
  "#d598ed",
  "#e4c76a",
  "#5ac8de",
  "#f88d9a",
  "#b3cba0",
];
export function Chart({
  panel,
  range,
  tick,
  mint = false,
  onEdit,
  onRemove,
  result,
  resultError,
  formatter = formatValue,
  streaming = false,
  onRange,
  timeInfo,
  controls,
  annotations,
  annotationDashboard,
  annotationError,
}: {
  panel: Panel;
  range: TimeSelection;
  tick: number;
  mint?: boolean;
  onEdit?: () => void;
  onRemove?: () => void;
  result?: QueryResult;
  resultError?: string;
  formatter?: (value: number, unit?: string) => string;
  streaming?: boolean;
  onRange?: (value: TimeSelection) => void;
  timeInfo?: string;
  controls?: ReactNode;
  annotations?: Annotation[];
  annotationDashboard?: Dashboard;
  annotationError?: string;
}) {
  const [data, setData] = useState<QueryResult | null>(null);
  const [error, setError] = useState("");
  const [hover, setHover] = useState<number | null>(null);
  const [selection, setSelection] = useState<{
    start: number;
    end: number;
  } | null>(null);
  const selectionStart = useRef<{ time: number; x: number } | null>(null);
  const gradient = useId().replace(/:/g, "");
  const svgRef = useRef<SVGSVGElement>(null);
  const [svgWidth, setSVGWidth] = useState(580);
  const zone = rawSelection(range).timezone;
  const loadedAnnotations = useAnnotations(
    annotationDashboard,
    panel,
    range,
    tick,
    {},
    annotations !== undefined,
  );
  const events = annotations || loadedAnnotations.events;
  const timeLabel = (timestamp: number) =>
    new Date(timestamp).toLocaleString("en-GB", {
      ...(data && data.end - data.start >= 86400000
        ? ({ month: "short", day: "2-digit" } as const)
        : {}),
      hour: "2-digit",
      minute: "2-digit",
      timeZone: zone === "browser" ? undefined : zone === "utc" ? "UTC" : zone,
    });
  useLayoutEffect(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const resize = () =>
      setSVGWidth(Math.max(280, Math.round(svg.getBoundingClientRect().width)));
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(svg);
    return () => observer.disconnect();
  }, [panel.visualization]);
  const plotWidth = svgWidth - 102,
    right = svgWidth - 20,
    ticks = svgWidth < 420 ? 4 : 7;
  useEffect(() => {
    if (result) {
      setData(result);
      setError(resultError || "");
      return;
    }
    const controller = new AbortController();
    queryPanel(panel, range, controller.signal)
      .then((result) => {
        setData(result);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, [
    panel.metric,
    panel.expr,
    JSON.stringify(panel.expressions),
    panel.aggregation,
    JSON.stringify(panel.labels),
    range,
    tick,
    result,
    resultError,
  ]);
  const chart = useMemo(() => {
    const points = data?.series.flatMap((s) => s.points) || [];
    let min = 0,
      max = 1;
    for (const point of points) {
      min = Math.min(min, point.value);
      max = Math.max(max, point.value);
    }
    const span = (max - min) * 1.1;
    const start = data?.start || Date.now() - 1800000,
      end = data?.end || Date.now();
    return {
      min,
      max: min + span,
      start,
      end,
      x: (t: number) => 82 + ((t - start) / (end - start)) * plotWidth,
      y: (v: number) => 185 - ((v - min) / span) * 155,
    };
  }, [data, plotWidth]);
  const visibleSeries = data?.series.slice(0, 8) || [];
  const latest = visibleSeries[0]?.points.at(-1)?.value;
  const color = mint ? palette[1] : palette[0];
  if (panel.visualization === "stat")
    return (
      <section className="chart-panel stat-panel" aria-label={tr(panel.title)}>
        <div className="panel-heading">
          <h2>{tr(panel.title)}</h2>
          {onEdit && (
            <button
              className="icon-button"
              aria-label={`${tr("Edit")} ${tr(panel.title)}`}
              onClick={onEdit}
            >
              <Pencil size={15} />
            </button>
          )}
        </div>
        <div className="stat-value">
          {latest === undefined ? "—" : formatter(latest, panel.unit)}
        </div>
        {error && <p className="form-error">{error}</p>}
        <div className="chart-legend">
          <i style={{ background: color }} />
          <span className="mono">{panel.expr || panel.metric}</span>
        </div>
      </section>
    );
  if (panel.visualization === "table")
    return (
      <section className="chart-panel" aria-label={tr(panel.title)}>
        <div className="panel-heading">
          <h2>{tr(panel.title)}</h2>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{tr("Labels")}</th>
                <th>{tr("Metric")}</th>
              </tr>
            </thead>
            <tbody>
              {visibleSeries.map((s, i) => (
                <tr key={i}>
                  <td className="mono">
                    {Object.entries(s.labels)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(", ")}
                  </td>
                  <td>{formatter(s.points.at(-1)!.value, panel.unit)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {error && <p className="form-error">{error}</p>}
      </section>
    );
  return (
    <section className="chart-panel" aria-label={tr(panel.title)}>
      <div className="panel-heading">
        <h2>{tr(panel.title)}</h2>
        <div className="panel-actions">
          {controls}
          {annotationDashboard && (
            <AnnotationEditor
              dashboard={annotationDashboard}
              panelId={Number(panel.config?.id || 0)}
              time={Math.round((chart.start + chart.end) / 2)}
              events={events}
            />
          )}
          {streaming && (
            <span className="status healthy" role="status">
              <i />
              {tr("Live")}
            </span>
          )}
          {latest !== undefined && (
            <span className="latest-value">
              {formatter(latest, panel.unit)}
            </span>
          )}
          {onEdit && (
            <button
              className="icon-button"
              aria-label={`${tr("Edit")} ${tr(panel.title)}`}
              onClick={onEdit}
            >
              <Pencil size={15} />
            </button>
          )}
          {onRemove && (
            <button
              className="icon-button danger-hover"
              aria-label={`${tr("Remove")} ${tr(panel.title)}`}
              onClick={onRemove}
            >
              <Trash2 size={15} />
            </button>
          )}
        </div>
      </div>
      {timeInfo && <p className="panel-time-info">{timeInfo}</p>}
      {(annotationError || loadedAnnotations.error) && (
        <p role="alert" className="form-error">
          {tr(annotationError || loadedAnnotations.error)}
        </p>
      )}
      <div className="chart-wrap">
        <svg
          onPointerDown={(event) => {
            if (!onRange || event.button !== 0) return;
            const bounds = event.currentTarget.getBoundingClientRect();
            const x = ((event.clientX - bounds.left) / bounds.width) * svgWidth;
            if (x < 82 || x > right) return;
            const time =
              chart.start + ((x - 82) / plotWidth) * (chart.end - chart.start);
            selectionStart.current = { time, x: event.clientX };
            setSelection({ start: time, end: time });
            event.currentTarget.setPointerCapture(event.pointerId);
          }}
          onPointerMove={(event) => {
            if (!selectionStart.current) return;
            const bounds = event.currentTarget.getBoundingClientRect();
            const x = Math.max(
              82,
              Math.min(
                right,
                ((event.clientX - bounds.left) / bounds.width) * svgWidth,
              ),
            );
            setSelection({
              start: selectionStart.current.time,
              end:
                chart.start +
                ((x - 82) / plotWidth) * (chart.end - chart.start),
            });
          }}
          onPointerUp={(event) => {
            const start = selectionStart.current;
            if (start && Math.abs(event.clientX - start.x) > 5) {
              const bounds = event.currentTarget.getBoundingClientRect();
              const x = Math.max(
                82,
                Math.min(
                  right,
                  ((event.clientX - bounds.left) / bounds.width) * svgWidth,
                ),
              );
              const end =
                chart.start +
                ((x - 82) / plotWidth) * (chart.end - chart.start);
              onRange?.({
                from: Math.round(Math.min(start.time, end)),
                to: Math.round(Math.max(start.time, end)),
                timezone: zone,
              });
            }
            selectionStart.current = null;
            setSelection(null);
            if (event.currentTarget.hasPointerCapture(event.pointerId))
              event.currentTarget.releasePointerCapture(event.pointerId);
          }}
          onPointerCancel={() => {
            selectionStart.current = null;
            setSelection(null);
          }}
          className="chart-svg"
          ref={svgRef}
          viewBox={`0 0 ${svgWidth} 225`}
          preserveAspectRatio="none"
          role="img"
          aria-label={`${panel.metric} ${panel.aggregation} time series`}
          onMouseLeave={() => setHover(null)}
          onMouseMove={(e) => {
            const rect = e.currentTarget.getBoundingClientRect();
            const x = ((e.clientX - rect.left) / rect.width) * svgWidth;
            setHover(
              x >= 82 && x <= right
                ? chart.start +
                    ((x - 82) / plotWidth) * (chart.end - chart.start)
                : null,
            );
          }}
        >
          <defs>
            <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity=".2" />
              <stop offset="100%" stopColor={color} stopOpacity=".02" />
            </linearGradient>
          </defs>
          {Array.from({ length: 5 }, (_, i) => {
            const y = 30 + (i * 155) / 4;
            return (
              <g key={i}>
                <line x1="82" x2={right} y1={y} y2={y} className="grid-line" />
                <text x="71" y={y + 4} textAnchor="end">
                  {formatter(
                    chart.max - ((chart.max - chart.min) * i) / 4,
                    panel.unit,
                  )}
                </text>
              </g>
            );
          })}
          {Array.from({ length: ticks }, (_, i) => {
            const x = 82 + (i * plotWidth) / (ticks - 1);
            return (
              <g key={i}>
                <line
                  x1={x}
                  x2={x}
                  y1="30"
                  y2="185"
                  className="grid-line vertical"
                />
                <text x={x} y="209" textAnchor="middle">
                  {timeLabel(
                    chart.start + ((chart.end - chart.start) * i) / (ticks - 1),
                  )}
                </text>
              </g>
            );
          })}
          {visibleSeries.map((s, i) => {
            const coordinates = s.points
              .map((p) => `${chart.x(p.timestamp)},${chart.y(p.value)}`)
              .join(" ");
            return (
              <g key={JSON.stringify(s.labels)}>
                {i === 0 && !s.comparison && s.points.length > 1 && (
                  <polygon
                    points={`${chart.x(s.points[0].timestamp)},185 ${coordinates} ${chart.x(s.points.at(-1)!.timestamp)},185`}
                    fill={`url(#${gradient})`}
                  />
                )}
                <polyline
                  points={coordinates}
                  fill="none"
                  stroke={i === 0 ? color : palette[i]}
                  strokeWidth="2"
                  strokeDasharray={s.comparison ? "1 5 4 5" : undefined}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                />
                {s.points.length === 1 && (
                  <circle
                    cx={chart.x(s.points[0].timestamp)}
                    cy={chart.y(s.points[0].value)}
                    r="3"
                    fill={i === 0 ? color : palette[i]}
                  />
                )}
              </g>
            );
          })}
          {events
            .filter(
              (event) =>
                event.time <= chart.end && event.timeEnd >= chart.start,
            )
            .map((event) => {
              const x = chart.x(Math.max(event.time, chart.start)),
                end = chart.x(Math.min(event.timeEnd, chart.end));
              return (
                <g
                  key={event.id}
                  className="annotation-marker"
                  data-annotation-id={event.id}
                  data-alert-state={event.newState?.split(" (")[0]}
                  role="img"
                  aria-label={`${tr("Annotation")}: ${event.text}`}
                >
                  <title>{`${event.text}${event.newState ? `\n${tr("Alert state")}: ${annotationStateLabel(event.prevState || "Normal", tr)} → ${annotationStateLabel(event.newState, tr)}` : ""}\n${event.tags.join(", ")}\n${timeLabel(event.time)}`}</title>
                  {event.timeEnd > event.time && (
                    <rect
                      x={x}
                      y={30}
                      width={Math.max(1, end - x)}
                      height={155}
                      fill={event.color || "#5ac8de"}
                      opacity={0.12}
                    />
                  )}
                  <line
                    x1={x}
                    x2={x}
                    y1={30}
                    y2={185}
                    stroke={event.color || "#5ac8de"}
                    strokeDasharray="3 3"
                  />
                  <circle
                    cx={x}
                    cy={25}
                    r={4}
                    fill={event.color || "#5ac8de"}
                  />
                </g>
              );
            })}
          {hover !== null && (
            <line
              x1={chart.x(hover)}
              x2={chart.x(hover)}
              y1="30"
              y2="185"
              stroke="#b5bed0"
              strokeDasharray="3 3"
            />
          )}
          {selection && (
            <rect
              x={chart.x(Math.min(selection.start, selection.end))}
              y="30"
              width={Math.abs(
                chart.x(selection.end) - chart.x(selection.start),
              )}
              height="155"
              fill="#85aaff33"
              stroke="#85aaff"
              pointerEvents="none"
            />
          )}
        </svg>
        {(!data || !visibleSeries.length || error) && (
          <div className="chart-empty" role={error ? "alert" : undefined}>
            {error ||
              (data
                ? tr(
                    panel.aggregation === "rate"
                      ? "Waiting for two counter samples"
                      : "No samples in this time range",
                  )
                : tr("Loading metrics…"))}
          </div>
        )}
        {hover !== null && visibleSeries.length > 0 && (
          <div className="chart-tooltip">
            <span>{timeLabel(hover)}</span>
            {visibleSeries.map((s, i) => {
              const point = s.points.reduce((a, b) =>
                Math.abs(b.timestamp - hover) < Math.abs(a.timestamp - hover)
                  ? b
                  : a,
              );
              return (
                <strong key={i}>
                  {formatter(point.value, panel.unit)}{" "}
                  <small>{Object.values(s.labels).join(" · ")}</small>
                </strong>
              );
            })}
          </div>
        )}
      </div>
      <div className="chart-legend">
        <i style={{ background: color }} />
        <span className="mono">{panel.expr || panel.metric}</span>
        <span className="legend-mode">
          {panel.expr
            ? "PromQL"
            : panel.aggregation === "rate"
              ? "rate /s"
              : tr(panel.aggregation)}
        </span>
      </div>
      {visibleSeries.length > 1 && (
        <div className="series-legend">
          {visibleSeries.map((s, i) => (
            <span key={i}>
              <i style={{ background: i === 0 ? color : palette[i] }} />
              {Object.entries(s.labels)
                .map(([k, v]) => `${k}=${v}`)
                .join(", ") || "aggregate"}
            </span>
          ))}
          {(data?.series.length || 0) > 8 && (
            <span>
              {tr("Showing 8 of")}
              {data?.series.length}
              {tr("series; use label filters in Explore.")}
            </span>
          )}
        </div>
      )}
    </section>
  );
}
