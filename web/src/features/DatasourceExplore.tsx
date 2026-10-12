import { useEffect, useRef, useState } from "react";
import {
  CoreApp,
  LoadingState,
  type DataFrame,
  type DataQuery,
  type Field,
  type PanelData,
} from "@grafana/data";
import { Subject, type Subscription } from "rxjs";
import { Download, Play, Plus, Square, Trash2 } from "lucide-react";
import { message, ranges, type Panel } from "../api";
import { Chart } from "../components/Chart";
import {
  DatasourceQueryEditor,
  useDatasourceEditor,
} from "../components/DatasourceQueryEditor";
import {
  framesAsSeries,
  watchFrames,
  type FrameUpdate,
  type GrafanaTarget,
} from "../grafana/engine";
import { resolveTimeRange, type TimeSelection } from "../grafana/time-range";
import { getLocale, t } from "../i18n";
import { isSQLDatasource } from "../grafana/sql-types";
import "./DatasourceExplore.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
const values = {};
function defaultQuery(uid: string, type: string): GrafanaTarget {
  return {
    refId: "A",
    datasource: { uid, type },
    ...(isSQLDatasource(type)
      ? { rawSql: "SELECT 1 AS value", format: "table" }
      : type === "prometheus"
        ? { expr: "", instant: false, range: true }
        : type === "grafana"
          ? { queryType: "randomWalk", seriesCount: 1 }
          : {}),
  };
}
function nextReference(queries: GrafanaTarget[]) {
  const names = new Set(queries.map((query) => query.refId));
  for (let index = 0; ; index++) {
    let remaining = index,
      name = "";
    do {
      name = String.fromCharCode(65 + (remaining % 26)) + name;
      remaining = Math.floor(remaining / 26) - 1;
    } while (remaining >= 0);
    if (!names.has(name)) return name;
  }
}
function fieldValue(field: Field, index: number) {
  const value = field.values[index];
  if (value == null) return "—";
  if (typeof value === "object") return JSON.stringify(value);
  if (field.display) {
    const display = field.display(value);
    return `${display.prefix || ""}${display.text}${display.suffix || ""}`;
  }
  return String(value);
}
function FrameTable({ frame }: { frame: DataFrame }) {
  const [page, setPage] = useState(0);
  const count = Math.max(1, Math.ceil(frame.length / 100));
  const current = Math.min(page, count - 1);
  return (
    <div className="explore-result-frame">
      <div className="explore-frame-heading">
        <h3>
          {frame.refId || "A"}
          {frame.name ? ` · ${frame.name}` : ""}
        </h3>
        <span className="subtle">
          {frame.length} {text("rows", "行")}
        </span>
      </div>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              {frame.fields.map((field, index) => (
                <th key={index}>
                  {field.config.displayName ||
                    field.config.displayNameFromDS ||
                    field.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {Array.from(
              {
                length: Math.min(
                  100,
                  Math.max(0, frame.length - current * 100),
                ),
              },
              (_, index) => (
                <tr key={index}>
                  {frame.fields.map((field, fieldIndex) => (
                    <td key={fieldIndex}>
                      {fieldValue(field, current * 100 + index)}
                    </td>
                  ))}
                </tr>
              ),
            )}
          </tbody>
        </table>
      </div>
      {count > 1 && (
        <div className="explore-frame-pages">
          <button disabled={current === 0} onClick={() => setPage(current - 1)}>
            {text("Previous", "上一页")}
          </button>
          <span>
            {current + 1} / {count}
          </span>
          <button
            disabled={current + 1 >= count}
            onClick={() => setPage(current + 1)}
          >
            {text("Next", "下一页")}
          </button>
        </div>
      )}
    </div>
  );
}

export function DatasourceExplore({
  uid,
  type,
  range,
  onRange,
}: {
  uid: string;
  type: string;
  range: string;
  onRange: (range: string) => void;
}) {
  const [queries, setQueries] = useState<GrafanaTarget[]>(() => [
    defaultQuery(uid, type),
  ]);
  const [selected, setSelected] = useState("A");
  const [raw, setRaw] = useState(() => JSON.stringify(queries[0], null, 2));
  const [rawError, setRawError] = useState("");
  const [result, setResult] = useState<FrameUpdate | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const [view, setView] = useState<"table" | "graph">("table");
  const subscription = useRef<Subscription | null>(null);
  const generation = useRef(0);
  const query = queries.find((query) => query.refId === selected) || queries[0];
  const { loaded, loadError } = useDatasourceEditor(uid, true, values, range);
  const resolved = resolveTimeRange(range);
  const panelData: PanelData | undefined = result
    ? {
        state: result.error
          ? LoadingState.Error
          : result.loading
            ? LoadingState.Loading
            : result.streaming
              ? LoadingState.Streaming
              : LoadingState.Done,
        series: result.frames.filter((frame) => frame.refId === selected),
        timeRange: result.timeRange?.sdk || resolved.sdk,
        ...(result.error ? { error: { message: result.error } } : {}),
      }
    : undefined;
  function stop() {
    generation.current++;
    subscription.current?.unsubscribe();
    subscription.current = null;
    setRunning(false);
    setResult((previous) =>
      previous ? { ...previous, loading: false, streaming: false } : previous,
    );
  }
  useEffect(
    () => () => {
      generation.current++;
      subscription.current?.unsubscribe();
    },
    [],
  );
  useEffect(() => {
    stop();
    setResult(null);
    setError("");
  }, [range]);
  function change(next: GrafanaTarget, updateRaw = true) {
    stop();
    const changed = { ...next, refId: query.refId, datasource: { uid, type } };
    setQueries((current) =>
      current.map((item) => (item.refId === query.refId ? changed : item)),
    );
    setResult(null);
    setError("");
    if (updateRaw) {
      setRaw(JSON.stringify(changed, null, 2));
      setRawError("");
    }
  }
  function select(refId: string) {
    setSelected(refId);
    setRaw(
      JSON.stringify(
        queries.find((query) => query.refId === refId),
        null,
        2,
      ),
    );
    setRawError("");
  }
  function add(incoming?: DataQuery) {
    if (queries.length >= 32) return;
    stop();
    const next = {
      ...defaultQuery(uid, type),
      ...incoming,
      refId: nextReference(queries),
      datasource: { uid, type },
    };
    setQueries((current) => [...current, next]);
    setSelected(next.refId);
    setRaw(JSON.stringify(next, null, 2));
    setRawError("");
    setResult(null);
  }
  function run() {
    if (!loaded || rawError) return;
    stop();
    setResult(null);
    setError("");
    setRunning(true);
    const currentGeneration = generation.current;
    const panel: Panel = {
      id: "explore-source",
      title: "Query result",
      metric: "",
      aggregation: "last",
      unit: "",
      visualization: "table",
      config: { type: "table", targets: queries },
    };
    subscription.current = watchFrames(
      panel,
      values,
      range,
      new Subject(),
      { app: CoreApp.Explore },
    ).subscribe({
      next: (update) => {
        if (generation.current !== currentGeneration) return;
        setResult(update);
        setError(update.error);
        setRunning(update.loading || update.streaming);
        if (update.error || (!update.loading && !update.streaming)) {
          subscription.current?.unsubscribe();
          subscription.current = null;
        }
      },
      error: (reason) => {
        if (generation.current === currentGeneration) {
          setError(message(reason));
          setRunning(false);
        }
      },
      complete: () => {
        if (generation.current === currentGeneration) setRunning(false);
      },
    });
  }
  function exportResult() {
    if (!result) return;
    const serializable = result.frames.map((frame) => ({
      refId: frame.refId,
      name: frame.name,
      length: frame.length,
      fields: frame.fields.map(({ display: _, ...field }) => field),
      meta: frame.meta,
    }));
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(serializable, null, 2)], {
        type: "application/json",
      }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = "explore-result.json";
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return (
    <div className="configured-explore">
      <section
        className="query-builder"
        aria-label={text("Datasource query", "数据源查询")}
      >
        <div className="explore-query-toolbar">
          <label>
            {text("Query", "查询")}
            <select
              aria-label={text("Select query", "选择查询")}
              value={selected}
              onChange={(event) => select(event.target.value)}
            >
              {queries.map((query) => (
                <option key={query.refId}>{query.refId}</option>
              ))}
            </select>
          </label>
          <label>
            {t("Time range")}
            <select
              aria-label={t("Time range")}
              value={range}
              onChange={(event) => onRange(event.target.value)}
            >
              {ranges.map((range) => (
                <option key={range.value} value={range.value}>
                  {t(range.label)}
                </option>
              ))}
            </select>
          </label>
          <button
            onClick={() => add()}
            disabled={running || queries.length >= 32}
          >
            <Plus size={15} />
            {text("Add query", "添加查询")}
          </button>
          <button
            disabled={running || queries.length === 1}
            aria-label={text("Remove selected query", "删除当前查询")}
            onClick={() => {
              stop();
              const next = queries.filter((item) => item.refId !== selected);
              setQueries(next);
              setSelected(next[0].refId!);
              setRaw(JSON.stringify(next[0], null, 2));
              setRawError("");
              setResult(null);
            }}
          >
            <Trash2 size={15} />
          </button>
        </div>
        {loadError && (
          <p role="alert" className="form-error">
            {loadError}
          </p>
        )}
        {loaded ? (
          <DatasourceQueryEditor
            loaded={loaded}
            query={query}
            queries={queries}
            range={resolved.sdk}
            data={panelData}
            app={CoreApp.Explore}
            editorKey={selected}
            onChange={(next) => change(next)}
            onRunQuery={run}
            onAddQuery={(next) => add(next)}
            disabled={running}
          />
        ) : !loadError ? (
          <p role="status">{t("Loading datasource…")}</p>
        ) : null}
        <details
          open={
            !!rawError ||
            (!!loaded &&
              !loaded.datasource.components?.QueryEditor &&
              loaded.datasource.type !== "prometheus")
          }
        >
          <summary>{text("Advanced query JSON", "高级查询 JSON")}</summary>
          <label>
            {text("Query model", "查询模型")}
            <textarea
              aria-label={text("Query model", "查询模型")}
              className="mono"
              rows={8}
              value={raw}
              disabled={running}
              onChange={(event) => {
                setRaw(event.target.value);
                try {
                  const parsed = JSON.parse(event.target.value);
                  if (
                    !parsed ||
                    typeof parsed !== "object" ||
                    Array.isArray(parsed)
                  )
                    throw new Error(
                      text(
                        "Query must be a JSON object",
                        "查询必须是 JSON 对象",
                      ),
                    );
                  change(parsed, false);
                  setRawError("");
                } catch (reason) {
                  setRawError(message(reason));
                  stop();
                }
              }}
            />
          </label>
          {rawError && (
            <p role="alert" className="form-error">
              {rawError}
            </p>
          )}
        </details>
        <div className="explore-query-actions">
          <button
            className="primary"
            disabled={!loaded || !!rawError}
            onClick={running ? stop : run}
          >
            {running ? <Square size={16} /> : <Play size={16} />}
            {t(running ? "Stop query" : "Run query")}
          </button>
          <span className="subtle">
            {queries.length} {text("queries", "个查询")}
          </span>
        </div>
      </section>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {result && !result.loading && (
        <section
          className="list-panel explore-source-results"
          aria-label={t("Query result")}
        >
          <div className="section-heading">
            <h2>
              {t("Query result")}
              <span>
                {result.frames.length} {text("frames", "个数据帧")}
              </span>
            </h2>
            <div className="row-actions">
              <button
                aria-pressed={view === "table"}
                onClick={() => setView("table")}
              >
                {text("Table view", "表格视图")}
              </button>
              <button
                aria-pressed={view === "graph"}
                onClick={() => setView("graph")}
              >
                {text("Time series view", "时序视图")}
              </button>
              <button onClick={exportResult} disabled={!result.frames.length}>
                <Download size={15} />
                JSON
              </button>
            </div>
          </div>
          {view === "table" ? (
            result.frames.length ? (
              result.frames.map((frame, index) => (
                <FrameTable
                  key={`${generation.current}-${frame.refId}-${index}`}
                  frame={frame}
                />
              ))
            ) : (
              <p className="explore-source-empty">
                {text("The query returned no rows.", "查询未返回数据。")}
              </p>
            )
          ) : (
            <div className="explore-chart">
              <Chart
                panel={{
                  id: "explore-source-result",
                  title: "Query result",
                  metric: "",
                  aggregation: "last",
                  unit: "",
                }}
                range={
                  result.timeRange
                    ? ({
                        from: result.timeRange.start,
                        to: result.timeRange.end,
                        timezone: result.timeRange.timezone,
                      } as TimeSelection)
                    : range
                }
                tick={0}
                result={framesAsSeries(
                  result.frames,
                  result.timeRange
                    ? { from: result.timeRange.start, to: result.timeRange.end }
                    : range,
                )}
                resultError={error}
                streaming={result.streaming}
              />
            </div>
          )}
        </section>
      )}
    </div>
  );
}
