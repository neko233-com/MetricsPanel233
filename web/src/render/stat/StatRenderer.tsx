import {
  FieldType,
  fieldReducers,
  getFieldDisplayName,
  reduceField,
  type Field,
} from "@grafana/data";
import { Pencil, Trash2 } from "lucide-react";
import { t } from "../../i18n";
import type { RendererProps } from "../types";

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

/**
 * Renders stat, gauge and bar-gauge panels from the same reduced field values.
 * The variant comes from the panel visualization so one registry entry per kind
 * can share the reducer, threshold color and empty-state behavior.
 */
export default function StatRenderer({
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
  const type = panel.visualization || config?.type || "stat";
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
  const unsupported = !fieldReducers.getIfExists(reducer);
  return (
    <section
      className={`chart-panel grafana-panel grafana-${type}`}
      aria-label={panel.title}
    >
      <div className="panel-heading">
        <h2 title={config?.description}>{panel.title}</h2>
        <div className="panel-actions">
          {onEdit ? (
            <button
              className="icon-button"
              aria-label={`${t("Edit")} ${panel.title}`}
              onClick={onEdit}
            >
              <Pencil size={15} />
            </button>
          ) : null}
          {onRemove ? (
            <button
              className="icon-button danger-hover"
              aria-label={`${t("Remove")} ${panel.title}`}
              onClick={onRemove}
            >
              <Trash2 size={15} />
            </button>
          ) : null}
        </div>
        {streaming ? (
          <span className="status healthy" role="status">
            <i />
            {t("Live")}
          </span>
        ) : null}
      </div>
      {timeInfo ? <p className="panel-time-info">{timeInfo}</p> : null}
      {queryError ? (
        <p className="form-error" role="alert">
          {t(queryError)}
        </p>
      ) : null}
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
              {type === "gauge" ? (
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
              ) : null}
              <strong style={{ color: stat.formatted.color }}>
                {stat.formatted.text}
              </strong>
              {type === "bargauge" ? (
                <progress
                  max="1"
                  value={fraction}
                  aria-label={stat.name}
                  style={{ accentColor: stat.formatted.color || "#ff9457" }}
                />
              ) : null}
            </div>
          );
        })}
      </div>
      {!queryError && !frames.length ? (
        <p className="subtle">
          {t(loading ? "Loading metrics…" : "No samples in this time range")}
        </p>
      ) : null}
      {unsupported ? (
        <p className="form-error" role="alert">
          {t("Unsupported reducer")}: {reducer}
        </p>
      ) : null}
    </section>
  );
}
