import { useEffect, useRef, useState } from "react";
import {
  CoreApp,
  LoadingState,
  dateTimeForTimeZone,
  dataFrameFromJSON,
  type DataFrame,
  type DataQuery,
  type PanelData,
} from "@grafana/data";
import { api, jsonBody, message } from "../api";
import { t } from "../i18n";
import {
  alertEditorTarget,
  alertQueryUID,
  nextAlertReference,
  validateAlertEditorGraph,
  withAlertTarget,
  type AlertQueryGraph,
  type AlertQueryNode,
} from "../grafana/alert-editor";
import {
  DatasourceQueryEditor,
  useDatasourceEditor,
} from "./DatasourceQueryEditor";
import type { GrafanaTarget } from "../grafana/engine";

type Preview = {
  at: number;
  condition: string;
  condition_error?: string;
  condition_values: {
    labels: Record<string, string>;
    value: number | null;
    value_text?: string;
    missing: boolean;
    satisfied: boolean | null;
  }[];
  results: Record<
    string,
    { frames?: any[]; error?: string; errorSource?: string }
  >;
};
const emptyValues = {};
export function AlertGraphEditor({
  graph,
  onChange,
  disabled = false,
}: {
  graph: AlertQueryGraph;
  onChange: (graph: AlertQueryGraph) => void;
  disabled?: boolean;
}) {
  const [selected, setSelected] = useState(0),
    [at, setAt] = useState(() => Date.now());
  const [running, setRunning] = useState(false),
    [preview, setPreview] = useState<Preview | null>(null),
    [error, setError] = useState("");
  const controller = useRef<AbortController | null>(null),
    generation = useRef(0);
  const node = graph.data?.[selected],
    target = node && alertEditorTarget(node);
  const range = {
    from: at - (node?.relativeTimeRange?.from || 0) * 1000,
    to: at - (node?.relativeTimeRange?.to || 0) * 1000,
    timezone: "utc",
    allowInstant: true,
  };
  // Alert expressions legitimately use a zero-length window; dashboard range
  // validation requires a positive span and cannot construct this SDK context.
  const sdkRange = {
    from: dateTimeForTimeZone("utc", range.from),
    to: dateTimeForTimeZone("utc", range.to),
    raw: {
      from: dateTimeForTimeZone("utc", range.from),
      to: dateTimeForTimeZone("utc", range.to),
    },
  };
  const { sources, loaded, loadError } = useDatasourceEditor(
    node ? alertQueryUID(node) : "",
    Boolean(node),
    emptyValues,
    range,
  );
  const stop = () => {
    generation.current++;
    controller.current?.abort();
    controller.current = null;
    setRunning(false);
  };
  const change = (next: AlertQueryGraph) => {
    stop();
    setPreview(null);
    setError("");
    onChange(next);
  };
  const updateNode = (update: Partial<AlertQueryNode>) =>
    change({
      ...graph,
      data: graph.data.map((item, index) =>
        index === selected ? { ...item, ...update } : item,
      ),
    });
  const patchModel = (update: Record<string, any>) =>
    node && updateNode({ model: { ...node.model, ...update } });
  const graphKey = JSON.stringify(graph);
  useEffect(() => {
    controller.current?.abort();
    generation.current++;
    setRunning(false);
    setPreview(null);
  }, [graphKey]);
  useEffect(
    () => () => {
      controller.current?.abort();
      generation.current++;
    },
    [],
  );
  useEffect(() => {
    setSelected((current) =>
      Math.min(current, Math.max(0, graph.data.length - 1)),
    );
  }, [graph.data.length]);
  const run = async () => {
    stop();
    setPreview(null);
    setError("");
    const ticket = ++generation.current;
    try {
      validateAlertEditorGraph(graph);
      const now = Date.now(),
        request = new AbortController();
      controller.current = request;
      setRunning(true);
      setAt(now);
      const result = await api<Preview>("/alerts/preview", {
        method: "POST",
        signal: request.signal,
        body: jsonBody({ grafana: graph, at: now }),
      });
      if (ticket !== generation.current) return;
      setPreview(result);
      setError(result.condition_error || "");
      setRunning(false);
      controller.current = null;
    } catch (error) {
      if (ticket !== generation.current) return;
      if ((error as { name?: string }).name === "AbortError") return;
      setError(t(message(error)));
      setRunning(false);
    }
  };
  const decoded: Record<string, DataFrame[]> = {};
  let decodingError = "";
  try {
    for (const [ref, response] of Object.entries(preview?.results || {}))
      decoded[ref] = (response.frames || []).map((frame) => ({
        ...dataFrameFromJSON(frame),
        refId: ref,
      }));
  } catch (error) {
    decodingError = message(error);
  }
  const queryData: PanelData | undefined =
    preview && node
      ? {
          state: preview.results[node.refId]?.error
            ? LoadingState.Error
            : LoadingState.Done,
          series: decoded[node.refId] || [],
          timeRange: sdkRange,
          ...(preview.results[node.refId]?.error
            ? { error: { message: preview.results[node.refId].error } }
            : {}),
        }
      : undefined;
  const add = (expression: boolean, incoming?: DataQuery) => {
    if (graph.data.length >= 32) return;
    const refId = nextAlertReference(graph),
      input = graph.data[0]?.refId || "A";
    const model: Record<string, any> = incoming
      ? { ...incoming }
      : expression
        ? {
            type: "math",
            expression: "$" + (input.includes(" ") ? `{${input}}` : input),
          }
        : { expr: "", instant: true };
    delete model.refId;
    const uid =
      incoming?.datasource?.uid || (expression ? "__expr__" : "metricspanel");
    if (!incoming) delete model.datasource;
    const added: AlertQueryNode = {
      refId,
      datasourceUid: uid,
      relativeTimeRange: { from: expression ? 0 : 60, to: 0 },
      model,
    };
    setSelected(graph.data.length);
    change({ ...graph, data: [...graph.data, added] });
  };
  const move = (offset: number) => {
    const data = [...graph.data];
    [data[selected], data[selected + offset]] = [
      data[selected + offset],
      data[selected],
    ];
    setSelected(selected + offset);
    change({ ...graph, data });
  };
  const condition = graph.record?.from || graph.condition;
  return (
    <div className="alert-graph-editor">
      <fieldset className="query-editor-fields" disabled={disabled}>
        <label>
          {t(graph.record ? "Recording input" : "Condition query")}
          <select
            aria-label={t("Condition query")}
            value={condition}
            onChange={(event) =>
              change({
                ...graph,
                condition: event.target.value,
                ...(graph.record
                  ? { record: { ...graph.record, from: event.target.value } }
                  : {}),
              })
            }
          >
            {!graph.data.some((item) => item.refId === condition) ? (
              <option value={condition}>
                {condition || t("Choose a condition query")}
              </option>
            ) : null}
            {graph.data.map((item, index) => (
              <option key={index} value={item.refId}>
                {item.refId}
              </option>
            ))}
          </select>
        </label>
        <div className="annotation-query-layout">
          <nav
            className="annotation-query-list"
            aria-label={t("Alert query list")}
          >
            {graph.data.map((item, index) => (
              <button
                key={index}
                type="button"
                aria-current={index === selected ? "page" : undefined}
                onClick={() => setSelected(index)}
              >
                {item.refId}
                <small>{alertQueryUID(item)}</small>
              </button>
            ))}
            <button
              type="button"
              disabled={graph.data.length >= 32}
              onClick={() => add(false)}
            >
              {t("Add query")}
            </button>
            <button
              type="button"
              disabled={graph.data.length >= 32}
              onClick={() => add(true)}
            >
              {t("Add expression")}
            </button>
          </nav>
          <div className="annotation-query-fields">
            {node && target ? (
              <>
                <div className="form-row">
                  <label>
                    {t("Query reference")}
                    <input
                      aria-label={t("Query reference")}
                      value={node.refId}
                      maxLength={100}
                      onChange={(event) =>
                        change(
                          withAlertTarget(graph, selected, {
                            ...target,
                            refId: event.target.value,
                          }),
                        )
                      }
                    />
                  </label>
                  <label>
                    {t("Datasource")}
                    <select
                      aria-label={t("Datasource")}
                      value={alertQueryUID(node)}
                      onChange={(event) => {
                        const source = sources.find(
                          (item) => item.uid === event.target.value,
                        );
                        const changed: GrafanaTarget = {
                          ...target,
                          datasource: {
                            uid: event.target.value,
                            type: source?.type,
                          },
                          ...(source?.type === "__expr__" && !node.model.type
                            ? { type: "math", expression: "" }
                            : {}),
                        };
                        change(withAlertTarget(graph, selected, changed));
                      }}
                    >
                      {sources.map((source) => (
                        <option
                          key={source.uid}
                          value={source.uid}
                          disabled={source.meta?.backend === false}
                        >
                          {t(source.name)}
                        </option>
                      ))}
                      {!sources.some(
                        (source) => source.uid === alertQueryUID(node),
                      ) ? (
                        <option value={alertQueryUID(node)}>
                          {alertQueryUID(node)}
                        </option>
                      ) : null}
                    </select>
                  </label>
                </div>
                {graph.data.some(
                  (item) =>
                    item.model.type === "sql" &&
                    alertQueryUID(item) === "__expr__",
                ) ? (
                  <p className="subtle">
                    {t("Update SQL table names after renaming input queries.")}
                  </p>
                ) : null}
                <div className="form-row">
                  <label>
                    {t("Lookback seconds")}
                    <input
                      type="number"
                      aria-label={t("Lookback seconds")}
                      min={0}
                      max={2678400}
                      step={1}
                      value={node.relativeTimeRange?.from || 0}
                      onChange={(event) =>
                        updateNode({
                          relativeTimeRange: {
                            ...node.relativeTimeRange,
                            from: Number(event.target.value),
                          },
                        })
                      }
                    />
                  </label>
                  <label>
                    {t("End offset seconds")}
                    <input
                      type="number"
                      aria-label={t("End offset seconds")}
                      min={0}
                      max={2678400}
                      step={1}
                      value={node.relativeTimeRange?.to || 0}
                      onChange={(event) =>
                        updateNode({
                          relativeTimeRange: {
                            ...node.relativeTimeRange,
                            to: Number(event.target.value),
                          },
                        })
                      }
                    />
                  </label>
                </div>
                {loadError ? (
                  <p role="alert" className="form-error">
                    {loadError}
                  </p>
                ) : loaded ? (
                  <DatasourceQueryEditor
                    loaded={loaded}
                    query={target}
                    queries={graph.data.map(alertEditorTarget)}
                    range={sdkRange}
                    data={queryData}
                    app={CoreApp.UnifiedAlerting}
                    editorKey={selected}
                    onChange={(next) =>
                      change(withAlertTarget(graph, selected, next))
                    }
                    onRunQuery={() => void run()}
                    onAddQuery={(next) => add(false, next)}
                  />
                ) : (
                  <p role="status">{t("Loading datasource…")}</p>
                )}
                <details>
                  <summary>{t("Query options")}</summary>
                  <div className="form-row">
                    <label>
                      {t("Query interval (ms)")}
                      <input
                        type="number"
                        aria-label={t("Query interval (ms)")}
                        min={0}
                        value={node.model.intervalMs ?? 1000}
                        onChange={(event) =>
                          patchModel({ intervalMs: Number(event.target.value) })
                        }
                      />
                    </label>
                    <label>
                      {t("Maximum data points")}
                      <input
                        type="number"
                        aria-label={t("Maximum data points")}
                        min={0}
                        max={1000000}
                        value={node.model.maxDataPoints ?? 2000}
                        onChange={(event) =>
                          patchModel({
                            maxDataPoints: Number(event.target.value),
                          })
                        }
                      />
                    </label>
                  </div>
                </details>
                <div className="query-actions">
                  <button
                    type="button"
                    disabled={selected === 0}
                    onClick={() => move(-1)}
                  >
                    {t("Move query up")}
                  </button>
                  <button
                    type="button"
                    disabled={selected === graph.data.length - 1}
                    onClick={() => move(1)}
                  >
                    {t("Move query down")}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      const data = graph.data.filter(
                        (_, index) => index !== selected,
                      );
                      setSelected(Math.max(0, selected - 1));
                      change({ ...graph, data });
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
        <div className="query-actions">
          <button type="button" onClick={running ? stop : () => void run()}>
            {t(running ? "Stop query" : "Preview alert queries")}
          </button>
        </div>
      </fieldset>
      <p className="subtle">
        {t(
          "Preview runs queries without saving, advancing alert timers or writing recordings.",
        )}
      </p>
      {error || decodingError ? (
        <p role="alert" className="form-error">
          {error || decodingError}
        </p>
      ) : null}
      {preview ? (
        <section
          className="annotation-query-preview"
          aria-label={t("Alert query preview")}
        >
          <h3>
            {t("Condition result")} · {preview.condition}
          </h3>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("Labels")}</th>
                  <th>{t("Value")}</th>
                  <th>{t("Condition result")}</th>
                </tr>
              </thead>
              <tbody>
                {preview.condition_values.map((value, index) => (
                  <tr key={index}>
                    <td>
                      {Object.entries(value.labels || {})
                        .map(([key, val]) => `${key}=${val}`)
                        .join(" · ") || "—"}
                    </td>
                    <td>
                      {value.value_text ||
                        (value.missing ? t("No data") : String(value.value))}
                    </td>
                    <td>
                      {value.satisfied == null
                        ? t("No data")
                        : t(value.satisfied ? "True" : "False")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {Object.entries(decoded).map(([ref, frames]) => (
            <div key={ref} className="query-result-frame">
              <h4>{ref}</h4>
              {preview.results[ref].error ? (
                <p className="form-error">{preview.results[ref].error}</p>
              ) : null}
              {frames.map((frame, index) => (
                <div key={index} className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        {frame.fields.map((field, i) => (
                          <th key={i}>{field.name}</th>
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
              ))}
            </div>
          ))}
          <details>
            <summary>{t("Frame JSON")}</summary>
            <pre className="query-preview-data">
              {JSON.stringify(preview, null, 2)}
            </pre>
          </details>
        </section>
      ) : null}
    </div>
  );
}
