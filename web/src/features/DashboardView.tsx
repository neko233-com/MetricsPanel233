import { t as tr } from "../i18n";
import { useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import {
  api,
  formatValue,
  jsonBody,
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
  const [editor, setEditor] = useState<Panel | "new" | null>(null);
  const [removing, setRemoving] = useState<Panel | null>(null);
  async function save(panels: Panel[]) {
    await api(`/dashboards/${encodeURIComponent(dashboard.id)}`, {
      method: "PUT",
      body: jsonBody({ ...dashboard, panels }),
    });
    await reload();
  }
  const chart = (p: Panel, i: number) => (
    <Chart
      key={p.id}
      panel={p}
      range={range}
      tick={tick}
      annotationDashboard={dashboard}
      mint={i === 2}
      onRange={onRange}
      onEdit={() => setEditor(p)}
      onRemove={() => setRemoving(p)}
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
          <DashboardSnapshotButton
            dashboard={dashboard}
            range={range}
            reload={reload}
          />
          <AnnotationQueriesButton
            dashboard={dashboard}
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
          <button className="primary" onClick={() => setEditor("new")}>
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
      <div className="chart-grid">
        {(dashboard.id === "system"
          ? dashboard.panels.slice(0, 2)
          : dashboard.panels
        ).map(chart)}
      </div>
      {dashboard.id === "system" || !dashboard.panels.length ? (
        <div className="secondary-grid">
          <div>
            {dashboard.panels[2] ? (
              chart(dashboard.panels[2], 2)
            ) : (
              <div className="empty-workspace">
                <h2>{tr("Add your first metric panel")}</h2>
                <p>{tr("Select a metric to start plotting live data.")}</p>
                <button onClick={() => setEditor("new")} className="primary">
                  <Plus size={18} />
                  {tr("Add panel")}
                </button>
              </div>
            )}
          </div>
          {dashboard.id === "system" ? (
            <section className="status-panel">
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
                      <td className="mono">
                        {timeAgo(stats?.last_sample || 0)}
                      </td>
                      <td className="mono">
                        {duration(
                          Date.now() - (stats?.started_at || Date.now()),
                        )}
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
        </div>
      ) : null}
      {dashboard.id === "system" && dashboard.panels.length > 3 && (
        <div className="chart-grid extra-charts">
          {dashboard.panels.slice(3).map((p, i) => chart(p, i + 3))}
        </div>
      )}
      {editor && (
        <PanelEditor
          panel={editor === "new" ? undefined : editor}
          metrics={metrics}
          onClose={() => setEditor(null)}
          onSave={async (p) => {
            await save(
              editor === "new" || p.id !== editor.id
                ? [...dashboard.panels, p]
                : dashboard.panels.map((item) => (item.id === p.id ? p : item)),
            );
            setEditor(null);
            notify(tr("Panel saved"));
          }}
        />
      )}
      {removing && (
        <Dialog title={tr("Remove panel")} onClose={() => setRemoving(null)}>
          <p>
            {tr("Remove \u201C")}
            {removing.title}
            {tr("\u201D from this dashboard?")}
          </p>
          <div className="form-actions">
            <button onClick={() => setRemoving(null)}>{tr("Cancel")}</button>
            <button
              className="danger"
              onClick={async () => {
                try {
                  await save(
                    dashboard.panels.filter((p) => p.id !== removing.id),
                  );
                  setRemoving(null);
                  notify(tr("Panel removed"));
                } catch (e) {
                  notify(String(e), true);
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
  return (
    <Dialog title={tr(panel ? "Edit panel" : "Add panel")} onClose={onClose}>
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
            setError(String(e));
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
          </select>
        </label>
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
        <div className="form-row">
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
          <button type="button" onClick={onClose}>
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
