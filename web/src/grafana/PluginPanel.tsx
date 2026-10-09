import {
  Component,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  createTheme,
  dateTime,
  EventBusSrv,
  LoadingState,
  PluginContextProvider,
  ThemeContext,
  type DataFrame,
  type PanelPlugin,
  type PanelProps,
  type FieldConfigSource,
} from "@grafana/data";

import { merge } from "lodash";
import { BrowserRouter } from "react-router";
import { loadPanelPlugin, setPluginVariables } from "./plugin-runtime";
import {
  rangeMilliseconds,
  message,
  type Panel,
  type InterpolationValues,
} from "../api";
import { interpolate } from "../api";
import { t } from "../i18n";

class PluginErrorBoundary extends Component<
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
const theme = createTheme({ colors: { mode: "dark" } });
export default function PluginPanel({
  panel,
  frames,
  values,
  range,
  tick,
  loading,
  queryError,
}: {
  panel: Panel;
  frames: DataFrame[];
  values: InterpolationValues;
  range: string;
  tick: number;
  loading: boolean;
  queryError: string;
}) {
  const [plugin, setPlugin] = useState<PanelPlugin | null>(null),
    [error, setError] = useState(""),
    [options, setOptions] = useState<Record<string, unknown>>({}),
    [fieldConfig, setFieldConfig] = useState<FieldConfigSource>(
      panel.config?.fieldConfig || { defaults: {}, overrides: [] },
    );
  const content = useRef<HTMLDivElement>(null),
    [size, setSize] = useState({ width: 0, height: 0 });
  useEffect(() => {
    let active = true;
    void loadPanelPlugin(panel.config?.type || panel.visualization || "")
      .then(async (loaded) => {
        let configured = panel.config?.options || {};
        const savedVersion = (panel.config as { pluginVersion?: string })
          ?.pluginVersion;
        if (
          loaded.onPanelMigration &&
          savedVersion !== loaded.meta.info.version
        ) {
          const migrated = await loaded.onPanelMigration({
            id: Number((panel.config as { id?: number })?.id || 0),
            type: loaded.meta.id,
            options: configured,
            fieldConfig,
            pluginVersion: savedVersion,
          });
          configured = { ...configured, ...migrated };
        }
        if (active) {
          setOptions(merge({}, loaded.defaults, configured));
          setPlugin(loaded);
          setError("");
        }
      })
      .catch((e) => {
        if (active) setError(message(e));
      });
    return () => {
      active = false;
    };
  }, [JSON.stringify(panel)]);
  useEffect(() => {
    const node = content.current;
    if (!node) return;
    const observer = new ResizeObserver(([entry]) =>
      setSize({
        width: entry.contentRect.width,
        height: entry.contentRect.height,
      }),
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  const eventBus = useMemo(() => new EventBusSrv(), []);
  useEffect(() => () => eventBus.removeAllListeners(), [eventBus]);
  const end = Date.now(),
    timeRange = {
      from: dateTime(end - rangeMilliseconds(range)),
      to: dateTime(end),
      raw: { from: "now-" + range, to: "now" },
    };
  setPluginVariables(values, range);
  const props: PanelProps = {
    id: Number((panel.config as { id?: number })?.id || 0),
    title: panel.title,
    options,
    fieldConfig,
    data: {
      series: frames,
      state: queryError
        ? LoadingState.Error
        : loading
          ? LoadingState.Loading
          : LoadingState.Done,
      timeRange,
      error: queryError ? { message: queryError } : undefined,
    },
    timeRange,
    timeZone: "browser",
    transparent: false,
    width: size.width,
    height: size.height,
    renderCounter: tick,
    eventBus,
    onOptionsChange: setOptions,
    onFieldConfigChange: setFieldConfig,
    replaceVariables: (text) => interpolate(text, values, range),
    onChangeTimeRange: () => {},
  };
  const Renderer = plugin?.panel;
  return (
    <section
      className="chart-panel grafana-external-panel"
      aria-label={panel.title}
    >
      <div className="panel-heading">
        <h2>{panel.title}</h2>
        <span className="subtle">
          {plugin?.meta.name || panel.config?.type}
        </span>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      <div ref={content} className="grafana-plugin-content">
        {!error &&
          (!Renderer ? (
            <p className="subtle">{t("Loading plugin…")}</p>
          ) : (
            size.width > 0 &&
            size.height > 0 && (
              <PluginErrorBoundary key={plugin?.meta.id}>
                <BrowserRouter>
                  <ThemeContext.Provider value={theme}>
                    <PluginContextProvider meta={plugin!.meta}>
                      <Renderer {...props} />
                    </PluginContextProvider>
                  </ThemeContext.Provider>
                </BrowserRouter>
              </PluginErrorBoundary>
            )
          ))}
      </div>
    </section>
  );
}
