import { useMemo, useState } from "react";
import { ArrowDown, ArrowUp, Pencil, Search, Trash2 } from "lucide-react";
import { t } from "../../i18n";
import { resolveTimeRange } from "../../grafana/time-range";
import type { RendererProps } from "../types";
import "./TracesRenderer.css";

type TraceRow = {
  id: string;
  service: string;
  name: string;
  start: number;
  duration: number;
  status: string;
};

function fieldByName(frame: RendererProps["frames"][number], names: string[]) {
  return frame.fields.find((field) =>
    names.some((name) => (field.name || "").toLowerCase() === name.toLowerCase()),
  );
}

function traceRows(frames: RendererProps["frames"]): TraceRow[] {
  const rows: TraceRow[] = [];
  for (const frame of frames) {
    const ids = fieldByName(frame, ["traceID", "traceId", "id"]);
    const services = fieldByName(frame, ["rootServiceName", "service", "serviceName"]);
    const names = fieldByName(frame, ["rootTraceName", "name", "traceName", "operation"]);
    const starts = fieldByName(frame, ["startTime", "time", "start"]);
    const durations = fieldByName(frame, ["durationMs", "duration", "durationMillis"]);
    const statuses = fieldByName(frame, ["status", "error", "statusCode"]);
    if (!ids) continue;
    for (let index = 0; index < ids.values.length; index += 1) {
      const id = ids.values[index];
      if (id == null) continue;
      const rawStart = starts?.values[index];
      const start = rawStart instanceof Date ? rawStart.getTime() : Number(rawStart);
      rows.push({
        id: String(id),
        service: String(services?.values[index] ?? ""),
        name: String(names?.values[index] ?? ""),
        start: Number.isFinite(start) ? start : 0,
        duration: Number(durations?.values[index] ?? 0),
        status: String(statuses?.values[index] ?? ""),
      });
    }
  }
  return rows;
}

function durationText(value: number) {
  if (!Number.isFinite(value)) return "—";
  if (value >= 1000) return `${(value / 1000).toFixed(2)} s`;
  return `${value.toFixed(value < 10 ? 2 : 0)} ms`;
}

export default function TracesRenderer({
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
  const rows = useMemo(() => traceRows(frames), [frames]);
  const hasStatus = useMemo(() => rows.some((row) => row.status), [rows]);
  const [query, setQuery] = useState(() => {
    try {
      return new URLSearchParams(location.search).get("traceId") || "";
    } catch {
      return "";
    }
  });
  const [sort, setSort] = useState<"duration" | "start">("start");
  const [descending, setDescending] = useState(true);
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const result = needle
      ? rows.filter((row) =>
          [row.id, row.service, row.name, row.status]
            .join(" ")
            .toLowerCase()
            .includes(needle),
        )
      : [...rows];
    return result.sort((left, right) =>
      sort === "duration"
        ? descending
          ? right.duration - left.duration
          : left.duration - right.duration
        : descending
          ? right.start - left.start
          : left.start - right.start,
    );
  }, [descending, query, rows, sort]);
  const timeZone =
    resolved.timezone === "browser"
      ? undefined
      : resolved.timezone === "utc"
        ? "UTC"
        : resolved.timezone;
  const traceLink = (id: string) => {
    try {
      const params = new URLSearchParams(location.search);
      params.set("traceId", id);
      return `${location.pathname}?${params}${location.hash}`;
    } catch {
      return `?traceId=${encodeURIComponent(id)}`;
    }
  };
  const sortBy = (next: "duration" | "start") => {
    if (sort === next) setDescending((current) => !current);
    else {
      setSort(next);
      setDescending(true);
    }
  };

  return (
    <section
      className="chart-panel native-traces-panel"
      data-trace-count={filtered.length}
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
      <div className="native-traces-toolbar">
        <label className="native-traces-search">
          <Search size={14} aria-hidden="true" />
          <input
            type="search"
            aria-label={t("Search traces")}
            placeholder={t("Search traces")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <span className="native-traces-count" role="status">
          {filtered.length} / {rows.length}
        </span>
      </div>
      <div className="native-traces-scroll">
        {filtered.length ? (
          <table aria-label={t(panel.title)}>
            <thead>
              <tr>
                <th>{t("Trace ID")}</th>
                <th>{t("Service")}</th>
                <th>{t("Trace name")}</th>
                <th>
                  <button
                    type="button"
                    aria-label={t("Sort by start time")}
                    onClick={() => sortBy("start")}
                  >
                    {t("Start time")}
                    {sort === "start" ? (
                      descending ? <ArrowDown size={12} /> : <ArrowUp size={12} />
                    ) : null}
                  </button>
                </th>
                <th>
                  <button
                    type="button"
                    aria-label={t("Sort by duration")}
                    onClick={() => sortBy("duration")}
                  >
                    {t("Duration")}
                    {sort === "duration" ? (
                      descending ? <ArrowDown size={12} /> : <ArrowUp size={12} />
                    ) : null}
                  </button>
                </th>
                {hasStatus ? <th>{t("Status")}</th> : null}
              </tr>
            </thead>
            <tbody>
              {filtered.map((row) => (
                <tr key={row.id} data-trace-id={row.id}>
                  <td>
                    <a href={traceLink(row.id)}>{row.id}</a>
                  </td>
                  <td>{row.service || "—"}</td>
                  <td className="native-traces-name">{row.name || "—"}</td>
                  <td className="mono">
                    {row.start
                      ? new Date(row.start).toLocaleString(undefined, {
                          timeZone,
                          hour12: false,
                        })
                      : "—"}
                  </td>
                  <td className="mono native-traces-duration">
                    {durationText(row.duration)}
                  </td>
                  {hasStatus ? (
                    <td>
                      {row.status ? (
                        <span className="native-traces-status">{row.status}</span>
                      ) : (
                        "—"
                      )}
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="native-traces-empty" role="status">
            {loading
              ? t("Loading traces…")
              : rows.length
                ? t("No traces match the current filters")
                : t("No traces in this time range")}
          </p>
        )}
      </div>
    </section>
  );
}
