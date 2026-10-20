import { useEffect, useMemo, useRef, useState } from "react";
import { Pencil, Search, Trash2 } from "lucide-react";
import { t } from "../../i18n";
import { resolveTimeRange } from "../../grafana/time-range";
import type { RendererProps } from "../types";
import "./LogsRenderer.css";

const ROW_HEIGHT = 26;
const OVERSCAN = 6;

type LogRow = {
  key: string;
  time: number;
  line: string;
  labels: Record<string, string>;
};

function logRows(frames: RendererProps["frames"]): LogRow[] {
  const rows: LogRow[] = [];
  frames.forEach((frame, frameIndex) => {
    const time = frame.fields.find((field) => field.type === "time");
    const preferred = frame.fields.filter(
      (field) =>
        field.type === "string" &&
        /^(line|body|message|log)$/i.test(field.name || ""),
    );
    const fields = preferred.length
      ? preferred
      : frame.fields.filter((field) => field.type === "string");
    for (const field of fields) {
      for (let index = 0; index < field.values.length; index += 1) {
        const raw = field.values[index];
        if (raw == null) continue;
        const rawTime = time?.values[index];
        const at =
          rawTime instanceof Date ? rawTime.getTime() : Number(rawTime);
        const labels = Object.fromEntries(
          Object.entries(
            (field.labels || {}) as Record<string, string>,
          ).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
        );
        rows.push({
          key: `${frame.refId || frameIndex}:${field.name || "line"}:${index}`,
          time: Number.isFinite(at) ? at : 0,
          line: String(raw),
          labels,
        });
      }
    }
  });
  return rows.sort((left, right) => right.time - left.time);
}

export default function LogsRenderer({
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
  const rows = useMemo(() => logRows(frames), [frames]);
  const [query, setQuery] = useState("");
  const [labelKey, setLabelKey] = useState("");
  const [labelValue, setLabelValue] = useState("");
  const [scrollTop, setScrollTop] = useState(0);
  const [height, setHeight] = useState(0);
  const viewport = useRef<HTMLDivElement>(null);

  const labelIndex = useMemo(() => {
    const index = new Map<string, Set<string>>();
    for (const row of rows) {
      for (const [key, value] of Object.entries(row.labels)) {
        const values = index.get(key) || new Set<string>();
        values.add(value);
        index.set(key, values);
      }
    }
    return index;
  }, [rows]);
  const labelKeys = useMemo(
    () => [...labelIndex.keys()].sort((left, right) => left.localeCompare(right)),
    [labelIndex],
  );
  const labelValues = useMemo(
    () =>
      labelKey
        ? [...(labelIndex.get(labelKey) || [])].sort((left, right) =>
            left.localeCompare(right),
          )
        : [],
    [labelIndex, labelKey],
  );

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return rows.filter((row) => {
      if (labelKey && row.labels[labelKey] !== labelValue) return false;
      if (!needle) return true;
      const haystack =
        row.line +
        " " +
        Object.entries(row.labels)
          .map(([key, value]) => `${key}=${value}`)
          .join(" ");
      return haystack.toLowerCase().includes(needle);
    });
  }, [labelKey, labelValue, query, rows]);

  useEffect(() => {
    const element = viewport.current;
    if (!element) return;
    const measure = () => setHeight(element.clientHeight);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    viewport.current?.scrollTo({ top: 0 });
    setScrollTop(0);
  }, [query, labelKey, labelValue]);

  const start = Math.max(0, Math.floor(scrollTop / ROW_HEIGHT) - OVERSCAN);
  const visible = Math.max(1, Math.ceil(height / ROW_HEIGHT) + OVERSCAN * 2);
  const slice = filtered.slice(start, start + visible);
  const timeZone =
    resolved.timezone === "browser"
      ? undefined
      : resolved.timezone === "utc"
        ? "UTC"
        : resolved.timezone;

  return (
    <section
      className="chart-panel native-logs-panel"
      data-panel-id={panel.id}
      data-log-count={filtered.length}
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
      <div className="native-logs-toolbar">
        <label className="native-logs-search">
          <Search size={14} aria-hidden="true" />
          <input
            type="search"
            aria-label={t("Search logs")}
            placeholder={t("Search logs")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <label>
          <span>{t("Label")}</span>
          <select
            aria-label={t("Label key")}
            value={labelKey}
            onChange={(event) => {
              setLabelKey(event.target.value);
              setLabelValue("");
            }}
          >
            <option value="">{t("All labels")}</option>
            {labelKeys.map((key) => (
              <option key={key} value={key}>
                {key}
              </option>
            ))}
          </select>
        </label>
        <label>
          <span>{t("Value")}</span>
          <select
            aria-label={t("Label value")}
            value={labelValue}
            disabled={!labelKey}
            onChange={(event) => setLabelValue(event.target.value)}
          >
            <option value="">{t("Any value")}</option>
            {labelValues.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
        <span className="native-logs-count" role="status">
          {filtered.length} / {rows.length}
        </span>
      </div>
      <div
        className="native-logs-viewport"
        ref={viewport}
        role="log"
        aria-label={t("Log lines")}
        onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
      >
        {filtered.length ? (
          <div
            className="native-logs-spacer"
            style={{ height: filtered.length * ROW_HEIGHT }}
          >
            {slice.map((row, index) => (
              <div
                className="native-logs-row"
                key={row.key}
                style={{ top: (start + index) * ROW_HEIGHT }}
              >
                <time>
                  {row.time
                    ? new Date(row.time).toLocaleTimeString(undefined, {
                        timeZone,
                        hour12: false,
                      })
                    : ""}
                </time>
                <span className="native-logs-labels">
                  {Object.entries(row.labels).map(([key, value]) => (
                    <span className="native-logs-label" key={key}>
                      <b>{key}</b>={value}
                    </span>
                  ))}
                </span>
                <span className="native-logs-line">{row.line}</span>
              </div>
            ))}
          </div>
        ) : (
          <p className="native-logs-empty" role="status">
            {loading
              ? t("Loading logs…")
              : rows.length
                ? t("No logs match the current filters")
                : t("No logs in this time range")}
          </p>
        )}
      </div>
    </section>
  );
}
