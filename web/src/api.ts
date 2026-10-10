import { getLocale, t } from "./i18n";
import { resolveTimeRange, type TimeSelection } from "./grafana/time-range";
export type Labels = Record<string, string>;
export type Metric = { name: string; series_count: number };
export type Point = { timestamp: number; value: number };
export type QueryResult = {
  metric: string;
  aggregation: string;
  start: number;
  end: number;
  step: number;
  series: { labels: Labels; points: Point[]; comparison?: boolean }[];
};
export type Panel = {
  id: string;
  title: string;
  metric: string;
  aggregation: string;
  unit: string;
  labels?: Labels;
  expr?: string;
  expressions?: string[];
  visualization?: string;
  config?: import("./grafana/engine").GrafanaConfig;
};
export type Variable = {
  name: string;
  type: string;
  query: string;
  current: string;
  options: string[];
  multi: boolean;
  include_all: boolean;
  config?: {
    label?: string;
    hide?: number | string;
    regex?: string;
    sort?: number | string;
    allValue?: string;
    options?: { text: string; value: string }[];
  };
};
export type Dashboard = {
  id: string;
  name: string;
  panels: Panel[];
  updated_at: number;
  variables?: Variable[];
  annotations?: Record<string, any>[];
  grafana?: unknown;
};
export function dashboardUID(dashboard: Dashboard): string {
  const original = dashboard.grafana as
    | {
        uid?: string;
        metadata?: { name?: string };
        spec?: { uid?: string };
        dashboard?: { uid?: string };
      }
    | undefined;
  return (
    original?.uid ||
    original?.dashboard?.uid ||
    original?.spec?.uid ||
    original?.metadata?.name ||
    dashboard.id
  );
}
export type Target = {
	 kind?: string;
	 username?: string;
	 database?: string;
	 tls_mode?: string;
	 secure_fields?: Record<string, boolean>;
	 secure_settings?: Record<string,string>;
  id: number;
  name: string;
  url: string;
  interval_seconds: number;
  labels: Labels;
  enabled: boolean;
  last_scrape: number;
  last_error: string;
  samples: number;
  duration_ms: number;
};
export type Stats = {
  series: number;
  samples: number;
  ingest_rate: number;
  storage_bytes: number;
  retention_days: number;
  collectors_online: number;
  collectors_total: number;
  started_at: number;
  last_sample: number;
};
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const token = sessionStorage.getItem("metricspanel-token") || "";
  const response = await fetch(
    path.startsWith("/prometheus/") || path.startsWith("/api/")
      ? path
      : `/api/v1${path}`,
    {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...options.headers,
      },
    },
  );
  const result = await response.json();
  if (!response.ok)
    throw new ApiError(
      (typeof result.error === "string"
        ? result.error
        : result.error?.message) || `HTTP ${response.status}`,
      response.status,
    );
  return result as T;
}
export const jsonBody = (data: unknown) => JSON.stringify(data);
export const message = (error: unknown) =>
  error instanceof Error ? error.message : String(error);
export function formatValue(value: number, unit = ""): string {
  if (unit === "bytes") {
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    const index = Math.max(
      0,
      Math.min(4, Math.floor(Math.log(Math.max(value, 1)) / Math.log(1024))),
    );
    return `${(value / 1024 ** index).toLocaleString("en", { maximumFractionDigits: 1 })} ${units[index]}`;
  }
  if (unit === "seconds") return `${value.toFixed(1)} s`;
  if (unit === "percent") return `${value.toFixed(1)}%`;
  const text = value.toLocaleString("en", {
    maximumFractionDigits: Math.abs(value) < 10 ? 2 : 1,
    notation: Math.abs(value) >= 10000 ? "compact" : "standard",
  });
  return unit === "ops" ? `${text} /s` : text;
}
export const timeAgo = (timestamp: number) =>
  !timestamp
    ? t("Pending")
    : getLocale() === "zh"
      ? `${Math.max(0, Math.round((Date.now() - timestamp) / 1000))}秒前`
      : `${Math.max(0, Math.round((Date.now() - timestamp) / 1000))}s ago`;
export const duration = (ms: number) => {
  const s = Math.max(0, Math.floor(ms / 1000));
  return s > 3600
    ? `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
    : s > 60
      ? `${Math.floor(s / 60)}m ${s % 60}s`
      : `${s}s`;
};
export const ranges = [
  { label: "Last 15 minutes", value: "15m" },
  { label: "Last 30 minutes", value: "30m" },
  { label: "Last hour", value: "1h" },
  { label: "Last 6 hours", value: "6h" },
  { label: "Last 24 hours", value: "24h" },
  { label: "Last 7 days", value: "168h" },
];
export const aggregations = ["last", "avg", "sum", "min", "max", "rate"];
export function parseLabels(text: string): Labels {
  const labels = JSON.parse(text) as unknown;
  if (
    labels === null ||
    typeof labels !== "object" ||
    Array.isArray(labels) ||
    Object.values(labels).some((v) => typeof v !== "string")
  )
    throw new Error("Labels must be a JSON object with string values");
  return labels as Labels;
}
export function download(name: string, data: unknown) {
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  URL.revokeObjectURL(url);
}

export function rangeMilliseconds(range: TimeSelection): number {
  const { start, end } = resolveTimeRange(range);
  return end - start;
}
export function interpolate(
  expr: string,
  variables: InterpolationValues,
  range: TimeSelection,
): string {
  const { start, end } = resolveTimeRange(range);
  const milliseconds = end - start,
    seconds = milliseconds / 1000;
  const interval = Math.max(5, Math.ceil(seconds / 120)),
    rateInterval = Math.max(60, interval * 4);
  const values = {
    ...variables,
    __interval: `${interval}s`,
    __interval_ms: String(interval * 1000),
    __rate_interval: `${rateInterval}s`,
    __range: Number.isInteger(seconds) ? `${seconds}s` : `${milliseconds}ms`,
    __range_s: String(seconds),
    __range_ms: String(milliseconds),
    __from: String(start),
    __to: String(end),
  };
  return expr.replace(
    /\$\{([a-zA-Z_][\w]*)(?::([\w]+))?\}|\$([a-zA-Z_][\w]*)|\[\[([a-zA-Z_][\w]*)(?::([\w]+))?\]\]/g,
    (
      match,
      braced: string,
      format: string,
      plain: string,
      legacy: string,
      legacyFormat: string,
      offset: number,
    ) => {
      const value = (values as InterpolationValues)[braced || plain || legacy];
      if (value === undefined) return match;
      let quoted = false,
        escapedChar = false;
      for (const char of expr.slice(0, offset)) {
        if (escapedChar) {
          escapedChar = false;
          continue;
        }
        if (char === "\\") {
          escapedChar = true;
          continue;
        }
        if (char === '"') quoted = !quoted;
      }
      if (typeof value === "object" && !Array.isArray(value))
        return quoted ? value.raw.replace(/["\\]/g, "\\$&") : value.raw;
      const parts = Array.isArray(value) ? value : [value];
      const mode = format || legacyFormat;
      if (mode === "json") return JSON.stringify(value);
      if (mode === "csv") return parts.join(",");
      if (mode === "raw" || mode === "pipe") return parts.join("|");
      if (mode === "doublequote")
        return parts.map((p) => JSON.stringify(p)).join(",");
      if (mode === "singlequote")
        return parts.map((p) => `'${p.replace(/['\\]/g, "\\$&")}'`).join(",");
      if (mode === "percentencode") return encodeURIComponent(parts.join(","));
      const escaped = parts.map((p) =>
        p === ".*" ? p : p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
      );
      if (mode === "regex" || Array.isArray(value)) {
        const regex =
          escaped.length > 1 ? `(${escaped.join("|")})` : escaped[0];
        return quoted ? regex.replace(/["\\]/g, "\\$&") : regex;
      }
      // Single Prometheus values only require quote/backslash escaping.
      return String(value).replace(/["\\]/g, "\\$&");
    },
  );
}
export type InterpolationValues = Record<
  string,
  string | string[] | { raw: string }
>;
export async function queryPanel(
  panel: Panel,
  range: TimeSelection,
  signal: AbortSignal,
): Promise<QueryResult> {
  const expressions = panel.expressions?.length
    ? panel.expressions
    : panel.expr
      ? [panel.expr]
      : [];
  const { start, end } = resolveTimeRange(range);
  if (!expressions.length)
    return api<QueryResult>(
      `/query?${new URLSearchParams({ metric: panel.metric, start: String(start), end: String(end), aggregation: panel.aggregation, labels: JSON.stringify(panel.labels || {}) })}`,
      { signal },
    );
  const step = Math.max(1, Math.ceil((end - start) / 120000));
  const results = await Promise.all(
    expressions.map((expr) =>
      api<{
        data: { result: { metric: Labels; values: [number, string][] }[] };
      }>(
        `/prometheus/api/v1/query_range?${new URLSearchParams({ query: expr, start: String(start / 1000), end: String(end / 1000), step: String(step) })}`,
        { signal },
      ),
    ),
  );
  const series = results
    .flatMap((r, i) =>
      r.data.result.map((s) => ({
        labels: {
          ...s.metric,
          ...(expressions.length > 1 ? { query: String(i + 1) } : {}),
        },
        points: s.values
          .filter((p) => Number.isFinite(Number(p[1])))
          .map((p) => ({ timestamp: p[0] * 1000, value: Number(p[1]) })),
      })),
    )
    .filter((s) => s.points.length > 0);
  return {
    metric: expressions[0],
    aggregation: "PromQL",
    start,
    end,
    step: step * 1000,
    series,
  };
}
