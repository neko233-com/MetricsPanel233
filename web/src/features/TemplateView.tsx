import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { Download, RefreshCw, Pencil } from "lucide-react";
import {
  downloadRaw,
  message,
  type Dashboard,
  type Panel,
  type InterpolationValues,
  dashboardUID,
} from "../api";
import { t, getLocale } from "../i18n";
import { queryValues } from "../grafana/variables";
import {
  DashboardVariableBar,
  DashboardVariablesButton,
  useDashboardVariables,
} from "../components/DashboardVariables";
import { DashboardVariablesEditor } from "../components/DashboardVariablesEditor";
import { layoutPanels } from "../grafana/layout";
import { DashboardSnapshotButton } from "../components/DashboardSnapshotButton";
import { DashboardHistoryButton } from "../components/DashboardHistoryButton";
import {
  DashboardLayoutButton,
  DashboardLayoutEditor,
} from "./DashboardLayoutEditor";
import type { FrameUpdate } from "../grafana/engine";
import type { PluginExtensionPanelContext } from "@grafana/data";
import { LoadingState } from "@grafana/data";
import type { TimeSelection } from "../grafana/time-range";
import { TimeRangePicker } from "../components/TimeRangePicker";
import { RefreshPicker } from "../components/RefreshPicker";
import { AnnotationQueriesButton } from "../components/AnnotationQueriesButton";
const PanelExtensionActions = lazy(() =>
  import("../grafana/PanelExtensionActions").then((module) => ({
    default: module.PanelExtensionActions,
  })),
);
const GrafanaPanel = lazy(() => import("../grafana/GrafanaPanel"));
const PanelQueriesEditor = lazy(() =>
  import("../components/PanelQueriesEditor").then((module) => ({
    default: module.PanelQueriesEditor,
  })),
);
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
  reload,
}: {
  dashboard: Dashboard;
  range: TimeSelection;
  onRange: (v: TimeSelection) => void;
  tick: number;
  refresh: () => void;
  refreshChoice: string;
  refreshOptions: string[];
  onRefreshChoice: (value: string) => void;
  reload: () => Promise<void>;
}) {
  const variableState = useDashboardVariables(dashboard, range, tick);
  const { variables, values, effective, error } = variableState;
  const options = Object.fromEntries(
    Object.entries(variableState.choices).map(([name, choices]) => [
      name,
      choices.map((choice) => choice.value),
    ]),
  );
  const key = JSON.stringify(values);
  const [editingVariables, setEditingVariables] = useState(false);
  const [editingQueries, setEditingQueries] = useState(false);
  const [editingLayout, setEditingLayout] = useState(false);
  const [exporting, setExporting] = useState(false),
    [exportError, setExportError] = useState("");
  const panels = useMemo(
    () => layoutPanels(dashboard.panels, values, options),
    [dashboard.panels, key, JSON.stringify(options)],
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
  const snapshot = (
    dashboard.grafana as { snapshot?: { created?: number } } | undefined
  )?.snapshot;
  return (
    <>
      <div className="page-heading dashboard-heading">
        <div>
          <h1>{dashboard.name}</h1>
          <p>
            {snapshot?.created
              ? `${t("Snapshot")} · ${new Date(snapshot.created).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en-US")}`
              : `${t("Panels")}: ${dashboard.panels.length}`}
          </p>
        </div>
        <div className="toolbar">
          <DashboardVariablesButton onClick={() => setEditingVariables(true)} />
          <DashboardLayoutButton
            disabled={!dashboard.panels.length}
            onClick={() => setEditingLayout(true)}
          />
          <DashboardHistoryButton dashboard={dashboard} reload={reload} />
          <DashboardSnapshotButton
            dashboard={dashboard}
            panels={panels.map((entry) => ({
              ...entry,
              values: queryValues(
                entry.values,
                variables,
                variableState.choices,
              ),
            }))}
            range={range}
            reload={reload}
          />
          <button onClick={() => setEditingQueries(true)}>
            <Pencil size={17} />
            {t("Panel queries")}
          </button>
          <AnnotationQueriesButton
            dashboard={dashboard}
            values={effective}
            range={range}
            reload={reload}
          />
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
            disabled={exporting}
            onClick={async () => {
              setExporting(true);
              setExportError("");
              try {
                await downloadRaw(
                  `${dashboard.id}-grafana.json`,
                  `/dashboard-exports/${encodeURIComponent(dashboard.id)}?format=grafana`,
                );
              } catch (e) {
                setExportError(message(e));
              } finally {
                setExporting(false);
              }
            }}
          >
            <Download size={17} />
            {t("Export JSON")}
          </button>
        </div>
      </div>
      <DashboardVariableBar state={variableState} />
      {(exportError || error) && (
        <p className="form-error" role="alert">
          {exportError || error}
        </p>
      )}
      <div className="grafana-grid">
        <Suspense fallback={<p>{t("Loading metrics…")}</p>}>
          {variableState.ready &&
            panels.map(({ panel, values: scoped, x, y, w, h }) => (
              <PanelCell
                key={panel.id}
                style={{
                  gridColumn: `${x + 1} / span ${w}`,
                  gridRow: `${y + 1} / span ${h}`,
                }}
                panel={panel}
                values={queryValues(scoped, variables, variableState.choices)}
                range={range}
                onRange={onRange}
                tick={tick}
                dashboard={extensionDashboard}
                annotationDashboard={dashboard}
              />
            ))}
        </Suspense>
      </div>
      {editingVariables && (
        <DashboardVariablesEditor
          dashboard={dashboard}
          range={range}
          values={values}
          reload={reload}
          onClose={() => setEditingVariables(false)}
        />
      )}
      {editingQueries ? (
        <Suspense fallback={<p role="status">{t("Loading…")}</p>}>
          <PanelQueriesEditor
            dashboard={dashboard}
            values={effective}
            range={range}
            reload={reload}
            onClose={() => setEditingQueries(false)}
          />
        </Suspense>
      ) : null}
      {editingLayout ? (
        <DashboardLayoutEditor
          dashboard={dashboard}
          reload={reload}
          onClose={() => setEditingLayout(false)}
        />
      ) : null}
    </>
  );
}
