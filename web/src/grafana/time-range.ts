import {
  dateMath,
  dateTime,
  dateTimeForTimeZone,
  ISO_8601,
  rangeUtil,
  type TimeRange,
} from "@grafana/data";

export type TimeSelection =
  | string
  | {
      from: string | number;
      to: string | number;
      timezone?: string;
      allowInstant?: boolean;
    };
export const MAX_RANGE_MS = 31 * 86400000;
export function rawSelection(value: TimeSelection) {
  return typeof value === "string"
    ? { from: "now-" + value, to: "now", timezone: "browser" }
    : { ...value, timezone: value.timezone || "browser" };
}
export function resolveTimeRange(value: TimeSelection, now = Date.now()) {
  const raw = rawSelection(value);
  const allowInstant = typeof value !== "string" && value.allowInstant === true;
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
  if (
    start < 0 ||
    end < start ||
    (end === start && !allowInstant) ||
    end - start > MAX_RANGE_MS
  )
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
    `${location.pathname}${params.size ? `?${params}` : ""}${location.hash}`,
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

export type ResolvedTimeRange = ReturnType<typeof resolveTimeRange>;
export type PanelTimeRange = ResolvedTimeRange & {
  sampledAt: number;
  shift?: string;
  comparison?: string;
  info: { timeFrom?: string; timeShift?: string; compareWith?: string };
};
// timeCompare is the serialized classic/V1 panel and V2 queryOptions field.
// Retain the early MetricsPanel compareWith spelling without overriding a saved
// standard value, including an explicitly disabled comparison (empty string).
export function panelComparison(overrides: {
  timeCompare?: string;
  compareWith?: string;
}) {
  return overrides.timeCompare ?? overrides.compareWith;
}
// Matches Grafana 13.2 PanelTimeRange: relative overrides require relative parent
// time, while shifts also apply to fixed dates and use calendar-aware date math.
export function resolvePanelTimeRange(
  value: TimeSelection,
  overrides: {
    timeFrom?: string;
    timeShift?: string;
    hideTimeOverride?: boolean;
    timeCompare?: string;
    compareWith?: string;
  },
  now = Date.now(),
): PanelTimeRange {
  let selection = rawSelection(value);
  let resolved = resolveTimeRange(selection, now);
  const info: PanelTimeRange["info"] = {};
  const comparison = panelComparison(overrides);
  if (comparison) info.compareWith = comparison;
  if (overrides.timeFrom) {
    const relative = rangeUtil.describeTextRange(overrides.timeFrom);
    if (relative.invalid) throw new Error("Invalid panel relative time");
    if (rangeUtil.isRelativeTimeRange(resolved.sdk.raw)) {
      selection = {
        from: relative.from,
        to: relative.to,
        timezone: resolved.timezone,
      };
      resolved = resolveTimeRange(selection, now);
      info.timeFrom = overrides.timeFrom;
    }
  }
  if (overrides.timeShift) {
    if (rangeUtil.describeTextRange(overrides.timeShift).invalid)
      throw new Error("Invalid panel time shift");
    const shift = "-" + overrides.timeShift;
    if (rangeUtil.isRelativeTimeRange(resolved.sdk.raw)) {
      // Semi-relative ranges retain their fixed anchor via ISO date math.
      const shifted = (bound: string | number) =>
        typeof bound === "string" && bound.startsWith("now")
          ? bound + shift
          : `${typeof bound === "number" || /^\d+$/.test(bound) ? dateTimeForTimeZone(resolved.timezone, Number(bound)).toISOString() : bound}${typeof bound === "string" && bound.includes("||") ? "" : "||"}${shift}`;
      selection = {
        from: shifted(selection.from),
        to: shifted(selection.to),
        timezone: resolved.timezone,
      };
    } else {
      const from = dateMath.parseDateMath(
        shift,
        dateTime(resolved.sdk.from),
        false,
      );
      const to = dateMath.parseDateMath(shift, dateTime(resolved.sdk.to), true);
      if (!from || !to) throw new Error("Invalid panel time shift");
      selection = {
        from: from.valueOf(),
        to: to.valueOf(),
        timezone: resolved.timezone,
      };
    }
    resolved = resolveTimeRange(selection, now);
    info.timeShift = overrides.timeShift;
  }
  return {
    ...resolved,
    sampledAt: now,
    shift: overrides.timeShift || undefined,
    comparison: comparison || undefined,
    info: overrides.hideTimeOverride ? {} : info,
  };
}

export function comparisonRefId(refId: string) {
  return refId.endsWith("-compare") ? refId : refId + "-compare";
}
export function resolveComparisonRange(
  primary: ResolvedTimeRange,
  compareWith: string,
): ResolvedTimeRange {
  let offset: number;
  if (compareWith === "__previousPeriod") offset = primary.end - primary.start;
  else {
    if (!/^\d+(?:\.\d+)?(?:ms|[Mwdhmsy])$/.test(compareWith))
      throw new Error("Invalid comparison interval");
    const interval = rangeUtil.describeInterval(compareWith);
    offset = Number.parseFloat(compareWith) * interval.sec * 1000;
  }
  if (!Number.isSafeInteger(offset) || offset <= 0)
    throw new Error("Invalid comparison interval");
  const resolved = resolveTimeRange({
    from: primary.start - offset,
    to: primary.end - offset,
    timezone: primary.timezone,
  });
  const raw = primary.sdk.raw;
  if (rangeUtil.isRelativeTimeRange(raw) && offset % 1000 === 0) {
    const period = rangeUtil.secondsToHms(offset / 1000);
    // secondsToHms chooses the largest unit; retain the remainder for non-preset windows.
    const exactPeriod =
      rangeUtil.intervalToMs(period) === offset ? period : `${offset / 1000}s`;
    const suffix =
      "-" + (compareWith === "__previousPeriod" ? exactPeriod : compareWith);
    resolved.sdk.raw = {
      from:
        typeof raw.from === "string" && raw.from.startsWith("now")
          ? raw.from + suffix
          : dateTime(resolved.sdk.from),
      to:
        typeof raw.to === "string" && raw.to.startsWith("now")
          ? raw.to + suffix
          : dateTime(resolved.sdk.to),
    };
  }
  return resolved;
}

export function panelZoomToDashboard(
  value: TimeSelection,
  shift?: string,
): TimeSelection {
  if (!shift) return value;
  const resolved = resolveTimeRange(value);
  const from = dateMath.parseDateMath(
    "+" + shift,
    dateTime(resolved.sdk.from),
    false,
  );
  const to = dateMath.parseDateMath(
    "+" + shift,
    dateTime(resolved.sdk.to),
    true,
  );
  if (!from || !to) throw new Error("Invalid panel time shift");
  const next = {
    from: from.valueOf(),
    to: to.valueOf(),
    timezone: resolved.timezone,
  };
  resolveTimeRange(next);
  return next;
}

function dashboardSpec(grafana: unknown): Record<string, unknown> | undefined {
  if (!grafana || typeof grafana !== "object") return;
  const doc = grafana as Record<string, unknown>;
  return doc.dashboard
    ? dashboardSpec(doc.dashboard)
    : doc.spec
      ? dashboardSpec(doc.spec)
      : doc;
}
export function refreshFromDashboard(grafana: unknown): string {
  const spec = dashboardSpec(grafana);
  const settings = spec?.timeSettings as { autoRefresh?: string } | undefined;
  const value = settings?.autoRefresh ?? spec?.refresh;
  return typeof value === "string" ? value : "";
}
export function refreshOptionsFromDashboard(grafana: unknown): string[] {
  const spec = dashboardSpec(grafana);
  const settings = spec?.timeSettings as
    { autoRefreshIntervals?: unknown } | undefined;
  const timepicker = spec?.timepicker as
    { refresh_intervals?: unknown } | undefined;
  const values =
    settings?.autoRefreshIntervals ?? timepicker?.refresh_intervals;
  return Array.isArray(values)
    ? values.filter((v): v is string => typeof v === "string" && !!v)
    : [];
}
export function refreshMilliseconds(
  value: string,
  range: TimeSelection,
  width = 1536,
): number {
  if (!value) return 0;
  if (value === "auto")
    return rangeUtil.calculateInterval(
      resolveTimeRange(range).sdk,
      Math.max(1, width),
      "5s",
    ).intervalMs;
  if (!/^\d+(?:\.\d+)?(?:ms|[Mwdhmsy])$/.test(value))
    throw new Error("Invalid refresh interval");
  const interval = rangeUtil.describeInterval(value);
  const milliseconds = Number.parseFloat(value) * interval.sec * 1000;
  if (!Number.isSafeInteger(milliseconds) || milliseconds <= 0)
    throw new Error("Invalid refresh interval");
  return Math.max(5000, milliseconds);
}
