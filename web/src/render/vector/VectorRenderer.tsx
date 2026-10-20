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
import type { RendererProps } from "../types";
import "./VectorRenderer.css";

const ROW_HEIGHT = 20;
const PADDING = { left: 94, right: 10, top: 8, bottom: 22 };
const MAX_ROWS = 200;

type VectorRow = { id: string; values: number[] };

type Hover = {
  id: string;
  dimension: number;
  value: number;
  left: number;
  top: number;
};

function vectorRows(frames: RendererProps["frames"]): VectorRow[] {
  const rows: VectorRow[] = [];
  for (const frame of frames) {
    const idField = frame.fields.find((field) => field.type === "string");
    const numeric = frame.fields.filter((field) => field.type === "number");
    const arrays = numeric.find((field) =>
      Array.isArray(field.values[0]),
    );
    if (arrays) {
      for (let index = 0; index < arrays.values.length && rows.length < MAX_ROWS; index += 1) {
        const raw = arrays.values[index];
        if (!Array.isArray(raw)) continue;
        rows.push({
          id: String(idField?.values[index] ?? `Vector ${index + 1}`),
          values: raw.map((value) => {
            const number = Number(value);
            return Number.isFinite(number) ? number : 0;
          }),
        });
      }
      continue;
    }
    if (numeric.length < 8) continue;
    const length = Math.max(...numeric.map((field) => field.values.length));
    for (let index = 0; index < length && rows.length < MAX_ROWS; index += 1) {
      rows.push({
        id: String(idField?.values[index] ?? `Vector ${index + 1}`),
        values: numeric.map((field) => {
          const number = Number(field.values[index]);
          return Number.isFinite(number) ? number : 0;
        }),
      });
    }
  }
  return rows;
}

function color(value: number, min: number, max: number) {
  const ratio = max > min ? (value - min) / (max - min) : 0;
  const hue = 220 - 190 * Math.min(1, Math.max(0, ratio));
  return `hsl(${hue} 82% ${24 + 42 * Math.min(1, Math.max(0, ratio))}%)`;
}

export default function VectorRenderer({
  panel,
  frames,
  timeInfo,
  loading,
  streaming,
  queryError,
  onEdit,
  onRemove,
}: RendererProps) {
  const config = panel.config;
  const rows = useMemo(() => vectorRows(frames), [frames]);
  const dimensions = useMemo(
    () => rows.reduce((size, row) => Math.max(size, row.values.length), 0),
    [rows],
  );
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hover, setHover] = useState<Hover>();

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
    if (!element || size.width <= 0 || !rows.length || dimensions <= 0) return;
    const height = Math.max(size.height, rows.length * ROW_HEIGHT + PADDING.top + PADDING.bottom);
    const ratio = window.devicePixelRatio || 1;
    element.width = Math.max(1, Math.floor(size.width * ratio));
    element.height = Math.max(1, Math.floor(height * ratio));
    element.style.width = `${size.width}px`;
    element.style.height = `${height}px`;
    const context = element.getContext("2d");
    if (!context) return;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, size.width, height);
    const plotWidth = Math.max(1, size.width - PADDING.left - PADDING.right);
    const cellWidth = plotWidth / dimensions;
    context.font = "10px system-ui, sans-serif";
    context.textBaseline = "middle";
    rows.forEach((row, rowIndex) => {
      const values = row.values;
      const min = Math.min(...values);
      const max = Math.max(...values);
      const top = PADDING.top + rowIndex * ROW_HEIGHT;
      context.fillStyle = "rgba(180, 190, 205, 0.72)";
      context.textAlign = "right";
      context.fillText(
        row.id.length > 14 ? row.id.slice(0, 14) + "…" : row.id,
        PADDING.left - 8,
        top + ROW_HEIGHT / 2,
      );
      values.forEach((value, dimension) => {
        context.fillStyle = color(value, min, max);
        context.fillRect(
          PADDING.left + dimension * cellWidth,
          top + 2,
          Math.ceil(cellWidth) + 0.4,
          ROW_HEIGHT - 4,
        );
      });
    });
    context.fillStyle = "rgba(180, 190, 205, 0.7)";
    context.textAlign = "center";
    context.textBaseline = "top";
    const tickStep = Math.max(1, Math.floor(dimensions / 6));
    for (let dimension = 0; dimension < dimensions; dimension += tickStep) {
      context.fillText(
        String(dimension),
        PADDING.left + (dimension + 0.5) * cellWidth,
        PADDING.top + rows.length * ROW_HEIGHT + 5,
      );
    }
  }, [dimensions, rows, size.width, size.height]);

  const onMove = (event: MouseEvent<HTMLCanvasElement>) => {
    if (!rows.length || dimensions <= 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const x = event.clientX - rect.left;
    const y = event.clientY - rect.top;
    const plotWidth = Math.max(1, size.width - PADDING.left - PADDING.right);
    const cellWidth = plotWidth / dimensions;
    const dimension = Math.floor((x - PADDING.left) / cellWidth);
    const row = Math.floor((y - PADDING.top) / ROW_HEIGHT);
    if (
      x < PADDING.left ||
      y < PADDING.top ||
      dimension < 0 ||
      row < 0 ||
      dimension >= dimensions ||
      row >= rows.length
    ) {
      setHover(undefined);
      return;
    }
    setHover({
      id: rows[row].id,
      dimension,
      value: rows[row].values[dimension] ?? 0,
      left: Math.min(size.width - 190, Math.max(8, x + 12)),
      top: Math.min(size.height - 70, Math.max(8, y + 12)),
    });
  };

  return (
    <section
      className="chart-panel native-vector-panel"
      data-vector-count={rows.length}
      data-vector-dimensions={dimensions}
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
      <div className="native-vector-viewport">
        <div className="native-vector-scroll" ref={host}>
          {rows.length ? (
            <canvas
              ref={canvas}
              role="img"
              aria-label={t(panel.title)}
              onMouseMove={onMove}
              onMouseLeave={() => setHover(undefined)}
            />
          ) : (
            <p className="native-vector-empty" role="status">
              {loading
                ? t("Loading metrics…")
                : t("No vector samples in this time range")}
            </p>
          )}
          {hover ? (
            <div
              className="native-vector-tooltip"
              role="status"
              style={{ left: hover.left, top: hover.top }}
            >
              <strong>{hover.id}</strong>
              <span>{t("Dimension")} {hover.dimension}</span>
              <span>{hover.value.toFixed(4)}</span>
            </div>
          ) : null}
        </div>
      </div>
    </section>
  );
}
