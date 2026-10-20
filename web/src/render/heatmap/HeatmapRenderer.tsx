import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
} from "react";
import { Pencil, Trash2 } from "lucide-react";
import { t } from "../../i18n";
import { resolveTimeRange } from "../../grafana/time-range";
import type { RendererProps } from "../types";
import "./HeatmapRenderer.css";

type HeatmapModel = {
  times: number[];
  labels: string[];
  values: number[][];
  max: number;
};

type Hover = {
  time: number;
  label: string;
  value: number;
  left: number;
  top: number;
};

const PADDING = { left: 86, right: 10, top: 10, bottom: 28 };

function heatmapModel(frames: RendererProps["frames"]): HeatmapModel | null {
  const frame = frames.find((candidate) =>
    candidate.fields.some((field) => field.type === "time"),
  );
  if (!frame) return null;
  const time = frame.fields.find((field) => field.type === "time");
  const rows = frame.fields.filter((field) => field.type === "number");
  if (!time || !rows.length) return null;
  const times = Array.from(time.values, (value) =>
    value instanceof Date ? value.getTime() : Number(value),
  );
  const labels = rows.map(
    (field) =>
      field.labels?.le ||
      field.labels?.bucket ||
      field.labels?.upper_bound ||
      field.name ||
      "",
  );
  const values = rows.map((field) =>
    Array.from(field.values, (value) => {
      const number = Number(value);
      return Number.isFinite(number) ? number : 0;
    }),
  );
  let max = 0;
  values.forEach((row) => row.forEach((value) => (max = Math.max(max, value))));
  return { times, labels, values, max };
}

function color(value: number, max: number) {
  const ratio = max > 0 ? Math.min(1, Math.max(0, value / max)) : 0;
  const hue = 220 - 190 * ratio;
  const light = 24 + 42 * ratio;
  return `hsl(${hue} 82% ${light}%)`;
}

export default function HeatmapRenderer({
  panel,
  frames,
  range,
  timeInfo,
  loading,
  streaming,
  queryError,
  onEdit,
  onRemove,
}: RendererProps) {
  const config = panel.config;
  const resolved = resolveTimeRange(range);
  const model = useMemo(() => heatmapModel(frames), [frames]);
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hover, setHover] = useState<Hover>();
  const timeZone =
    resolved.timezone === "browser"
      ? undefined
      : resolved.timezone === "utc"
        ? "UTC"
        : resolved.timezone;

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

  useLayoutEffect(() => {
    const element = canvas.current;
    const elementHost = host.current;
    if (!element || !elementHost || !model || size.width <= 0 || size.height <= 0)
      return;
    const ratio = window.devicePixelRatio || 1;
    element.width = Math.max(1, Math.floor(size.width * ratio));
    element.height = Math.max(1, Math.floor(size.height * ratio));
    element.style.width = `${size.width}px`;
    element.style.height = `${size.height}px`;
    const context = element.getContext("2d");
    if (!context) return;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, size.width, size.height);
    const plotWidth = Math.max(1, size.width - PADDING.left - PADDING.right);
    const plotHeight = Math.max(1, size.height - PADDING.top - PADDING.bottom);
    const rows = model.values.length;
    const columns = Math.max(1, model.times.length - 1);
    const cellWidth = plotWidth / columns;
    const cellHeight = plotHeight / Math.max(1, rows);
    for (let row = 0; row < rows; row += 1) {
      for (let column = 0; column < columns; column += 1) {
        const value = model.values[row]?.[column] ?? 0;
        context.fillStyle = color(value, model.max);
        context.fillRect(
          PADDING.left + column * cellWidth,
          PADDING.top + row * cellHeight,
          Math.ceil(cellWidth) + 0.5,
          Math.ceil(cellHeight) + 0.5,
        );
      }
    }
    context.fillStyle = "rgba(180, 190, 205, 0.75)";
    context.font = "11px system-ui, sans-serif";
    context.textAlign = "right";
    context.textBaseline = "middle";
    for (let row = 0; row < rows; row += 1) {
      const label = model.labels[row];
      if (label)
        context.fillText(
          label.length > 12 ? label.slice(0, 12) + "…" : label,
          PADDING.left - 8,
          PADDING.top + (row + 0.5) * cellHeight,
        );
    }
    context.textAlign = "center";
    context.textBaseline = "top";
    const tickCount = Math.min(6, Math.max(2, model.times.length));
    const tickStep = Math.max(1, Math.floor(columns / tickCount));
    const timeFormat = new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZone,
    });
    for (let column = 0; column < columns; column += tickStep) {
      context.fillText(
        timeFormat.format(new Date(model.times[column] ?? 0)),
        PADDING.left + (column + 0.5) * cellWidth,
        PADDING.top + plotHeight + 7,
      );
    }
    context.strokeStyle = "rgba(150, 165, 190, 0.22)";
    context.strokeRect(
      PADDING.left,
      PADDING.top,
      plotWidth,
      plotHeight,
    );
  }, [model, size.width, size.height, timeZone]);

  const onMove = (event: MouseEvent<HTMLCanvasElement>) => {
    if (!model || size.width <= 0 || size.height <= 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const x = event.clientX - rect.left;
    const y = event.clientY - rect.top;
    const plotWidth = Math.max(1, size.width - PADDING.left - PADDING.right);
    const plotHeight = Math.max(1, size.height - PADDING.top - PADDING.bottom);
    const column = Math.floor(((x - PADDING.left) / plotWidth) * Math.max(1, model.times.length - 1));
    const row = Math.floor(((y - PADDING.top) / plotHeight) * model.values.length);
    if (
      x < PADDING.left ||
      y < PADDING.top ||
      column < 0 ||
      row < 0 ||
      column >= model.times.length - 1 ||
      row >= model.values.length
    ) {
      setHover(undefined);
      return;
    }
    const value = model.values[row]?.[column] ?? 0;
    setHover({
      time: model.times[column] ?? 0,
      label: model.labels[row],
      value,
      left: Math.min(size.width - 190, Math.max(8, x + 12)),
      top: Math.min(size.height - 80, Math.max(8, y + 12)),
    });
  };

  return (
    <section
      className="chart-panel native-heatmap-panel"
      data-cell-count={model ? model.times.length * model.values.length : 0}
      aria-label={t(panel.title)}
    >
      <div className="panel-heading">
        <h2 title={config?.description}>{t(panel.title)}</h2>
        <div className="panel-actions">
          {streaming ? (
            <span className="status healthy" role="status">
              <i />
              {t("Live")}
            </span>
          ) : null}
          {onEdit ? (
            <button
              className="icon-button"
              aria-label={`${t("Edit")} ${t(panel.title)}`}
              onClick={onEdit}
            >
              <Pencil size={15} />
            </button>
          ) : null}
          {onRemove ? (
            <button
              className="icon-button danger-hover"
              aria-label={`${t("Remove")} ${t(panel.title)}`}
              onClick={onRemove}
            >
              <Trash2 size={15} />
            </button>
          ) : null}
        </div>
      </div>
      {timeInfo ? <p className="panel-time-info">{timeInfo}</p> : null}
      {queryError ? (
        <p className="form-error" role="alert">
          {t(queryError)}
        </p>
      ) : null}
      <div className="native-heatmap-viewport">
        <div className="native-heatmap-host" ref={host}>
          <canvas
            ref={canvas}
            role="img"
            aria-label={t(panel.title)}
            onMouseMove={onMove}
            onMouseLeave={() => setHover(undefined)}
          />
        </div>
        {model && model.max > 0 ? (
          <div className="native-heatmap-legend" aria-hidden="true">
            <span>0</span>
            <i />
            <span>{model.max}</span>
          </div>
        ) : null}
        {hover ? (
          <div
            className="native-heatmap-tooltip"
            role="status"
            style={{ left: hover.left, top: hover.top }}
          >
            <time>
              {new Date(hover.time).toLocaleString(undefined, {
                timeZone,
                hour12: false,
              })}
            </time>
            <span>{hover.label}</span>
            <strong>{hover.value}</strong>
          </div>
        ) : null}
        {!model ? (
          <p className="native-heatmap-empty" role="status">
            {loading ? t("Loading metrics…") : t("No samples in this time range")}
          </p>
        ) : null}
      </div>
    </section>
  );
}
