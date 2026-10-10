import { Cron } from "croner";
import {
  FieldType,
  durationToMilliseconds,
  isValidDuration,
  parseDuration,
  type DataFrame,
} from "@grafana/data";
import type { AnnotationConfig } from "./annotation-config";

export type TimeRegionConfig = {
  mode?: null | "cron";
  from?: string;
  to?: string;
  fromDayOfWeek?: number;
  toDayOfWeek?: number;
  timezone?: string;
  cronExpr?: string;
  duration?: string;
  [key: string]: unknown;
};
const DAY = 1440;
const WEEK = 7 * DAY;
const MAX_REGIONS = 1000;

function clock(value: string) {
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(value))
    throw new Error("Invalid time region time");
  const [hour, minute] = value.split(":").map(Number);
  return hour * 60 + minute;
}

// Grafana's simple schedule contract uses elapsed durations: day-only ends
// include the whole end day; an explicit weekday wraps backwards across a week.
export function simpleTimeRegionPlan(config: TimeRegionConfig) {
  const { fromDayOfWeek: startDay, toDayOfWeek: endDay } = config;
  for (const day of [startDay, endDay])
    if (day != null && (!Number.isInteger(day) || day < 1 || day > 7))
      throw new Error("Invalid time region weekday");
  const from = config.from || undefined,
    to = config.to || undefined;
  if (startDay == null && from == null) return undefined;
  const startClock = clock(from || "00:00");
  const finishClock = clock(to || from || "00:00");
  const dayOnly = startDay != null && from == null && to == null;
  let minutes = (((endDay ?? startDay ?? 1) - (startDay ?? 1) + 7) % 7) * DAY;
  if (dayOnly) minutes = (minutes + DAY) % WEEK;
  else minutes += finishClock - startClock;
  if (minutes < 0) minutes += startDay == null && endDay == null ? DAY : WEEK;
  return {
    cronExpr: `${startClock % 60} ${Math.floor(startClock / 60)} * * ${startDay ?? "*"}`,
    durationMs: minutes * 60000,
  };
}

export function isTimeRegionQuery(config: AnnotationConfig) {
  return (config.target?.queryType ?? config.queryType) === "timeRegions";
}

export function calculateTimeRegions(
  config: TimeRegionConfig,
  range: { from: number; to: number },
) {
  if (!config || typeof config !== "object" || Array.isArray(config))
    throw new Error("Invalid time region configuration");
  let plan;
  if (config.mode === "cron") {
    if (
      typeof config.cronExpr !== "string" ||
      !config.cronExpr.trim() ||
      typeof config.duration !== "string" ||
      !isValidDuration(config.duration) ||
      /(^|\s)-/.test(config.duration)
    )
      throw new Error("Invalid time region Cron or duration");
    plan = {
      cronExpr: config.cronExpr,
      durationMs: durationToMilliseconds(parseDuration(config.duration)),
    };
  } else {
    if (config.mode != null)
      throw new Error("Invalid time region configuration");
    plan = simpleTimeRegionPlan(config);
  }
  if (!plan) return [];
  const { durationMs, cronExpr } = plan;
  const first = range.from - durationMs;
  if (
    ![range.from, range.to, durationMs, first].every(Number.isSafeInteger) ||
    range.to < range.from ||
    durationMs < 0 ||
    !Number.isFinite(new Date(first).valueOf())
  )
    throw new Error("Invalid time region range or duration");
  const timezone =
    config.timezone === "browser"
      ? undefined
      : config.timezone === "utc"
        ? "Etc/UTC"
        : config.timezone;
  if (timezone != null) {
    try {
      new Intl.DateTimeFormat("en", { timeZone: timezone });
    } catch {
      throw new Error("Invalid time zone");
    }
  }
  // No callback is supplied: Croner evaluates dates without starting a timer.
  const cron = new Cron(cronExpr, { timezone });
  const regions: { from: number; to: number }[] = [];
  try {
    let previous = first;
    let next = cron.nextRun(new Date(previous));
    while (next && next.valueOf() <= range.to) {
      const start = next.valueOf(),
        end = start + durationMs;
      if (
        start <= previous ||
        !Number.isSafeInteger(end) ||
        !Number.isFinite(new Date(end).valueOf())
      )
        throw new Error("Invalid time region range or duration");
      if (regions.length === MAX_REGIONS)
        throw new Error("At most 1000 time regions per query");
      regions.push({ from: start, to: end });
      previous = start;
      next = cron.nextRun(next);
    }
  } finally {
    cron.stop();
  }
  return regions;
}

export function timeRegionQuery(
  config: AnnotationConfig,
  range: { from: number; to: number },
) {
  const schedule: TimeRegionConfig =
    config.target?.timeRegion ?? config.timeRegion ?? {};
  const name = String(config.name || "Annotations");
  const regions = calculateTimeRegions(schedule, range);
  // Include only public recurrence fields in identity, never arbitrary plugin data.
  const namespace = JSON.stringify([
    name,
    schedule.mode,
    schedule.fromDayOfWeek,
    schedule.from,
    schedule.toDayOfWeek,
    schedule.to,
    schedule.cronExpr,
    schedule.duration,
    schedule.timezone,
  ]);
  const events = regions.map(({ from, to }) => ({
    key: `time-region:${namespace}:${from}:${to}`,
    readOnly: true,
    time: from,
    timeEnd: to,
    text: name,
    tags: [] as string[],
  }));
  const frames: DataFrame[] = events.length
    ? [
        {
          length: events.length,
          fields: [
            {
              name: "time",
              type: FieldType.time,
              config: {},
              values: events.map((event) => event.time),
            },
            {
              name: "timeEnd",
              type: FieldType.time,
              config: {},
              values: events.map((event) => event.timeEnd),
            },
            {
              name: "text",
              type: FieldType.string,
              config: {},
              values: events.map((event) => event.text),
            },
          ],
        },
      ]
    : [];
  return { events, frames };
}
