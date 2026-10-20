import { useMemo } from "react";
import { Pencil, Trash2 } from "lucide-react";
import { t } from "../../i18n";
import type { RendererProps } from "../types";
import "./HistogramRenderer.css";

type Bar = { label: string; value: number };

function histogramBars(frames: RendererProps["frames"]): Bar[] {
  const frame = frames[0];
  if (!frame) return [];
  const strings = frame.fields.filter((field) => field.type === "string");
  const numbers = frame.fields.filter((field) => field.type === "number");
  if (strings.length && numbers.length === 1) {
    const labels = strings[0].values;
    const values = numbers[0].values;
    return Array.from(values, (value, index) => {
      const number = Number(value);
      return {
        label: String(labels[index] ?? index),
        value: Number.isFinite(number) ? number : 0,
      };
    });
  }
  return numbers.flatMap((field) => {
    let value: number | undefined;
    for (let index = field.values.length - 1; index >= 0; index -= 1) {
      const number = Number(field.values[index]);
      if (Number.isFinite(number)) {
        value = number;
        break;
      }
    }
    if (value == null) return [];
    return [{
      label:
        field.labels?.le ||
        field.labels?.bucket ||
        field.labels?.upper_bound ||
        field.name ||
        "",
      value,
    }];
  });
}

export default function HistogramRenderer({
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
  const bars = useMemo(() => histogramBars(frames), [frames]);
  const max = useMemo(
    () => bars.reduce((highest, bar) => Math.max(highest, bar.value), 0),
    [bars],
  );
  return (
    <section
      className="chart-panel native-histogram-panel"
      data-bar-count={bars.length}
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
      <div className="native-histogram-viewport">
        {bars.length ? (
          <div className="native-histogram-bars" role="img" aria-label={t(panel.title)}>
            {bars.map((bar, index) => (
              <div className="native-histogram-bar" key={`${bar.label}:${index}`}>
                <span className="native-histogram-value">{bar.value}</span>
                <span
                  className="native-histogram-fill"
                  style={{
                    height: `${max > 0 ? Math.max(2, (bar.value / max) * 100) : 2}%`,
                  }}
                />
                <span className="native-histogram-label" title={bar.label}>
                  {bar.label}
                </span>
              </div>
            ))}
          </div>
        ) : (
          <p className="native-histogram-empty" role="status">
            {loading ? t("Loading metrics…") : t("No samples in this time range")}
          </p>
        )}
      </div>
    </section>
  );
}
