import {
  CoreApp,
  type QueryEditorProps,
  type DataSourceApi,
} from "@grafana/data";
import { useEffect, useState } from "react";
import type { GrafanaQuery } from "./grafana-source";
import { TimeRegionEditor } from "../components/TimeRegionEditor";
import { t } from "../i18n";

const modes = [
  ["randomWalk", "Random walk"],
  ["measurements", "Live measurements"],
  ["list", "Public files"],
  ["snapshot", "Snapshot"],
  ["annotations", "Annotation events"],
  ["timeRegions", "Time regions"],
] as const;

function commaValues(text: string) {
  return text
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
}
function CommaInput({
  label,
  values,
  onChange,
}: {
  label: string;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  const [text, setText] = useState(() => values.join(", "));
  const serialized = JSON.stringify(values);
  useEffect(() => {
    setText((previous) =>
      JSON.stringify(commaValues(previous)) === serialized
        ? previous
        : values.join(", "),
    );
  }, [serialized]);
  return (
    <label>
      {t(label)}
      <input
        aria-label={t(label)}
        value={text}
        onChange={(event) => {
          setText(event.target.value);
          onChange(commaValues(event.target.value));
        }}
      />
    </label>
  );
}
const numericFields = [
  ["seriesCount", "Series count", "1", 0, 128],
  ["startValue", "Start value", "Auto"],
  ["min", "Minimum value", "None"],
  ["max", "Maximum value", "None"],
  ["spread", "Spread", "1", 0],
  ["noise", "Noise", "0", 0],
  ["dropPercent", "Drop percent", "0", 0, 100],
] as const;

// Edit Grafana's public query model directly. Changing a mode retains its other
// fields, so imported snapshots and future options survive a round trip.
export function GrafanaQueryEditor({
  query,
  onChange,
  onRunQuery,
  app,
}: QueryEditorProps<DataSourceApi<GrafanaQuery>, GrafanaQuery>) {
  const mode = query.queryType || "randomWalk";
  const patch = (update: Partial<GrafanaQuery>) =>
    onChange({ ...query, ...update });
  const known = modes.some(([value]) => value === mode);
  const serverQuery = mode === "randomWalk" || mode === "list";
  const alerting = app === CoreApp.UnifiedAlerting;
  const annotation = query.target || query;
  const patchAnnotation = (update: Record<string, unknown>) =>
    patch({
      target: {
        type: annotation.type || "dashboard",
        tags: annotation.tags || [],
        limit: annotation.limit ?? 100,
        matchAny: annotation.matchAny ?? false,
        ...query.target,
        ...update,
      },
    });
  return (
    <div className="grafana-query-editor">
      <label>
        {t("Builtin query type")}
        <select
          aria-label={t("Builtin query type")}
          value={mode}
          onChange={(event) => patch({ queryType: event.target.value })}
        >
          {modes.map(([value, label]) => (
            <option
              key={value}
              value={value}
              disabled={alerting && value !== "randomWalk" && value !== "list"}
            >
              {t(label)}
            </option>
          ))}
          {!known ? <option value={mode}>{mode}</option> : null}
        </select>
      </label>
      {mode === "randomWalk" ? (
        <div className="grafana-random-fields">
          {numericFields.map(([key, label, placeholder, ...bounds]) => (
            <label key={key}>
              {t(label)}
              <input
                type="number"
                aria-label={t(label)}
                step={key === "seriesCount" ? 1 : "any"}
                min={bounds[0]}
                max={bounds[1]}
                placeholder={t(placeholder)}
                value={query[key] ?? ""}
                onChange={(event) =>
                  patch({
                    [key]:
                      event.target.value === ""
                        ? undefined
                        : event.target.valueAsNumber,
                  })
                }
              />
            </label>
          ))}
        </div>
      ) : mode === "measurements" ? (
        <>
          <label>
            {t("Live channel")}
            <input
              aria-label={t("Live channel")}
              placeholder="ds/UID/STREAM"
              value={query.channel || ""}
              onChange={(event) => patch({ channel: event.target.value })}
            />
          </label>
          <div className="form-row">
            <CommaInput
              label="Live fields (comma separated)"
              values={query.filter?.fields || []}
              onChange={(fields) =>
                patch({ filter: { ...query.filter, fields } })
              }
            />
            <label>
              {t("Live buffer (ms)")}
              <input
                type="number"
                aria-label={t("Live buffer (ms)")}
                min={0}
                step={1}
                placeholder={t("Auto")}
                value={query.buffer ?? ""}
                onChange={(event) =>
                  patch({
                    buffer:
                      event.target.value === ""
                        ? undefined
                        : event.target.valueAsNumber,
                  })
                }
              />
            </label>
          </div>
        </>
      ) : mode === "list" ? (
        <label>
          {t("Public asset folder")}
          <input
            aria-label={t("Public asset folder")}
            value={query.path || ""}
            placeholder="assets"
            onChange={(event) => patch({ path: event.target.value })}
          />
        </label>
      ) : mode === "snapshot" ? (
        <p className="subtle">
          {t("Snapshot frames")}:{" "}
          <output aria-label={t("Snapshot frames")}>
            {query.snapshot?.length || 0}
          </output>
          . {t("Snapshot data is preserved. Edit frames in advanced JSON.")}
        </p>
      ) : mode === "timeRegions" ? (
        <TimeRegionEditor
          value={query.timeRegion || {}}
          onChange={(timeRegion) => patch({ timeRegion })}
        />
      ) : mode === "annotations" ? (
        <>
          <label>
            {t("Annotation scope")}
            <select
              aria-label={t("Annotation scope")}
              value={annotation.type || "dashboard"}
              onChange={(event) =>
                patchAnnotation({ type: event.target.value })
              }
            >
              <option value="dashboard">{t("This dashboard")}</option>
              <option value="tags">{t("By tags")}</option>
            </select>
          </label>
          {annotation.type === "tags" ? (
            <>
              <CommaInput
                label="Tags (comma separated)"
                values={annotation.tags || []}
                onChange={(tags) => patchAnnotation({ tags })}
              />
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  checked={Boolean(annotation.matchAny)}
                  onChange={(event) =>
                    patchAnnotation({ matchAny: event.target.checked })
                  }
                />
                {t("Match any tag")}
              </label>
            </>
          ) : null}
          <label>
            {t("Event limit")}
            <input
              type="number"
              aria-label={t("Event limit")}
              min={1}
              max={1000}
              step={1}
              value={annotation.limit ?? 100}
              onChange={(event) =>
                patchAnnotation({ limit: event.target.valueAsNumber })
              }
            />
          </label>
        </>
      ) : (
        <p className="subtle">
          {t("Unknown builtin query type. Use advanced JSON.")}
        </p>
      )}
      {alerting && !serverQuery ? (
        <p role="alert" className="form-error">
          {t(
            "This query runs in the browser and cannot evaluate server-side alerts.",
          )}
        </p>
      ) : null}
      <button
        type="button"
        disabled={!known || (alerting && !serverQuery)}
        onClick={onRunQuery}
      >
        {t("Run builtin query")}
      </button>
    </div>
  );
}
