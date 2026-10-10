import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import {
  FieldType,
  getFieldDisplayName,
  reduceField,
  fieldReducers,
  type DataFrame,
  type Field,
} from "@grafana/data";
import DOMPurify from "dompurify";
import { marked } from "marked";
import { Chart } from "../components/Chart";
import { message, type Panel, type InterpolationValues } from "../api";
import { t } from "../i18n";
import { watchFrames, framesAsSeries } from "./engine";
import { Subject } from "rxjs";
import type { FrameUpdate } from "./engine";
import type { PanelTimeRange, TimeSelection } from "./time-range";
const PluginPanel = lazy(() => import("./PluginPanel"));

function display(
  field: Field,
  value: unknown,
): { text: string; color?: string } {
  const formatted = field.display?.(value);
  return {
    text: formatted
      ? `${formatted.prefix || ""}${formatted.text}${formatted.suffix || ""}`
      : value == null
        ? "—"
        : String(value),
    color: formatted?.color,
  };
}

export default function GrafanaPanel({
  panel,
  values,
  range,
  tick,
  onUpdate,
  onRange,
}: {
  panel: Panel;
  values: InterpolationValues;
  range: TimeSelection;
  onRange: (range: TimeSelection) => void;
  tick: number;
  onUpdate?: (update: FrameUpdate) => void;
}) {
  const [frames, setFrames] = useState<DataFrame[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [streaming, setStreaming] = useState(false);
  const [queryRange, setQueryRange] = useState<PanelTimeRange>();
  const config = panel.config,
    type = panel.visualization || "timeseries",
    key = JSON.stringify(values);
  const refresh = useMemo(() => new Subject<void>(), []);
  useEffect(() => {
    if (type === "text" || type === "row") {
      setLoading(false);
      return;
    }
    setLoading(true);
    const listener = watchFrames(panel, values, range, refresh).subscribe({
      next: (update) => {
        onUpdate?.(update);
        setFrames(update.frames);
        setError(update.error);
        setLoading(update.loading);
        setStreaming(update.streaming);
        setQueryRange(update.timeRange);
      },
      error: (e) => {
        setFrames([]);
        setError(message(e));
        setLoading(false);
        setStreaming(false);
      },
    });
    return () => listener.unsubscribe();
  }, [JSON.stringify(panel), key, range, refresh]);
  useEffect(() => refresh.next(), [tick, refresh]);
  const effective = queryRange
    ? {
        from: queryRange.start,
        to: queryRange.end,
        timezone: queryRange.timezone,
      }
    : range;
  const timeInfo = [
    queryRange?.info.timeFrom
      ? `${t("Relative time")}: ${queryRange.info.timeFrom}`
      : "",
    queryRange?.info.timeShift
      ? `${t("Time shift")}: ${queryRange.info.timeShift}`
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const series = useMemo(
    () => framesAsSeries(frames, effective),
    [frames, queryRange, range],
  );
  if (type === "timeseries") {
    const first = frames
      .flatMap((f) => f.fields)
      .find((f) => f.type === FieldType.number);
    return (
      <Chart
        panel={panel}
        range={effective}
        timeInfo={timeInfo}
        tick={tick}
        result={series}
        resultError={error}
        streaming={streaming}
        onRange={onRange}
        formatter={(v) => (first ? display(first, v).text : String(v))}
      />
    );
  }
  if (type === "row")
    return (
      <div className="grafana-row">
        <h2>{panel.title}</h2>
      </div>
    );
  const supported = ["stat", "gauge", "bargauge", "table", "text"].includes(
    type,
  );
  if (!supported)
    return (
      <Suspense
        fallback={<div className="chart-panel">{t("Loading plugin…")}</div>}
      >
        <PluginPanel
          panel={panel}
          frames={frames}
          values={values}
          range={range}
          queryRange={queryRange}
          timeInfo={timeInfo}
          tick={tick}
          loading={loading}
          streaming={streaming}
          queryError={error}
          onRange={onRange}
        />
      </Suspense>
    );
  const reduce = config?.options?.reduceOptions || {},
    reducer = reduce.calcs?.[0] || "lastNotNull";
  let fields = frames.flatMap((frame) =>
    frame.fields
      .filter((field) => field.type === FieldType.number)
      .map((field) => ({ field, frame })),
  );
  if (reduce.fields) {
    try {
      const regex = new RegExp(reduce.fields);
      fields = fields.filter(({ field, frame }) =>
        regex.test(getFieldDisplayName(field, frame, frames)),
      );
    } catch {
      /* invalid field expression is surfaced by the template author */
    }
  }
  const stats = fields.flatMap(({ field, frame }) => {
    const values = reduce.values
      ? field.values.slice(0, Math.min(reduce.limit || 25, 500))
      : [
          fieldReducers.getIfExists(reducer)
            ? reduceField({ field, reducers: [reducer] })[reducer]
            : undefined,
        ];
    return values.map((value, i) => ({
      field,
      name:
        getFieldDisplayName(field, frame, frames) +
        (values.length > 1 ? ` ${i + 1}` : ""),
      value,
      formatted: display(field, value),
    }));
  });
  const content = config?.options?.content || "";
  const html =
    type === "text"
      ? DOMPurify.sanitize(
          config?.options?.mode === "html"
            ? content
            : marked.parse(content, { async: false }),
          {
            USE_PROFILES: { html: true },
            FORBID_TAGS: ["form", "input", "button", "iframe"],
          },
        )
      : "";
  return (
    <section
      className={`chart-panel grafana-panel grafana-${type}`}
      aria-label={panel.title}
    >
      <div className="panel-heading">
        <h2 title={config?.description}>{panel.title}</h2>
        {streaming && (
          <span className="status healthy" role="status">
            <i />
            {t("Live")}
          </span>
        )}
      </div>
      {timeInfo && <p className="panel-time-info">{timeInfo}</p>}
      {error && (
        <p className="form-error" role="alert">
          {t(error)}
        </p>
      )}
      {!supported && (
        <p role="alert">
          {t("Renderer required")}: <code>{config?.type || type}</code>
        </p>
      )}
      {type === "text" &&
        (config?.options?.mode === "code" ? (
          <pre>{content}</pre>
        ) : (
          <div
            className="grafana-text"
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ))}
      {type === "table" && (
        <div className="table-scroll grafana-table">
          {frames.map((frame, fi) => (
            <table key={fi}>
              <thead>
                <tr>
                  {frame.fields.map((field, i) => (
                    <th key={i}>{getFieldDisplayName(field, frame, frames)}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {Array.from(
                  { length: Math.min(frame.length, 500) },
                  (_, row) => (
                    <tr key={row}>
                      {frame.fields.map((field, i) => (
                        <td
                          key={i}
                          style={{
                            color: display(field, field.values[row]).color,
                          }}
                        >
                          {display(field, field.values[row]).text}
                        </td>
                      ))}
                    </tr>
                  ),
                )}
              </tbody>
            </table>
          ))}
          {frames.some((f) => f.length > 500) && (
            <p>{t("Showing first 500 rows")}</p>
          )}
        </div>
      )}
      {["stat", "gauge", "bargauge"].includes(type) && (
        <div
          className={`grafana-values ${config?.options?.orientation || "auto"}`}
        >
          {stats.map((stat, i) => {
            const min = stat.field.config.min ?? 0,
              max =
                stat.field.config.max ??
                (stat.field.config.unit === "percentunit" ? 1 : 100);
            const fraction = Math.max(
              0,
              Math.min(
                1,
                (Number(stat.value) - min) /
                  Math.max(Number.EPSILON, max - min),
              ),
            );
            return (
              <div className="grafana-value" key={i}>
                <span className="subtle">{stat.name}</span>
                {type === "gauge" && (
                  <svg
                    viewBox="0 0 160 90"
                    role="meter"
                    aria-label={stat.name}
                    aria-valuenow={Number(stat.value)}
                    aria-valuemin={min}
                    aria-valuemax={max}
                  >
                    <path
                      d="M 15 78 A 65 65 0 0 1 145 78"
                      fill="none"
                      stroke="#283044"
                      strokeWidth="12"
                    />
                    <path
                      d="M 15 78 A 65 65 0 0 1 145 78"
                      fill="none"
                      stroke={stat.formatted.color || "#ff9457"}
                      strokeWidth="12"
                      pathLength="100"
                      strokeDasharray={`${fraction * 100} 100`}
                    />
                  </svg>
                )}
                <strong style={{ color: stat.formatted.color }}>
                  {stat.formatted.text}
                </strong>
                {type === "bargauge" && (
                  <progress
                    max="1"
                    value={fraction}
                    aria-label={stat.name}
                    style={{ accentColor: stat.formatted.color || "#ff9457" }}
                  />
                )}
              </div>
            );
          })}
        </div>
      )}
      {!["text", "row"].includes(type) && !error && !frames.length && (
        <p className="subtle">
          {t(loading ? "Loading metrics…" : "No samples in this time range")}
        </p>
      )}
      {supported &&
        ["stat", "gauge", "bargauge"].includes(type) &&
        !fieldReducers.getIfExists(reducer) && (
          <p className="form-error" role="alert">
            {t("Unsupported reducer")}: {reducer}
          </p>
        )}
    </section>
  );
}
