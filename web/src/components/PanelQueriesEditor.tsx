import { useEffect, useRef, useState } from "react";
import {
  CoreApp,
  LoadingState,
  type DataQuery,
  type PanelData,
} from "@grafana/data";
import type { Subscription } from "rxjs";
import {
  api,
  interpolate,
  jsonBody,
  message,
  type Dashboard,
  type InterpolationValues,
} from "../api";
import { t } from "../i18n";
import {
  watchFrames,
  type FrameUpdate,
  type GrafanaTarget,
} from "../grafana/engine";
import { Subject } from "rxjs";
import {
  DatasourceQueryEditor,
  useDatasourceEditor,
} from "./DatasourceQueryEditor";
import {
  panelWithQueries,
  validatePanelQueries,
  withPanelQueries,
} from "../grafana/panel-queries";
import { resolveTimeRange, type TimeSelection } from "../grafana/time-range";
import { Dialog } from "./Dialog";

type Draft = { key: number; originalRef?: string; query: GrafanaTarget };
function nextReference(queries: GrafanaTarget[]) {
  const refs = new Set(queries.map((query) => query.refId));
  for (let n = 0; ; n++) {
    let index = n,
      value = "";
    do {
      value = String.fromCharCode(65 + (index % 26)) + value;
      index = Math.floor(index / 26) - 1;
    } while (index >= 0);
    if (!refs.has(value)) return value;
  }
}

export function PanelQueriesEditor({
  dashboard,
  values,
  range,
  reload,
  onClose,
}: {
  dashboard: Dashboard;
  values: InterpolationValues;
  range: TimeSelection;
  reload: () => Promise<void>;
  onClose: () => void;
}) {
  const baseline = useRef(dashboard);
  const counter = useRef(0);
  const [allDrafts, setAllDrafts] = useState<Record<string, Draft[]>>(() =>
    Object.fromEntries(
      dashboard.panels
        .filter((panel) => panel.visualization !== "row")
        .map((panel) => [
          panel.id,
          (panel.config?.targets || []).map((query) => ({
            key: counter.current++,
            originalRef: query.refId,
            query: structuredClone(query),
          })),
        ]),
    ),
  );
  const [panelID, setPanelID] = useState(Object.keys(allDrafts)[0] || "");
  const [selected, setSelected] = useState<number | undefined>(
    allDrafts[panelID]?.[0]?.key,
  );
  const [dirty, setDirty] = useState<Set<string>>(() => new Set());
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [preview, setPreview] = useState<FrameUpdate | null>(null);
  const [raw, setRaw] = useState("");
  const [validJSON, setValidJSON] = useState(true);
  const subscription = useRef<Subscription | null>(null);
  const panel = baseline.current.panels.find((item) => item.id === panelID);
  const drafts = allDrafts[panelID] || [];
  const queries = drafts.map((draft) => draft.query);
  const current = drafts.find((draft) => draft.key === selected);
  const query = current?.query;
  const ref = query?.datasource || panel?.config?.datasource;
  const rawUID = typeof ref === "string" ? ref : ref?.uid || "";
  const uid =
    interpolate(rawUID, values, range) ||
    (typeof ref === "object" && ref?.type === "__expr__" ? "__expr__" : "");
  const { sources, loaded, loadError } = useDatasourceEditor(
    uid,
    Boolean(query),
    values,
    range,
  );
  const resolved = resolveTimeRange(range);
  const panelData: PanelData | undefined = preview
    ? {
        state: preview.error
          ? LoadingState.Error
          : preview.loading
            ? LoadingState.Loading
            : preview.streaming
              ? LoadingState.Streaming
              : LoadingState.Done,
        series: preview.frames.filter((frame) => frame.refId === query?.refId),
        timeRange: preview.timeRange?.sdk || resolved.sdk,
        ...(preview.error ? { error: { message: preview.error } } : {}),
      }
    : undefined;
  const stop = () => {
    subscription.current?.unsubscribe();
    subscription.current = null;
    setRunning(false);
    setPreview((previous) =>
      previous ? { ...previous, loading: false, streaming: false } : previous,
    );
  };
  const updateDrafts = (next: Draft[]) => {
    stop();
    setPreview(null);
    setError("");
    setAllDrafts((previous) => ({ ...previous, [panelID]: next }));
    setDirty((previous) => new Set(previous).add(panelID));
  };
  const change = (next: GrafanaTarget, updateRaw = true) => {
    if (!current) return;
    updateDrafts(
      drafts.map((draft) =>
        draft.key === current.key ? { ...draft, query: next } : draft,
      ),
    );
    if (updateRaw) setRaw(JSON.stringify(next, null, 2));
    if (updateRaw) setValidJSON(true);
  };
  const patch = (update: GrafanaTarget) =>
    query && change({ ...query, ...update });
  const select = (draft?: Draft) => {
    setSelected(draft?.key);
    setRaw(JSON.stringify(draft?.query || {}, null, 2));
    setValidJSON(true);
    setError("");
  };
  const valuesKey = JSON.stringify(values),
    rangeKey = JSON.stringify(range);
  useEffect(
    () => () => {
      subscription.current?.unsubscribe();
    },
    [],
  );
  useEffect(() => {
    subscription.current?.unsubscribe();
    subscription.current = null;
    setRunning(false);
    setPreview(null);
  }, [panelID, rangeKey, valuesKey]);
  const run = () => {
    if (!panel || !validJSON) return;
    stop();
    setPreview(null);
    setError("");
    try {
      validatePanelQueries(queries);
      setRunning(true);
      subscription.current = watchFrames(
        panelWithQueries(panel, queries),
        values,
        range,
        new Subject(),
      ).subscribe({
        next: (update) => {
          setPreview(update);
          if (update.error) {
            setError(update.error);
          }
          if (update.error || (!update.loading && !update.streaming)) stop();
        },
        error: (error) => {
          setError(message(error));
          setRunning(false);
        },
        complete: () => setRunning(false),
      });
    } catch (error) {
      setError(message(error));
      setRunning(false);
    }
  };
  const add = (expression: boolean, incoming?: DataQuery) => {
    if (!panel || drafts.length >= 32) return;
    const refId = nextReference(queries);
    const item: Draft = {
      key: counter.current++,
      query: incoming
        ? { ...incoming, refId, datasource: incoming.datasource || undefined }
        : expression
          ? {
              refId,
              datasource: { uid: "__expr__", type: "__expr__" },
              type: "math",
              expression: "",
            }
          : {
              refId,
              datasource: { uid: "metricspanel", type: "prometheus" },
              expr: "",
              instant: true,
            },
    };
    updateDrafts([...drafts, item]);
    select(item);
  };
  const save = async () => {
    if (!validJSON) return;
    setSaving(true);
    stop();
    setError("");
    try {
      let updated = baseline.current;
      for (const id of dirty) {
        const entries = allDrafts[id];
        updated = withPanelQueries(
          updated,
          id,
          entries.map((entry) => entry.query),
          entries.map((entry) => entry.originalRef),
        );
      }
      await api(`/dashboards/${encodeURIComponent(updated.id)}`, {
        method: "PUT",
        headers: { "If-Match": String(baseline.current.updated_at) },
        body: jsonBody(updated),
      });
      await reload();
      onClose();
    } catch (error) {
      setError(t(message(error)));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={t("Panel queries")}
      onClose={saving ? () => {} : onClose}
      style={{ width: 1040 }}
    >
      <div className="panel-query-editor">
        <fieldset disabled={saving} className="query-editor-fields">
          <label>
            {t("Panel")}
            <select
              aria-label={t("Panel")}
              value={panelID}
              disabled={!validJSON}
              onChange={(event) => {
                const id = event.target.value;
                stop();
                setPanelID(id);
                select(allDrafts[id]?.[0]);
              }}
            >
              {baseline.current.panels
                .filter((item) => item.visualization !== "row")
                .map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.title}
                  </option>
                ))}
            </select>
          </label>
          <div className="annotation-query-layout">
            <nav
              className="annotation-query-list"
              aria-label={t("Panel query list")}
            >
              {drafts.map((draft) => (
                <button
                  type="button"
                  key={draft.key}
                  disabled={!validJSON}
                  aria-current={draft.key === selected ? "page" : undefined}
                  onClick={() => select(draft)}
                >
                  {draft.query.refId}
                  <small>
                    {draft.query.type === "sql"
                      ? "SQL"
                      : (draft.query.datasource &&
                          (typeof draft.query.datasource === "string"
                            ? draft.query.datasource
                            : draft.query.datasource.uid)) ||
                        t("Default datasource")}
                  </small>
                </button>
              ))}
              <button
                type="button"
                disabled={!panel || drafts.length >= 32 || !validJSON}
                onClick={() => add(false)}
              >
                {t("Add query")}
              </button>
              <button
                type="button"
                disabled={!panel || drafts.length >= 32 || !validJSON}
                onClick={() => add(true)}
              >
                {t("Add expression")}
              </button>
            </nav>
            <div className="annotation-query-fields">
              {query ? (
                <>
                  <div className="form-row">
                    <label>
                      {t("Query reference")}
                      <input
                        aria-label={t("Query reference")}
                        value={query.refId || ""}
                        maxLength={100}
                        disabled={!validJSON}
                        onChange={(event) =>
                          patch({ refId: event.target.value })
                        }
                      />
                    </label>
                    <label>
                      {t("Datasource")}
                      <select
                        aria-label={t("Datasource")}
                        value={rawUID}
                        disabled={!validJSON}
                        onChange={(event) => {
                          const source = sources.find(
                            (source) => source.uid === event.target.value,
                          );
                          patch({
                            datasource: {
                              uid: event.target.value,
                              type: source?.type,
                            },
                            ...(source?.type === "__expr__" && !query.type
                              ? { type: "math", expression: "" }
                              : {}),
                          });
                        }}
                      >
                        <option value="">{t("Default datasource")}</option>
                        {sources.map((source) => (
                          <option key={source.uid} value={source.uid}>
                            {t(source.name)}
                          </option>
                        ))}
                        {(dashboard.variables || [])
                          .filter((variable) => variable.type === "datasource")
                          .map((variable) => (
                            <option
                              key={variable.name}
                              value={`$${variable.name}`}
                            >
                              ${variable.name}
                            </option>
                          ))}
                        {rawUID &&
                        !sources.some((source) => source.uid === rawUID) &&
                        !(dashboard.variables || []).some(
                          (variable) => `$${variable.name}` === rawUID,
                        ) ? (
                          <option value={rawUID}>{rawUID}</option>
                        ) : null}
                      </select>
                    </label>
                  </div>
                  <div className="query-actions">
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={Boolean(query.hide)}
                        disabled={!validJSON}
                        onChange={(event) =>
                          patch({ hide: event.target.checked })
                        }
                      />
                      {t("Hide from visualization")}
                    </label>
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={query.timeRangeCompare !== false}
                        disabled={!validJSON}
                        onChange={(event) =>
                          patch({ timeRangeCompare: event.target.checked })
                        }
                      />
                      {t("Include in comparison")}
                    </label>
                  </div>
                  {loadError ? (
                    <p role="alert" className="form-error">
                      {loadError}
                    </p>
                  ) : loaded ? (
                    <DatasourceQueryEditor
                      loaded={loaded}
                      query={query}
                      queries={queries}
                      range={resolved.sdk}
                      data={panelData}
                      app={CoreApp.PanelEditor}
                      editorKey={selected || 0}
                      disabled={!validJSON}
                      onChange={(next) => change(next)}
                      onRunQuery={run}
                      onAddQuery={(next) => add(false, next)}
                    />
                  ) : (
                    <p role="status">{t("Loading datasource…")}</p>
                  )}
                  <details className="annotation-advanced">
                    <summary>{t("Advanced query JSON")}</summary>
                    <textarea
                      rows={9}
                      className="mono"
                      aria-label={t("Query JSON")}
                      value={raw || JSON.stringify(query, null, 2)}
                      onChange={(event) => {
                        const text = event.target.value;
                        stop();
                        setPreview(null);
                        setRaw(text);
                        try {
                          const parsed = JSON.parse(text);
                          if (
                            !parsed ||
                            typeof parsed !== "object" ||
                            Array.isArray(parsed)
                          )
                            throw new Error();
                          change(parsed, false);
                          setValidJSON(true);
                        } catch {
                          setValidJSON(false);
                        }
                      }}
                    />
                    {!validJSON ? (
                      <p role="alert" className="form-error">
                        {t("Query JSON must be an object")}
                      </p>
                    ) : null}
                  </details>
                  <div className="query-actions">
                    <button
                      type="button"
                      disabled={
                        !validJSON ||
                        drafts.findIndex((draft) => draft.key === selected) ===
                          0
                      }
                      onClick={() => {
                        const next = [...drafts],
                          index = next.findIndex(
                            (draft) => draft.key === selected,
                          );
                        [next[index - 1], next[index]] = [
                          next[index],
                          next[index - 1],
                        ];
                        updateDrafts(next);
                      }}
                    >
                      {t("Move query up")}
                    </button>
                    <button
                      type="button"
                      disabled={
                        !validJSON ||
                        drafts.findIndex((draft) => draft.key === selected) ===
                          drafts.length - 1
                      }
                      onClick={() => {
                        const next = [...drafts],
                          index = next.findIndex(
                            (draft) => draft.key === selected,
                          );
                        [next[index + 1], next[index]] = [
                          next[index],
                          next[index + 1],
                        ];
                        updateDrafts(next);
                      }}
                    >
                      {t("Move query down")}
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        const next = drafts.filter(
                          (draft) => draft.key !== selected,
                        );
                        updateDrafts(next);
                        select(next[0]);
                      }}
                    >
                      {t("Remove query")}
                    </button>
                  </div>
                </>
              ) : (
                <p>{t("No panel queries")}</p>
              )}
            </div>
          </div>
        </fieldset>
        <div className="query-actions">
          <button
            type="button"
            disabled={saving || !validJSON || !panel}
            onClick={running ? stop : run}
          >
            {t(running ? "Stop query" : "Run query")}
          </button>
          <button
            type="button"
            className="primary"
            disabled={saving || !validJSON || !dirty.size}
            onClick={() => void save()}
          >
            {t(saving ? "Saving…" : "Save queries")}
          </button>
        </div>
        {error ? (
          <p role="alert" className="form-error">
            {error}
          </p>
        ) : null}
        {preview ? (
          <section
            className="annotation-query-preview"
            aria-label={t("Query preview")}
          >
            <h3>{t("Query preview")}</h3>
            {preview.frames.map((frame, index) => (
              <div key={index} className="query-result-frame">
                <h4>{frame.refId || frame.name || t("Query result")}</h4>
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        {frame.fields.map((field, i) => (
                          <th key={i}>
                            {field.config.displayNameFromDS || field.name}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {Array.from(
                        { length: Math.min(5, frame.length) },
                        (_, row) => (
                          <tr key={row}>
                            {frame.fields.map((field, i) => (
                              <td key={i}>
                                {field.values[row] == null
                                  ? "—"
                                  : String(field.values[row])}
                              </td>
                            ))}
                          </tr>
                        ),
                      )}
                    </tbody>
                  </table>
                </div>
                {frame.length > 5 ? (
                  <p className="subtle">
                    {t("Showing first 5 rows")} / {frame.length}
                  </p>
                ) : null}
              </div>
            ))}
            <details>
              <summary>{t("Frame JSON")}</summary>
              <pre className="query-preview-data">
                {JSON.stringify(
                  preview.frames.map((frame) => ({
                    refId: frame.refId,
                    rows: frame.length,
                    fields: frame.fields.map((field) => ({
                      name: field.name,
                      values: field.values.slice(0, 5),
                    })),
                  })),
                  null,
                  2,
                )}
              </pre>
            </details>
          </section>
        ) : null}
      </div>
    </Dialog>
  );
}
