import { Component, useEffect, useState, type ReactNode } from "react";
import { BrowserRouter } from "react-router";
import {
  CoreApp,
  createTheme,
  DataSourcePluginContextProvider,
  ThemeContext,
  type DataQuery,
  type DataSourceApi,
  type DataSourceInstanceSettings,
  type PanelData,
  type TimeRange,
} from "@grafana/data";
import { message, type InterpolationValues } from "../api";
import type { GrafanaTarget } from "../grafana/engine";
import {
  getPluginDatasource,
  sdkRuntime,
  setPluginVariables,
} from "../grafana/plugin-runtime";
import type { TimeSelection } from "../grafana/time-range";
import { t } from "../i18n";

export type EditorSource = {
  uid: string;
  name: string;
  type: string;
  meta?: { backend?: boolean };
};
export type LoadedEditorSource = {
  datasource: DataSourceApi;
  settings: DataSourceInstanceSettings;
};
const theme = createTheme({ colors: { mode: "dark" } });
const emptyValues: InterpolationValues = {};

export function useDatasourceEditor(
  uid: string,
  activeQuery: boolean,
  values: InterpolationValues = emptyValues,
  range: TimeSelection,
) {
  const [sources, setSources] = useState<EditorSource[]>([]);
  const [loaded, setLoaded] = useState<LoadedEditorSource | null>(null);
  const [loadError, setLoadError] = useState("");
  useEffect(() => {
    let active = true;
    void sdkRuntime()
      .then(async (runtime) => {
        await runtime.getDataSourceSrv().reload();
        if (active)
          setSources([
            ...runtime
              .getDataSourceSrv()
              .getList({ all: true })
              .filter((source) => source.type !== "__expr__"),
            {
              uid: "__expr__",
              name: "Expression",
              type: "__expr__",
              meta: { backend: true },
            },
          ]);
      })
      .catch((error) => {
        if (active) setLoadError(message(error));
      });
    return () => {
      active = false;
    };
  }, []);
  const valuesKey = JSON.stringify(values),
    rangeKey = JSON.stringify(range);
  useEffect(() => {
    let active = true;
    setLoaded(null);
    setLoadError("");
    if (!activeQuery) return;
    setPluginVariables(values, range);
    void getPluginDatasource(
      uid,
      Object.fromEntries(
        Object.entries(values).map(([name, value]) => [
          name,
          { value, text: value },
        ]),
      ),
    )
      .then(async (datasource) => {
        const runtime = await sdkRuntime(),
          settings = runtime
            .getDataSourceSrv()
            .getInstanceSettings(datasource.uid);
        if (!settings) throw new Error("Datasource settings not found");
        if (active) setLoaded({ datasource, settings });
      })
      .catch((error) => {
        if (active) setLoadError(message(error));
      });
    return () => {
      active = false;
    };
  }, [uid, activeQuery, valuesKey, rangeKey]);
  return { sources, loaded, loadError };
}

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
      <p role="alert" className="form-error">
        {this.state.error}
      </p>
    ) : (
      this.props.children
    );
  }
}

export function DatasourceQueryEditor({
  loaded,
  query,
  queries,
  range,
  data,
  app,
  editorKey,
  disabled = false,
  onChange,
  onRunQuery,
  onAddQuery,
}: {
  loaded: LoadedEditorSource;
  query: GrafanaTarget;
  queries: GrafanaTarget[];
  range: TimeRange;
  data?: PanelData;
  app: CoreApp;
  editorKey: string | number;
  disabled?: boolean;
  onChange: (query: GrafanaTarget) => void;
  onRunQuery: () => void;
  onAddQuery: (query: DataQuery) => void;
}) {
  const Editor = loaded.datasource.components?.QueryEditor;
  if (Editor)
    return (
      <EditorBoundary key={`${loaded.datasource.uid}-${editorKey}`}>
        <ThemeContext.Provider value={theme}>
          <BrowserRouter>
            <DataSourcePluginContextProvider instanceSettings={loaded.settings}>
              <fieldset className="query-editor-fields" disabled={disabled}>
                <Editor
                  datasource={loaded.datasource}
                  query={query as DataQuery}
                  queries={queries as DataQuery[]}
                  range={range}
                  data={data}
                  app={app}
                  onChange={(next) =>
                    onChange({
                      ...query,
                      ...next,
                      datasource:
                        next.datasource === null
                          ? undefined
                          : next.datasource || query.datasource,
                    })
                  }
                  onRunQuery={onRunQuery}
                  onAddQuery={(next) =>
                    onAddQuery({
                      ...next,
                      datasource: next.datasource || {
                        uid: loaded.datasource.uid,
                        type: loaded.datasource.type,
                      },
                    })
                  }
                />
              </fieldset>
            </DataSourcePluginContextProvider>
          </BrowserRouter>
        </ThemeContext.Provider>
      </EditorBoundary>
    );
  if (loaded.datasource.type !== "prometheus")
    return (
      <p className="subtle">
        {t("This datasource has no query editor. Use advanced JSON.")}
      </p>
    );
  const patch = (update: GrafanaTarget) => onChange({ ...query, ...update });
  const modeLabel =
    app === CoreApp.UnifiedAlerting ? "Data query mode" : "Query mode";
  return (
    <fieldset className="query-editor-fields" disabled={disabled}>
      <label>
        {t("PromQL expression")}
        <textarea
          rows={3}
          className="mono"
          aria-label={t("PromQL expression")}
          value={query.expr || ""}
          onChange={(event) => patch({ expr: event.target.value })}
        />
      </label>
      <div className="form-row">
        <label>
          {t("Legend")}
          <input
            aria-label={t("Legend")}
            value={query.legendFormat || ""}
            onChange={(event) => patch({ legendFormat: event.target.value })}
          />
        </label>
        <label>
          {t(modeLabel)}
          <select
            aria-label={t(modeLabel)}
            value={query.instant ? "instant" : "range"}
            onChange={(event) =>
              patch({
                instant: event.target.value === "instant",
                range: event.target.value === "range",
              })
            }
          >
            <option value="instant">{t("Instant")}</option>
            <option value="range">{t("Range")}</option>
          </select>
        </label>
      </div>
    </fieldset>
  );
}
