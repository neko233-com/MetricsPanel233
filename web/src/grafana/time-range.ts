import {
  dateMath,
  dateTime,
  dateTimeForTimeZone,
  ISO_8601,
  type TimeRange,
} from "@grafana/data";

export type TimeSelection =
  | string
  | { from: string | number; to: string | number; timezone?: string };
export const MAX_RANGE_MS = 31 * 86400000;
export function rawSelection(value: TimeSelection) {
  return typeof value === "string"
    ? { from: "now-" + value, to: "now", timezone: "browser" }
    : { ...value, timezone: value.timezone || "browser" };
}
export function resolveTimeRange(value: TimeSelection, now = Date.now()) {
  const raw = rawSelection(value);
  const timezone = raw.timezone === "UTC" ? "utc" : raw.timezone;
  if (timezone !== "browser" && timezone !== "utc") {
    try {
      new Intl.DateTimeFormat("en", { timeZone: timezone });
    } catch {
      throw new Error("Invalid time zone");
    }
  }
  const parse = (bound: string | number, roundUp: boolean) => {
    if (typeof bound === "number" || /^\d+$/.test(bound))
      return dateTimeForTimeZone(timezone, Number(bound));
    if (bound.startsWith("now"))
      return dateMath.toDateTime(bound, { now, roundUp, timezone });
    const [base, math] = bound.split("||");
    if (bound.split("||").length > 2) throw new Error("Invalid time range");
    const absolute = dateTimeForTimeZone(timezone, base, ISO_8601);
    return math ? dateMath.parseDateMath(math, absolute, roundUp) : absolute;
  };
  const from = parse(raw.from, false),
    to = parse(raw.to, true);
  if (
    !from?.isValid() ||
    !to?.isValid() ||
    !Number.isSafeInteger(from.valueOf()) ||
    !Number.isSafeInteger(to.valueOf())
  )
    throw new Error("Invalid time range");
  const start = from.valueOf(),
    end = to.valueOf();
  if (start < 0 || end <= start || end - start > MAX_RANGE_MS)
    throw new Error("Time range must be positive and at most 31 days");
  const sdk: TimeRange = {
    from,
    to,
    raw: {
      from: typeof raw.from === "number" ? dateTime(from) : raw.from,
      to: typeof raw.to === "number" ? dateTime(to) : raw.to,
    },
  };
  return { start, end, timezone, sdk };
}
export function rangeFromURL(
  params = new URLSearchParams(location.search),
  fallback: TimeSelection = "30m",
): TimeSelection | undefined {
  const defaults = rawSelection(fallback);
  const zone = params.get("timezone") || defaults.timezone;
  let value: TimeSelection | undefined;
  if (params.has("from") || params.has("to")) {
    value = {
      from: params.get("from") || defaults.from,
      to: params.get("to") || defaults.to,
      timezone: zone,
    };
  } else if (params.has("time") || params.has("time.window")) {
    const center = Number(params.get("time")),
      width = Number(params.get("time.window"));
    if (
      !params.has("time") ||
      !params.has("time.window") ||
      !Number.isSafeInteger(center) ||
      !Number.isSafeInteger(width) ||
      width <= 0
    )
      throw new Error("Invalid time window");
    value = {
      from: center - Math.ceil(width / 2),
      to: center + Math.floor(width / 2),
      timezone: zone,
    };
  } else if (params.has("timezone")) {
    value = { ...defaults, timezone: zone };
  }
  if (value) resolveTimeRange(value);
  return value;
}
export function writeRangeURL(value: TimeSelection) {
  const raw = rawSelection(value),
    params = new URLSearchParams(location.search);
  params.set("from", String(raw.from));
  params.set("to", String(raw.to));
  params.set("timezone", raw.timezone);
  params.delete("time");
  params.delete("time.window");
  history.replaceState(
    history.state,
    "",
    `${location.pathname}?${params}${location.hash}`,
  );
}
export function rangeFromDashboard(
  grafana: unknown,
): TimeSelection | undefined {
  const original = grafana as
    | {
        dashboard?: unknown;
        spec?: unknown;
        time?: { from?: string; to?: string };
        timezone?: string;
        timeSettings?: { from?: string; to?: string; timezone?: string };
      }
    | undefined;
  if (!original) return;
  if (original.dashboard) return rangeFromDashboard(original.dashboard);
  if (original.spec) return rangeFromDashboard(original.spec);
  const settings = original.timeSettings || original.time;
  const value = {
    from: settings?.from || "now-6h",
    to: settings?.to || "now",
    timezone: original.timeSettings?.timezone || original.timezone || "browser",
  };
  resolveTimeRange(value);
  return value;
}
export function rangeLabel(value: TimeSelection) {
  if (typeof value === "string") return value;
  return `${value.from} → ${value.to} (${value.timezone || "browser"})`;
}
