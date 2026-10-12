import { t as tr } from "../i18n";
import { lazy, Suspense, useState } from "react";
import { Plus, RefreshCw, Pencil } from "lucide-react";
import {
  api,
  ApiError,
  formatValue,
  jsonBody,
  message,
  parseLabels,
  aggregations,
  timeAgo,
  duration,
  type Dashboard,
  type Metric,
  type Panel,
  type Stats,
  type Target,
} from "../api";
import { Chart } from "../components/Chart";
import { Dialog } from "../components/Dialog";
import type { TimeSelection } from "../grafana/time-range";
import { TimeRangePicker } from "../components/TimeRangePicker";
import { RefreshPicker } from "../components/RefreshPicker";
import { AnnotationQueriesButton } from "../components/AnnotationQueriesButton";
import { DashboardSnapshotButton } from "../components/DashboardSnapshotButton";
import { DashboardHistoryButton } from "../components/DashboardHistoryButton";
import { panelPosition, placeAddedPanel } from "../grafana/layout";
import { nativePanelForSDK } from "../grafana/panel-queries";
import {
  DashboardVariableBar,
  DashboardVariablesButton,
  useDashboardVariables,
} from "../components/DashboardVariables";
import { DashboardVariablesEditor } from "../components/DashboardVariablesEditor";
import { layoutPanels } from "../grafana/layout";
import { queryValues } from "../grafana/variables";
import {
  DashboardLayoutButton,
  DashboardLayoutEditor,
} from "./DashboardLayoutEditor";
const GrafanaPanel = lazy(() => import("../grafana/GrafanaPanel"));
const PanelQueriesEditor = lazy(() =>
  import("../components/PanelQueriesEditor").then((module) => ({
    default: module.PanelQueriesEditor,
  })),
);
export function DashboardView({
  dashboard,
  stats,
  targets,
  metrics,
  tick,
  range,
  onRange,
  refresh,
  refreshChoice,
  onRefreshChoice,
  reload,
  notify,
}: {
  dashboard: Dashboard;
  stats: Stats | null;
  targets: Target[];
  metrics: Metric[];
  tick: number;
  range: TimeSelection;
  onRange: (v: TimeSelection) => void;
  refresh: () => void;
  refreshChoice: string;
  onRefreshChoice: (value: string) => void;
  reload: () => Promise<void>;
  notify: (s: string, error?: boolean) => void;
}) {
  const [editor, setEditor] = useState<{
    panel: Panel | "new";
    baseline: Dashboard;
  } | null>(null);
  const [removing, setRemoving] = useState<{
    panel: Panel;
    baseline: Dashboard;
  } | null>(null);
  const [removalError, setRemovalError] = useState(""),
    [removalBusy, setRemovalBusy] = useState(false);
  const [editingLayout, setEditingLayout] = useState(false);
  const [editingQueries, setEditingQueries] = useState(false);
  const [editingVariables, setEditingVariables] = useState(false);
  const variableState = useDashboardVariables(dashboard, range, tick);
  const options = Object.fromEntries(
    Object.entries(variableState.choices).map(([name, choices]) => [
      name,
      choices.map((choice) => choice.value),
    ]),
  );
  const entries = layoutPanels(
    dashboard.panels,
    variableState.values,
    options,
  ).map((entry) => ({
    ...entry,
    values: queryValues(
      entry.values,
      variableState.variables,
      variableState.choices,
    ),
  }));
  const openEditor = (panel?: Panel) => {
    const baseline = structuredClone(dashboard);
    setEditor({
      panel: panel
        ? baseline.panels.find((item) => item.id === panel.id)!
        : "new",
      baseline,
    });
  };
  async function save(baseline: Dashboard, body: unknown) {
    await api(`/dashboards/${encodeURIComponent(baseline.id)}/panels`, {
      method: "PATCH",
      headers: { "If-Match": String(baseline.updated_at) },
      body: jsonBody(body),
    });
    await reload();
  }
  const remove = (p: Panel) => {
    setRemovalError("");
    setRemoving({ panel: p, baseline: structuredClone(dashboard) });
  };
  const chart = (
    p: Panel,
    i: number,
    values = variableState.effective,
    sourcePanel = p,
  ) =>
    !!variableState.variables.length ||
    p.config?.targets !== undefined ||
    !["timeseries", "stat", "table", undefined].includes(p.visualization) ? (
      <Suspense
        fallback={
          <section className="chart-panel" aria-busy="true">
            {tr("Loading metrics…")}
          </section>
        }
      >
        <GrafanaPanel
          panel={nativePanelForSDK(p)}
          values={values}
          range={range}
          tick={tick}
          dashboard={dashboard}
          onRange={onRange}
          onEdit={() => openEditor(sourcePanel)}
          onRemove={() => remove(sourcePanel)}
        />
      </Suspense>
    ) : (
      <Chart
        key={p.id}
        panel={p}
        range={range}
        tick={tick}
        annotationDashboard={dashboard}
        mint={i === 2}
        onRange={onRange}
        onEdit={() => openEditor(sourcePanel)}
        onRemove={() => remove(sourcePanel)}
      />
    );
  return (
    <>
      <div className="page-heading dashboard-heading">
        <div>
          <h1>{tr(dashboard.name)}</h1>
          <p>
            {tr(
              dashboard.id === "system"
                ? "Live metrics from your local collector."
                : "Panels",
            )}
            {dashboard.id !== "system" ? `: ${dashboard.panels.length}` : ""}
          </p>
        </div>
        <div className="toolbar">
          <DashboardVariablesButton onClick={() => setEditingVariables(true)} />
          <button
            disabled={!dashboard.panels.length}
            onClick={() => setEditingQueries(true)}
          >
            <Pencil size={17} />
            {tr("Panel queries")}
          </button>
          <DashboardLayoutButton
            disabled={!dashboard.panels.length}
            onClick={() => setEditingLayout(true)}
          />
          <DashboardHistoryButton dashboard={dashboard} reload={reload} />
          <DashboardSnapshotButton
            dashboard={dashboard}
            panels={entries.map((entry) => ({
              ...entry,
              panel: variableState.variables.length
                ? nativePanelForSDK(entry.panel)
                : entry.panel,
            }))}
            range={range}
            reload={reload}
          />
          <AnnotationQueriesButton
            dashboard={dashboard}
            values={variableState.effective}
            range={range}
            reload={reload}
          />
          <TimeRangePicker value={range} onChange={onRange} />
          <RefreshPicker value={refreshChoice} onChange={onRefreshChoice} />
          <button
            className="icon-button outlined"
            onClick={refresh}
            aria-label={tr("Refresh metrics")}
          >
            <RefreshCw size={19} />
          </button>
          <button className="primary" onClick={() => openEditor()}>
            <Plus size={18} />
            {tr("Add panel")}
          </button>
        </div>
      </div>
      {dashboard.id === "system" ? (
        <div className="stats-band">
          <div>
            <span>{tr("Ingest rate")}</span>
            <strong>
              {stats ? formatValue(stats.ingest_rate) : "—"}{" "}
              <small>{tr("samples/s")}</small>
            </strong>
          </div>
          <div>
            <span>{tr("Active series")}</span>
            <strong>
              {stats?.series.toLocaleString() || "—"}{" "}
              <small>{tr("series")}</small>
            </strong>
          </div>
          <div>
            <span>{tr("Storage used")}</span>
            <strong>
              {stats ? formatValue(stats.storage_bytes, "bytes") : "—"}{" "}
              <small>
                {stats?.retention_days || 30}
                {tr("d retention")}
              </small>
            </strong>
          </div>
          <div>
            <span>{tr("Collectors online")}</span>
            <strong>
              {stats
                ? `${stats.collectors_online} / ${stats.collectors_total}`
                : "—"}{" "}
              <small>{tr("online")}</small>
            </strong>
          </div>
        </div>
      ) : null}
      <DashboardVariableBar state={variableState} />
      <div className="chart-grid dashboard-panel-grid">
        {variableState.ready &&
          entries.map(({ panel, sourcePanel, values, x, y, w, h }, index) => {
            return (
              <div
                className="dashboard-panel-cell"
                key={panel.id}
                data-panel-id={
                  sourcePanel.config?.repeat ? panel.id : sourcePanel.id
                }
                style={{
                  gridColumn: `${x + 1} / span ${w}`,
                  gridRow: `${y + 1} / span ${h}`,
                }}
              >
                {chart(panel, index, values, sourcePanel)}
              </div>
            );
          })}
      </div>
      {!dashboard.panels.length ? (
        <div className="empty-workspace">
          <h2>{tr("Add your first metric panel")}</h2>
          <p>{tr("Select a metric to start plotting live data.")}</p>
          <button onClick={() => openEditor()} className="primary">
            <Plus size={18} />
            {tr("Add panel")}
          </button>
        </div>
      ) : null}
      {dashboard.id === "system" ? (
        <section className="status-panel dashboard-collection-status">
          <div className="panel-heading">
            <h2>{tr("Collection status")}</h2>
          </div>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{tr("Name")}</th>
                  <th>{tr("Status")}</th>
                  <th>{tr("Last seen")}</th>
                  <th>{tr("Uptime")}</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td className="mono">{tr("metricspanel")}</td>
                  <td>
                    <span className="status healthy">
                      <i />
                      {tr("healthy")}
                    </span>
                  </td>
                  <td className="mono">{timeAgo(stats?.last_sample || 0)}</td>
                  <td className="mono">
                    {duration(Date.now() - (stats?.started_at || Date.now()))}
                  </td>
                </tr>
                {targets.map((t) => (
                  <tr key={t.id}>
                    <td title={t.url}>{t.name}</td>
                    <td>
                      <span
                        className={`status ${!t.enabled ? "muted" : t.last_error ? "unhealthy" : t.last_scrape ? "healthy" : "muted"}`}
                      >
                        <i />
                        {tr(
                          !t.enabled
                            ? "paused"
                            : t.last_error
                              ? "error"
                              : t.last_scrape
                                ? "healthy"
                                : "pending",
                        )}
                      </span>
                    </td>
                    <td className="mono">{timeAgo(t.last_scrape)}</td>
                    <td className="mono">
                      {t.samples}
                      {tr("samples")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}
      {editingLayout ? (
        <DashboardLayoutEditor
          dashboard={dashboard}
          reload={reload}
          onClose={() => setEditingLayout(false)}
        />
      ) : null}
      {editingVariables && (
        <DashboardVariablesEditor
          dashboard={dashboard}
          range={range}
          values={variableState.values}
          reload={reload}
          onClose={() => setEditingVariables(false)}
        />
      )}
      {editingQueries ? (
        <Suspense fallback={<p role="status">{tr("Loading datasource…")}</p>}>
          <PanelQueriesEditor
            dashboard={dashboard}
            values={variableState.effective}
            range={range}
            reload={reload}
            onClose={() => setEditingQueries(false)}
          />
        </Suspense>
      ) : null}
      {editor && (
        <PanelEditor
          panel={editor.panel === "new" ? undefined : editor.panel}
          metrics={metrics}
          onClose={() => setEditor(null)}
          onSave={async (p) => {
            const added = editor.panel === "new" || p.id !== editor.panel.id;
            const position = added
              ? panelPosition(
                  placeAddedPanel(p, editor.baseline.panels),
                  editor.baseline.panels.length,
                )
              : undefined;
            const {
              id,
              title,
              metric,
              expr,
              aggregation,
              unit,
              visualization,
              labels,
            } = p;
            await save(editor.baseline, {
              action:
                editor.panel !== "new" && p.id !== editor.panel.id
                  ? "duplicate"
                  : "save",
              sourceId: editor.panel === "new" ? undefined : editor.panel.id,
              panel: {
                id,
                title,
                metric,
                expr,
                aggregation,
                unit,
                visualization,
                labels,
              },
              position,
            });
            setEditor(null);
            notify(tr("Panel saved"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={tr("Remove panel")}
          onClose={() => {
            if (!removalBusy) setRemoving(null);
          }}
        >
          <p>
            {tr("Remove \u201C")}
            {removing.panel.title}
            {tr("\u201D from this dashboard?")}
          </p>
          {removalError && (
            <p className="form-error" role="alert">
              {tr(removalError)}
            </p>
          )}
          <div className="form-actions">
            <button disabled={removalBusy} onClick={() => setRemoving(null)}>
              {tr("Cancel")}
            </button>
            <button
              className="danger"
              disabled={removalBusy}
              onClick={async () => {
                setRemovalBusy(true);
                setRemovalError("");
                try {
                  await save(removing.baseline, {
                    action: "delete",
                    sourceId: removing.panel.id,
                  });
                  setRemoving(null);
                  notify(tr("Panel removed"));
                } catch (e) {
                  setRemovalError(
                    e instanceof ApiError && e.status === 409
                      ? tr("Dashboard changed. Close and reopen this editor.")
                      : message(e),
                  );
                } finally {
                  setRemovalBusy(false);
                }
              }}
            >
              {tr("Remove panel")}
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
function PanelEditor({
  panel,
  metrics,
  onClose,
  onSave,
}: {
  panel?: Panel;
  metrics: Metric[];
  onClose: () => void;
  onSave: (p: Panel) => Promise<void>;
}) {
  const [title, setTitle] = useState(panel?.title || "");
  const [metric, setMetric] = useState(panel?.metric || metrics[0]?.name || "");
  const [expr, setExpr] = useState(panel?.expr || "");
  const [aggregation, setAggregation] = useState(panel?.aggregation || "last");
  const [unit, setUnit] = useState(panel?.unit || "");
  const [visualization, setVisualization] = useState(
    panel?.visualization || "timeseries",
  );
  const [labels, setLabels] = useState(JSON.stringify(panel?.labels || {}));
  const [error, setError] = useState(""),
    [saving, setSaving] = useState(false);
  const configured = panel?.config?.targets !== undefined;
  return (
    <Dialog
      title={tr(panel ? "Edit panel" : "Add panel")}
      onClose={saving ? () => {} : onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setSaving(true);
          setError("");
          try {
            await onSave({
              ...panel,
              id:
                (e.nativeEvent as SubmitEvent).submitter?.getAttribute(
                  "value",
                ) === "duplicate"
                  ? crypto.randomUUID()
                  : panel?.id || crypto.randomUUID(),
              title,
              metric,
              expr,
              expressions: expr ? [expr] : undefined,
              aggregation,
              unit,
              visualization,
              labels: parseLabels(labels),
            });
          } catch (e) {
            setError(
              e instanceof ApiError && e.status === 409
                ? tr("Dashboard changed. Close and reopen this editor.")
                : message(e),
            );
            setSaving(false);
          }
        }}
      >
        <label>
          {tr("Panel title")}
          <input
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={tr("e.g. API request rate")}
            required
            maxLength={100}
          />
        </label>
        <label>
          {tr("Visualization")}
          <select
            aria-label={tr("Visualization")}
            value={visualization}
            onChange={(event) => setVisualization(event.target.value)}
          >
            <option value="timeseries">{tr("Time series")}</option>
            <option value="stat">{tr("Stat")}</option>
            <option value="table">{tr("Table")}</option>
            <option value="gauge">{tr("Gauge")}</option>
            <option value="bargauge">{tr("Bar gauge")}</option>
            {visualization &&
            !["timeseries", "stat", "table", "gauge", "bargauge"].includes(
              visualization,
            ) ? (
              <option value={visualization}>{visualization}</option>
            ) : null}
          </select>
        </label>
        {configured ? (
          <p className="subtle">
            {tr("Edit datasource queries using Panel queries.")}
          </p>
        ) : (
          <>
            <label>
              {tr("Metric")}
              <input
                list="metric-options"
                value={metric}
                onChange={(e) => setMetric(e.target.value)}
                required
                placeholder={tr("Select or enter a metric")}
              />
              <datalist id="metric-options">
                {metrics.map((m) => (
                  <option key={m.name} value={m.name} />
                ))}
              </datalist>
            </label>
            <label>
              {tr("PromQL expression")}
              <textarea
                className="mono"
                value={expr}
                onChange={(e) => setExpr(e.target.value)}
                placeholder="sum(rate(app_requests_total[5m]))"
                rows={2}
              />
            </label>
          </>
        )}
        <div className="form-row">
          {!configured && (
            <label>
              {tr("Aggregation")}
              <select
                value={aggregation}
                onChange={(e) => setAggregation(e.target.value)}
              >
                {aggregations.map((a) => (
                  <option key={a} value={a}>
                    {tr(a)}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            {tr("Unit")}
            <select value={unit} onChange={(e) => setUnit(e.target.value)}>
              <option value="">{tr("Auto / number")}</option>
              {["bytes", "seconds", "percent", "count", "ops"].map((u) => (
                <option key={u} value={u}>
                  {tr(u)}
                </option>
              ))}
            </select>
          </label>
        </div>
        {!configured && (
          <label>
            {tr("Label filters")}
            <small>{tr("JSON equality filters")}</small>
            <textarea
              className="mono"
              value={labels}
              onChange={(e) => setLabels(e.target.value)}
              rows={2}
            />
          </label>
        )}
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          {panel ? (
            <button value="duplicate" disabled={saving}>
              {tr("Duplicate panel")}
            </button>
          ) : null}
          <button type="button" disabled={saving} onClick={onClose}>
            {tr("Cancel")}
          </button>
          <button className="primary" disabled={saving}>
            {tr(saving ? "Saving…" : "Save panel")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
