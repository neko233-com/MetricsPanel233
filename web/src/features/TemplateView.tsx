import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { Download, RefreshCw } from "lucide-react";
import {
  download,
  message,
  type Dashboard,
  type Panel,
  type InterpolationValues,
  dashboardUID,
} from "../api";
import { t } from "../i18n";
import { queryValues, variableOptions } from "../grafana/variables";
import { layoutPanels } from "../grafana/layout";
import type { VariableValues } from "../grafana/engine";
import type { FrameUpdate } from "../grafana/engine";
import type { PluginExtensionPanelContext } from "@grafana/data";
import { LoadingState } from "@grafana/data";
import type { TimeSelection } from "../grafana/time-range";
import { TimeRangePicker } from "../components/TimeRangePicker";
import { RefreshPicker } from "../components/RefreshPicker";
const PanelExtensionActions = lazy(() =>
  import("../grafana/PanelExtensionActions").then((module) => ({
    default: module.PanelExtensionActions,
  })),
);
const GrafanaPanel = lazy(() => import("../grafana/GrafanaPanel"));
function selectedVariables(
  variables: Dashboard["variables"],
  params: URLSearchParams,
): VariableValues {
  return Object.fromEntries(
    (variables || []).map((variable) => {
      const selected = params.getAll(`var-${variable.name}`);
      if (selected.length)
        return [variable.name, variable.multi ? selected : selected[0]];
      const current =
        variable.current ||
        variable.options[0] ||
        (variable.include_all ? "$__all" : "");
      return [variable.name, variable.multi ? current.split("|") : current];
    }),
  );
}

function PanelCell({
  panel,
  values,
  range,
  tick,
  style,
  dashboard,
  annotationDashboard,
  onRange,
}: {
  panel: Panel;
  values: InterpolationValues;
  range: TimeSelection;
  onRange: (range: TimeSelection) => void;
  tick: number;
  style: CSSProperties;
  dashboard: PluginExtensionPanelContext["dashboard"];
  annotationDashboard: Dashboard;
}) {
  const ref = useRef<HTMLDivElement>(null),
    [visible, setVisible] = useState(false);
  const [update, setUpdate] = useState<FrameUpdate | null>(null);
  useEffect(() => {
    const cell = ref.current;
    let intersecting = false;
    const update = () =>
      setVisible(
        intersecting ||
          Boolean(cell?.querySelector("dialog[open]")) ||
          Boolean(cell?.contains(document.activeElement)),
      );
    const focusChanged = () => queueMicrotask(update);
    const observer = new IntersectionObserver(
      ([entry]) => {
        intersecting = entry.isIntersecting;
        update();
      },
      { rootMargin: "200px" },
    );
    if (ref.current) observer.observe(ref.current);
    cell?.addEventListener("focusout", focusChanged);
    return () => {
      observer.disconnect();
      cell?.removeEventListener("focusout", focusChanged);
    };
  }, []);
  return (
    <div
      ref={ref}
      className="grafana-cell"
      style={style}
      role="group"
      aria-label={panel.title}
    >
      {visible ? (
        <>
          <GrafanaPanel
            panel={panel}
            values={values}
            range={range}
            tick={tick}
            onUpdate={setUpdate}
            onRange={onRange}
            dashboard={annotationDashboard}
          />
          <PanelExtensionActions
            panel={panel}
            values={values}
            range={range}
            dashboard={dashboard}
            frames={update?.frames || []}
            queryRange={update?.timeRange}
            state={
              update?.error
                ? LoadingState.Error
                : update?.loading
                  ? LoadingState.Loading
                  : update?.streaming
                    ? LoadingState.Streaming
                    : LoadingState.Done
            }
          />
        </>
      ) : panel.visualization === "row" ? (
        <div className="grafana-row">
          <h2>{panel.title}</h2>
        </div>
      ) : (
        <section
          className="chart-panel"
          aria-label={panel.title}
          aria-busy="true"
        >
          <div className="panel-heading">
            <h2>{panel.title}</h2>
          </div>
        </section>
      )}
    </div>
  );
}

export function TemplateView({
  dashboard,
  range,
  onRange,
  tick,
  refresh,
  refreshChoice,
  refreshOptions,
  onRefreshChoice,
}: {
  dashboard: Dashboard;
  range: TimeSelection;
  onRange: (v: TimeSelection) => void;
  tick: number;
  refresh: () => void;
  refreshChoice: string;
  refreshOptions: string[];
  onRefreshChoice: (value: string) => void;
}) {
  const variables = dashboard.variables || [];
  const [values, setValues] = useState<VariableValues>(() =>
    selectedVariables(variables, new URLSearchParams(location.search)),
  );
  useEffect(() => {
    const restore = () =>
      setValues(
        selectedVariables(variables, new URLSearchParams(location.search)),
      );
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [dashboard.id, JSON.stringify(variables)]);
  const [options, setOptions] = useState<Record<string, string[]>>({}),
    [error, setError] = useState("");
  const key = JSON.stringify(values),
    effective = useMemo(
      () => queryValues(values, variables),
      [key, dashboard.id],
    );
  useEffect(() => {
    const params = new URLSearchParams(location.search);
    for (const [name, value] of Object.entries(values)) {
      params.delete(`var-${name}`);
      for (const choice of Array.isArray(value) ? value : [value])
        params.append(`var-${name}`, choice);
    }
    history.replaceState(
      history.state,
      "",
      `${location.pathname}?${params}${location.hash}`,
    );
  }, [key]);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all(
      variables.map(
        async (v) =>
          [
            v.name,
            await variableOptions(v, effective, range, controller.signal),
          ] as const,
      ),
    )
      .then((entries) => {
        setOptions(Object.fromEntries(entries));
        setError("");
        setValues((old) => {
          const next = { ...old };
          let changed = false;
          for (const [name, choices] of entries)
            if (
              (!next[name] ||
                (Array.isArray(next[name]) && !next[name].length)) &&
              choices.length
            ) {
              next[name] = variables.find((v) => v.name === name)?.multi
                ? [choices[0]]
                : choices[0];
              changed = true;
            }
          return changed ? next : old;
        });
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, [dashboard.id, key, range]);
  const panels = useMemo(
    () => layoutPanels(dashboard.panels, values, options),
    [dashboard.panels, key, options],
  );
  const extensionDashboard = useMemo(() => {
    const original = dashboard.grafana as
      | {
          tags?: unknown;
          dashboard?: { tags?: unknown };
          spec?: { tags?: unknown };
        }
      | undefined;
    const tags =
      original?.dashboard?.tags ?? original?.spec?.tags ?? original?.tags;
    return {
      uid: dashboardUID(dashboard),
      title: dashboard.name,
      tags: Array.isArray(tags)
        ? tags.filter((tag): tag is string => typeof tag === "string")
        : [],
    };
  }, [dashboard]);
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{dashboard.name}</h1>
          <p>{t("Grafana template")} · PromQL</p>
        </div>
        <div className="toolbar">
          <TimeRangePicker value={range} onChange={onRange} />
          <button
            className="icon-button outlined"
            aria-label={t("Refresh metrics")}
            onClick={refresh}
          >
            <RefreshCw size={18} />
          </button>
          <RefreshPicker
            value={refreshChoice}
            options={refreshOptions}
            onChange={onRefreshChoice}
          />
          <button
            onClick={() =>
              download(`${dashboard.id}-grafana.json`, dashboard.grafana)
            }
          >
            <Download size={17} />
            {t("Export JSON")}
          </button>
        </div>
      </div>
      {!!variables.length && (
        <div className="template-variables">
          {variables
            .filter(
              (v) =>
                v.type !== "constant" &&
                v.type !== "datasource" &&
                v.config?.hide !== 2 &&
                v.config?.hide !== "hideVariable",
            )
            .map((v) => (
              <label key={v.name}>
                {v.config?.label || v.name}
                {v.type === "textbox" ? (
                  <input
                    aria-label={v.name}
                    value={String(values[v.name] || "")}
                    onChange={(e) =>
                      setValues((old) => ({ ...old, [v.name]: e.target.value }))
                    }
                  />
                ) : (
                  <select
                    aria-label={v.name}
                    multiple={v.multi}
                    value={
                      v.multi
                        ? Array.isArray(values[v.name])
                          ? values[v.name]
                          : [String(values[v.name] || "")]
                        : String(values[v.name] || "")
                    }
                    onChange={(e) =>
                      setValues((old) => ({
                        ...old,
                        [v.name]: v.multi
                          ? Array.from(e.target.selectedOptions).map(
                              (o) => o.value,
                            )
                          : e.target.value,
                      }))
                    }
                  >
                    {v.include_all && (
                      <option value="$__all">{t("All")}</option>
                    )}
                    {(options[v.name] || v.options).map((value) => (
                      <option key={value} value={value}>
                        {v.config?.options?.find((o) => o.value === value)
                          ?.text || value}
                      </option>
                    ))}
                  </select>
                )}
              </label>
            ))}
        </div>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="grafana-grid">
        <Suspense fallback={<p>{t("Loading metrics…")}</p>}>
          {panels.map(({ panel, values: scoped, x, y, w, h }) => (
            <PanelCell
              key={panel.id}
              style={{
                gridColumn: `${x + 1} / span ${w}`,
                gridRow: `${y + 1} / span ${h}`,
              }}
              panel={panel}
              values={queryValues(scoped, variables)}
              range={range}
              onRange={onRange}
              tick={tick}
              dashboard={extensionDashboard}
              annotationDashboard={dashboard}
            />
          ))}
        </Suspense>
      </div>
    </>
  );
}
