import {
  Component,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { BrowserRouter } from "react-router";
import {
  DataSourcePluginContextProvider,
  ThemeContext,
  createTheme,
  LoadingState,
  type AnnotationQuery,
  type DataSourceApi,
  type DataSourceInstanceSettings,
  type PanelData,
} from "@grafana/data";
import { from, map, timeout, type Subscription } from "rxjs";
import {
  api,
  dashboardUID,
  jsonBody,
  message,
  type Dashboard,
  type InterpolationValues,
} from "../api";
import { t } from "../i18n";
import { Dialog } from "./Dialog";
import {
  readAnnotationDrafts,
  withAnnotationQueries,
  annotationSource,
  nativeAnnotationParams,
  validateAnnotationConfig,
  type AnnotationDraft,
  type AnnotationConfig,
} from "../grafana/annotation-config";
import { annotationsChanged } from "../grafana/annotations";
import {
  runDatasourceAnnotationQuery,
  type AnnotationQueryResult,
} from "../grafana/annotation-query";
import { resolveTimeRange, type TimeSelection } from "../grafana/time-range";
import {
  getPluginDatasource,
  sdkRuntime,
  setPluginVariables,
} from "../grafana/plugin-runtime";

type Draft = AnnotationDraft & { key: string };
type Loaded = {
  datasource: DataSourceApi;
  settings: DataSourceInstanceSettings;
};
const theme = createTheme({ colors: { mode: "dark" } });
class EditorBoundary extends Component<
  { children: ReactNode },
  { error: string }
> {
  state = { error: "" };
  static getDerivedStateFromError(error: unknown) {
    return { error: message(error) };
  }
  render() {
    return this.state.error ? (
      <p className="form-error" role="alert">
        {this.state.error}
      </p>
    ) : (
      this.props.children
    );
  }
}

function QueryJSON({
  config,
  onChange,
  onValid,
}: {
  config: AnnotationConfig;
  onChange: (value: AnnotationConfig) => void;
  onValid: (valid: boolean) => void;
}) {
  const serialized = JSON.stringify(config, null, 2);
  const [text, setText] = useState(serialized),
    [error, setError] = useState("");
  useEffect(() => {
    setText(serialized);
    setError("");
    onValid(true);
  }, [serialized]);
  return (
    <details className="annotation-advanced">
      <summary>{t("Advanced query JSON")}</summary>
      <label>
        {t("Query JSON")}
        <textarea
          aria-label={t("Query JSON")}
          rows={9}
          value={text}
          spellCheck={false}
          onChange={(event) => {
            const value = event.target.value;
            setText(value);
            try {
              const parsed = JSON.parse(value);
              if (
                !parsed ||
                typeof parsed !== "object" ||
                Array.isArray(parsed)
              )
                throw new Error("Query JSON must be an object");
              validateAnnotationConfig(parsed);
              setError("");
              onValid(true);
              onChange(parsed);
            } catch (error) {
              setError(t(message(error)));
              onValid(false);
            }
          }}
        />
      </label>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </details>
  );
}

export default function AnnotationQueriesEditor({
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
  const baseVersion = useRef(dashboard.updated_at);
  const [drafts, setDrafts] = useState<Draft[]>(() =>
    readAnnotationDrafts(dashboard).map((draft) => ({
      ...draft,
      key: crypto.randomUUID(),
    })),
  );
  const [selected, select] = useState(drafts[0]?.key || "");
  const draft = drafts.find((item) => item.key === selected);
  const config = draft?.config;
  const current = useRef(config);
  current.current = config;
  const [sources, setSources] = useState<DataSourceInstanceSettings[]>([]),
    [loaded, setLoaded] = useState<Loaded | null>(null);
  const [loadError, setLoadError] = useState(""),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false),
    [validJSON, setValidJSON] = useState(true);
  const [previewActive, setPreviewActive] = useState(false);
  const [running, setRunning] = useState(false),
    [result, setResult] = useState<AnnotationQueryResult | null>(null),
    [panelData, setPanelData] = useState<PanelData>();
  const subscription = useRef<Subscription | null>(null),
    nativeController = useRef<AbortController | null>(null);
  const resolved = useMemo(
    () => resolveTimeRange(range),
    [JSON.stringify(range)],
  );
  const fixed = {
    from: resolved.start,
    to: resolved.end,
    timezone: resolved.timezone,
  };
  const source = annotationSource(config || {}, values, fixed);
  const scope = JSON.stringify([
    selected,
    source.uid,
    source.native,
    values,
    fixed,
  ]);
  const change = (next: AnnotationConfig) =>
    setDrafts((old) =>
      old.map((item) =>
        item.key === selected ? { ...item, config: next } : item,
      ),
    );
  const patch = (update: AnnotationConfig) =>
    config && change({ ...config, ...update });
  const target = source.native
    ? { ...config, ...config?.target }
    : config?.target || {};
  const patchTarget = (update: AnnotationConfig) =>
    patch({ target: { ...config?.target, ...update } });
  const stop = () => {
    setPreviewActive(false);
    subscription.current?.unsubscribe();
    subscription.current = null;
    nativeController.current?.abort();
    nativeController.current = null;
    setRunning(false);
  };
  useEffect(() => {
    let active = true;
    void sdkRuntime()
      .then(async (runtime) => {
        await runtime.getDataSourceSrv().reload();
        if (active) setSources(runtime.getDataSourceSrv().getList());
      })
      .catch((error) => {
        if (active) setLoadError(message(error));
      });
    return () => {
      active = false;
      subscription.current?.unsubscribe();
      nativeController.current?.abort();
    };
  }, []);
  useEffect(() => {
    let active = true;
    stop();
    setLoaded(null);
    setLoadError("");
    setResult(null);
    setPanelData(undefined);
    setError("");
    setValidJSON(true);
    if (!config || source.native) return;
    setPluginVariables(values, fixed);
    void getPluginDatasource(
      source.uid,
      Object.fromEntries(
        Object.entries(values).map(([name, value]) => [
          name,
          { value, text: value },
        ]),
      ),
    )
      .then(async (datasource) => {
        const runtime = await sdkRuntime();
        const settings = runtime
          .getDataSourceSrv()
          .getInstanceSettings(datasource.uid);
        if (!settings) throw new Error("Datasource settings not found");
        if (!active) return;
        setLoaded({ datasource, settings });
        const original = structuredClone(current.current || {});
        const candidate: AnnotationConfig = {
          ...datasource.annotations?.getDefaultQuery?.(),
          ...original,
        };
        if (
          candidate.datasource &&
          typeof candidate.datasource === "object" &&
          !candidate.datasource.type
        )
          candidate.datasource = {
            ...candidate.datasource,
            type: datasource.type,
          };
        const prepared = datasource.annotations?.prepareAnnotation
          ? datasource.annotations.prepareAnnotation(candidate)
          : typeof candidate.query === "string"
            ? {
                ...candidate,
                target: { refId: "Anno", query: candidate.query },
                mappings: {},
              }
            : candidate;
        if (prepared && JSON.stringify(prepared) !== JSON.stringify(original))
          setDrafts((old) =>
            old.map((item) =>
              item.key === selected ? { ...item, config: prepared } : item,
            ),
          );
      })
      .catch((error) => {
        if (active) setLoadError(message(error));
      });
    return () => {
      active = false;
      subscription.current?.unsubscribe();
      nativeController.current?.abort();
    };
  }, [scope]);

  const run = () => {
    if (!config) return;
    stop();
    setRunning(true);
    setPreviewActive(true);
    setError("");
    setResult(null);
    setPanelData({
      state: LoadingState.Loading,
      series: [],
      timeRange: resolved.sdk,
    });
    setPluginVariables(values, fixed);
    const controller = new AbortController();
    nativeController.current = controller;
    const raw = dashboard.grafana as Record<string, any> | undefined;
    const stream = source.native
      ? (() => {
          const params = nativeAnnotationParams(
            config,
            dashboard,
            values,
            fixed,
          );
          return from(
            params
              ? api<any[]>("/api/annotations?" + params, {
                  signal: controller.signal,
                })
              : Promise.resolve([]),
          ).pipe(map((events) => ({ events }) as AnnotationQueryResult));
        })()
      : loaded
        ? runDatasourceAnnotationQuery(
            loaded.datasource,
            config as AnnotationQuery,
            {
              range: resolved.sdk,
              timezone: resolved.timezone,
              variables: values,
              width: innerWidth,
              dashboard: {
                uid: dashboardUID(dashboard),
                title: dashboard.name,
                ...(raw?.dashboard || raw?.spec || raw || {}),
                timezone: resolved.timezone,
              },
            },
          )
        : null;
    if (!stream) {
      setPreviewActive(false);
      setRunning(false);
      setError(t("Datasource is still loading"));
      return;
    }
    subscription.current = stream
      .pipe(timeout({ first: 60000, each: 60000 }))
      .subscribe({
        next: (result) => {
          setRunning(false);
          setResult(result);
          setPanelData({
            state: result.error
              ? LoadingState.Error
              : result.state || LoadingState.Done,
            series: result.frames || [],
            timeRange: resolved.sdk,
            ...(result.error ? { error: { message: result.error } } : {}),
          });
        },
        error: (error) => {
          setPreviewActive(false);
          nativeController.current?.abort();
          setRunning(false);
          setError(
            error?.name === "TimeoutError"
              ? t("Annotation query timed out")
              : message(error),
          );
        },
        complete: () => {
          setPreviewActive(false);
          setRunning(false);
        },
      });
  };
  const Editor =
    loaded?.datasource.annotations?.QueryEditor ||
    loaded?.datasource.components?.QueryEditor;
  const rawRef =
    typeof config?.datasource === "string"
      ? config.datasource
      : config?.datasource?.uid || "";
  const selectValue = source.native ? "-- Grafana --" : rawRef;
  const knownSources = new Set([
    "-- Grafana --",
    "",
    ...sources.map((entry) => entry.uid),
    ...(dashboard.variables || [])
      .filter((v) => v.type === "datasource")
      .map((v) => `$${v.name}`),
  ]);
  return (
    <Dialog
      title={t("Annotation queries")}
      onClose={saving ? () => {} : onClose}
      style={{ width: 960 }}
    >
      <form
        className="annotation-query-editor"
        onSubmit={async (event) => {
          event.preventDefault();
          if (!validJSON) return;
          setSaving(true);
          setError("");
          stop();
          try {
            const latest = (await api<Dashboard[]>("/dashboards")).find(
              (item) => item.id === dashboard.id,
            );
            if (!latest || latest.updated_at !== baseVersion.current)
              throw new Error(
                "Dashboard changed. Close and reopen this editor.",
              );
            const updated = withAnnotationQueries(latest, drafts);
            await api(`/dashboards/${encodeURIComponent(dashboard.id)}`, {
              method: "PUT",
              body: jsonBody(updated),
            });
            annotationsChanged();
            await reload();
            onClose();
          } catch (error) {
            setError(t(message(error)));
          } finally {
            setSaving(false);
          }
        }}
      >
        <div className="annotation-query-layout">
          <div
            className="annotation-query-list"
            role="navigation"
            aria-label={t("Saved annotation queries")}
          >
            {drafts.map((item) => (
              <button
                type="button"
                key={item.key}
                aria-current={selected === item.key ? "page" : undefined}
                aria-label={`${t("Edit annotation query")} ${item.config.builtIn && item.config.name === "Annotations & Alerts" ? t("Annotations & Alerts") : item.config.name || t("Annotations")}`}
                onClick={() => select(item.key)}
              >
                <span
                  className="annotation-query-dot"
                  style={{
                    backgroundColor: item.config.iconColor || "#5ac8de",
                  }}
                />
                <span>
                  {item.config.builtIn &&
                  item.config.name === "Annotations & Alerts"
                    ? t("Annotations & Alerts")
                    : item.config.name || t("Annotations")}
                  <small>
                    {t(item.config.enable === false ? "Disabled" : "Enabled")}
                  </small>
                </span>
              </button>
            ))}
            <button
              type="button"
              disabled={drafts.length >= 32}
              onClick={() => {
                const key = crypto.randomUUID();
                setDrafts((old) => [
                  ...old,
                  {
                    key,
                    config: {
                      name: `${t("Annotation query")} ${old.length + 1}`,
                      enable: true,
                      iconColor: "#5ac8de",
                      datasource: { uid: "-- Grafana --", type: "grafana" },
                      target: {
                        type: "tags",
                        tags: [],
                        limit: 100,
                        refId: "Anno",
                      },
                    },
                  },
                ]);
                select(key);
              }}
            >
              <Plus size={16} />
              {t("Add annotation query")}
            </button>
          </div>
          <div className="annotation-query-fields" key={selected}>
            {config ? (
              <>
                <div className="form-row">
                  <label>
                    {t("Query name")}
                    <input
                      required
                      maxLength={256}
                      value={config.name || ""}
                      onChange={(event) => patch({ name: event.target.value })}
                    />
                  </label>
                  <label>
                    {t("Marker color")}
                    <input
                      value={config.iconColor || ""}
                      onChange={(event) =>
                        patch({ iconColor: event.target.value })
                      }
                    />
                  </label>
                </div>
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={config.enable !== false}
                    onChange={(event) =>
                      patch({ enable: event.target.checked })
                    }
                  />
                  {t("Enabled")}
                </label>
                <label>
                  {t("Annotation datasource")}
                  <select
                    aria-label={t("Annotation datasource")}
                    value={selectValue}
                    disabled={Boolean(config.builtIn)}
                    onChange={(event) => {
                      const uid = event.target.value,
                        type = sources.find((entry) => entry.uid === uid)?.type;
                      const next: AnnotationConfig = {
                        ...config,
                        datasource: {
                          uid,
                          type: uid === "-- Grafana --" ? "grafana" : type,
                        },
                        target: { refId: "Anno" },
                      };
                      delete next.query;
                      change(next);
                    }}
                  >
                    <option value="-- Grafana --">
                      {t("Stored annotations")}
                    </option>
                    <option value="">{t("Default datasource")}</option>
                    {sources.map((entry) => (
                      <option key={entry.uid} value={entry.uid}>
                        {entry.name}
                      </option>
                    ))}
                    {(dashboard.variables || [])
                      .filter((v) => v.type === "datasource")
                      .map((v) => (
                        <option
                          key={v.name}
                          value={`$${v.name}`}
                        >{`$${v.name}`}</option>
                      ))}
                    {!knownSources.has(selectValue) && (
                      <option value={selectValue}>{selectValue}</option>
                    )}
                  </select>
                </label>
                {source.native ? (
                  <>
                    <label>
                      {t("Annotation scope")}
                      <select
                        aria-label={t("Annotation scope")}
                        value={
                          config.builtIn
                            ? "dashboard"
                            : target.type || "dashboard"
                        }
                        disabled={Boolean(config.builtIn)}
                        onChange={(event) =>
                          patchTarget({ type: event.target.value })
                        }
                      >
                        <option value="dashboard">{t("This dashboard")}</option>
                        <option value="tags">{t("By tags")}</option>
                      </select>
                    </label>
                    {!config.builtIn && target.type === "tags" && (
                      <>
                        <label>
                          {t("Tags (comma separated)")}
                          <input
                            defaultValue={(target.tags || []).join(", ")}
                            onChange={(event) =>
                              patchTarget({
                                tags: event.target.value
                                  .split(",")
                                  .map((tag) => tag.trim())
                                  .filter(Boolean),
                              })
                            }
                          />
                        </label>
                        <label className="checkbox-label">
                          <input
                            type="checkbox"
                            checked={Boolean(target.matchAny)}
                            onChange={(event) =>
                              patchTarget({ matchAny: event.target.checked })
                            }
                          />
                          {t("Match any tag")}
                        </label>
                      </>
                    )}
                    <label>
                      {t("Event limit")}
                      <input
                        type="number"
                        min={1}
                        max={1000}
                        value={target.limit || 100}
                        onChange={(event) =>
                          patchTarget({ limit: Number(event.target.value) })
                        }
                      />
                    </label>
                  </>
                ) : (
                  <>
                    {loadError ? (
                      <p className="form-error" role="alert">
                        {loadError}
                      </p>
                    ) : loaded ? (
                      Editor ? (
                        <div className="annotation-plugin-editor">
                          <EditorBoundary key={loaded.datasource.uid}>
                            <ThemeContext.Provider value={theme}>
                              <BrowserRouter>
                                <DataSourcePluginContextProvider
                                  instanceSettings={loaded.settings}
                                >
                                  <Editor
                                    datasource={loaded.datasource}
                                    query={{
                                      ...loaded.datasource.annotations?.getDefaultQuery?.(),
                                      ...(config.target || { refId: "Anno" }),
                                    }}
                                    annotation={config as AnnotationQuery}
                                    range={resolved.sdk}
                                    data={panelData}
                                    onChange={(target) => patch({ target })}
                                    onAnnotationChange={(annotation) =>
                                      change(annotation)
                                    }
                                    onRunQuery={run}
                                  />
                                </DataSourcePluginContextProvider>
                              </BrowserRouter>
                            </ThemeContext.Provider>
                          </EditorBoundary>
                        </div>
                      ) : loaded.datasource.type === "prometheus" ? (
                        <label>
                          {t("PromQL expression")}
                          <textarea
                            aria-label={t("PromQL expression")}
                            rows={3}
                            value={config.target?.expr || ""}
                            onChange={(event) =>
                              patchTarget({ expr: event.target.value })
                            }
                          />
                        </label>
                      ) : (
                        <p className="subtle">
                          {t(
                            "This datasource has no query editor. Use advanced JSON.",
                          )}
                        </p>
                      )
                    ) : (
                      <p role="status">{t("Loading datasource…")}</p>
                    )}
                    {loaded &&
                      !loaded.datasource.annotations?.processEvents &&
                      !loaded.datasource.annotationQuery && (
                        <details className="annotation-mappings">
                          <summary>{t("Event field mappings")}</summary>
                          {[
                            "time",
                            "timeEnd",
                            "text",
                            "title",
                            "tags",
                            "id",
                            "panelId",
                            "prevState",
                            "newState",
                            "data",
                          ].map((key) => {
                            const mapping = config.mappings?.[key] || {};
                            return (
                              <div className="annotation-mapping-row" key={key}>
                                <label>
                                  {key}
                                  <select
                                    aria-label={`${t("Mapping source")} ${key}`}
                                    value={mapping.source || "field"}
                                    onChange={(event) =>
                                      patch({
                                        mappings: {
                                          ...config.mappings,
                                          [key]: {
                                            ...mapping,
                                            source: event.target.value,
                                          },
                                        },
                                      })
                                    }
                                  >
                                    <option value="field">{t("Field")}</option>
                                    <option value="text">
                                      {t("Constant text")}
                                    </option>
                                    <option value="skip">{t("Skip")}</option>
                                  </select>
                                </label>
                                <label>
                                  {t("Mapping value")}
                                  <input
                                    aria-label={`${t("Mapping value")} ${key}`}
                                    disabled={mapping.source === "skip"}
                                    value={mapping.value || ""}
                                    placeholder={key}
                                    onChange={(event) =>
                                      patch({
                                        mappings: {
                                          ...config.mappings,
                                          [key]: {
                                            ...mapping,
                                            value: event.target.value,
                                          },
                                        },
                                      })
                                    }
                                  />
                                </label>
                              </div>
                            );
                          })}
                        </details>
                      )}
                  </>
                )}
                {!!dashboard.panels.some((panel) => panel.config?.id) && (
                  <details>
                    <summary>{t("Panel filter")}</summary>
                    <label>
                      {t("Panel IDs (comma separated)")}
                      <input
                        key={selected}
                        defaultValue={(config.filter?.ids || []).join(", ")}
                        pattern="\s*([1-9][0-9]*\s*(,\s*[1-9][0-9]*\s*)*)?"
                        onChange={(event) => {
                          if (event.target.validity.valid)
                            patch({
                              filter: {
                                ...config.filter,
                                ids: event.target.value
                                  .split(",")
                                  .map(Number)
                                  .filter((id) => id > 0),
                              },
                            });
                        }}
                      />
                    </label>
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={Boolean(config.filter?.exclude)}
                        onChange={(event) =>
                          patch({
                            filter: {
                              ...config.filter,
                              exclude: event.target.checked,
                            },
                          })
                        }
                      />
                      {t("Exclude these panels")}
                    </label>
                  </details>
                )}
                <div className="annotation-query-actions">
                  <button
                    type="button"
                    onClick={run}
                    disabled={running || (!source.native && !loaded)}
                  >
                    {t(running ? "Running…" : "Test annotation query")}
                  </button>
                  {previewActive && (
                    <button type="button" onClick={stop}>
                      {t("Stop query")}
                    </button>
                  )}
                  <button
                    type="button"
                    className="icon-button"
                    aria-label={t("Move query up")}
                    disabled={drafts[0]?.key === selected}
                    onClick={() =>
                      setDrafts((old) => {
                        const next = [...old],
                          index = next.findIndex(
                            (item) => item.key === selected,
                          );
                        if (index <= 0) return old;
                        [next[index - 1], next[index]] = [
                          next[index],
                          next[index - 1],
                        ];
                        return next;
                      })
                    }
                  >
                    <ArrowUp size={16} />
                  </button>
                  <button
                    type="button"
                    className="icon-button"
                    aria-label={t("Move query down")}
                    disabled={drafts.at(-1)?.key === selected}
                    onClick={() =>
                      setDrafts((old) => {
                        const next = [...old],
                          index = next.findIndex(
                            (item) => item.key === selected,
                          );
                        if (index < 0 || index >= next.length - 1) return old;
                        [next[index + 1], next[index]] = [
                          next[index],
                          next[index + 1],
                        ];
                        return next;
                      })
                    }
                  >
                    <ArrowDown size={16} />
                  </button>
                  <button
                    type="button"
                    className="icon-button danger-hover"
                    aria-label={t("Delete annotation query")}
                    disabled={Boolean(config.builtIn)}
                    onClick={() => {
                      const remaining = drafts.filter(
                        (item) => item.key !== selected,
                      );
                      setDrafts(remaining);
                      select(remaining[0]?.key || "");
                    }}
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
                <QueryJSON
                  config={config}
                  onChange={change}
                  onValid={setValidJSON}
                />
              </>
            ) : (
              <p className="subtle">{t("No annotation queries")}</p>
            )}
            {result && (
              <div
                className="annotation-query-preview"
                role="region"
                aria-label={t("Annotation query result")}
              >
                <p role="status">
                  {result.events.length} {t("events found")}
                </p>
                {result.error && (
                  <p className="form-error" role="alert">
                    {result.error}
                  </p>
                )}
                <ul>
                  {result.events.slice(0, 20).map((event, index) => (
                    <li key={index}>
                      <time>
                        {new Date(Number(event.time)).toLocaleString()}
                      </time>
                      <span>{event.text || event.title}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
          </div>
        </div>
        <div className="form-actions">
          <button type="button" disabled={saving} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={saving || !validJSON}>
            {t(saving ? "Saving…" : "Save annotation queries")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
