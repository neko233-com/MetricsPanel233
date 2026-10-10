import { useEffect, useState } from "react";
import {
  Bell,
  ChevronDown,
  ChevronRight,
  History,
  Pause,
  Pencil,
  Play,
  Plus,
  Trash2,
} from "lucide-react";
import {
  api,
  jsonBody,
  message,
  timeAgo,
  type Labels,
  type Metric,
} from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";

type Instance = {
  key: string;
  labels: Labels;
  state: string;
  value: number | null;
  value_text?: string;
  active_at: number;
  firing_at: number;
  recovering_at: number;
  reason?: string;
};
type Rule = {
  uid: string;
  title: string;
  expr: string;
  execution?: "promql" | "grafana";
  condition: string;
  interval_seconds: number;
  for_seconds: number;
  keep_firing_for_seconds: number;
  no_data_state: string;
  error_state: string;
  missing_series_evaluations: number;
  labels: Labels;
  annotations: Labels;
  paused: boolean;
  record?: string;
  folder_uid: string;
  group: string;
  version: number;
  updated_at: number;
  grafana?: unknown;
};
type RuleView = Rule & {
  runtime: {
    last_evaluation: number;
    duration_ms: number;
    health: string;
    error?: string;
    instances: Instance[];
  };
};
type Event = {
  id: number;
  uid: string;
  key: string;
  labels: Labels;
  from: string;
  to: string;
  timestamp: number;
  reason?: string;
};
const firing = (s: string) =>
  ["Firing", "Recovering", "NoData", "Error"].includes(s);
const stateText = (state: string) =>
  state === "Pending" && getLocale() === "zh" ? t("Waiting to fire") : t(state);
const stateClass = (state: string) =>
  firing(state)
    ? "unhealthy"
    : state === "Pending"
      ? "alert-pending"
      : state === "Normal"
        ? "healthy"
        : "muted";
const labelText = (labels: Labels) =>
  Object.entries(labels)
    .filter(([key]) => key !== "alertname")
    .map(([key, value]) => `${key}=${value}`)
    .join(" · ") || t("No labels");
const dateText = (ts: number) =>
  new Date(ts).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

export function Alerts({
  tick,
  metrics,
  notify,
}: {
  tick: number;
  metrics: Metric[];
  notify: (text: string, error?: boolean) => void;
}) {
  const [rules, setRules] = useState<RuleView[]>([]),
    [history, setHistory] = useState<Event[]>([]),
    [error, setError] = useState(""),
    [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<RuleView | "new" | null>(null),
    [removing, setRemoving] = useState<RuleView | null>(null),
    [expanded, setExpanded] = useState<string | null>(null),
    [busy, setBusy] = useState<string | null>(null),
    [filter, setFilter] = useState("all"),
    [loaded, setLoaded] = useState(false);
  const reload = () => setRevision((v) => v + 1);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      api<RuleView[]>("/alerts/rules", { signal: controller.signal }),
      api<Event[]>("/alerts/history?limit=20", { signal: controller.signal }),
    ])
      .then(([rs, es]) => {
        setRules(rs);
        setHistory(es);
        setError("");
        setLoaded(true);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, [tick, revision]);
  async function act(rule: RuleView, action: "pause" | "evaluate") {
    setBusy(rule.uid);
    try {
      if (action === "evaluate")
        await api(`/alerts/rules/${encodeURIComponent(rule.uid)}/evaluate`, {
          method: "POST",
        });
      else {
        const { runtime: _, ...config } = rule;
        await api(`/alerts/rules/${encodeURIComponent(rule.uid)}`, {
          method: "PUT",
          body: jsonBody({ ...config, paused: !rule.paused }),
        });
      }
      reload();
      notify(
        t(
          action === "evaluate"
            ? "Evaluation completed"
            : rule.paused
              ? "Rule resumed"
              : "Rule paused",
        ),
      );
    } catch (e) {
      notify(message(e), true);
      reload();
    } finally {
      setBusy(null);
    }
  }
  const counts = {
    firing: rules.reduce(
      (n, r) => n + r.runtime.instances.filter((v) => firing(v.state)).length,
      0,
    ),
    pending: rules.reduce(
      (n, r) =>
        n + r.runtime.instances.filter((v) => v.state === "Pending").length,
      0,
    ),
    paused: rules.filter((r) => r.paused).length,
  };
  const shown = rules.filter(
    (r) =>
      filter === "all" ||
      (filter === "paused" && r.paused) ||
      (filter === "firing" && r.runtime.instances.some((v) => firing(v.state))),
  );
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{t("Alerts")}</h1>
          <p>
            {t(
              "Turn metrics into conditions. State and timers survive restarts.",
            )}
          </p>
        </div>
        <button className="primary" onClick={() => setEditor("new")}>
          <Plus size={18} />
          {t("Create alert")}
        </button>
      </div>
      <div className="alert-summary">
        {[
          { label: "Rules", value: rules.length, style: "" },
          {
            label: "Firing instances",
            value: counts.firing,
            style: counts.firing ? "danger" : "",
          },
          {
            label: "Pending instances",
            value: counts.pending,
            style: "warning",
          },
          { label: "Paused rules", value: counts.paused, style: "" },
        ].map((v) => (
          <div key={v.label} className={`alert-summary-card ${v.style}`}>
            <span>{t(v.label)}</span>
            <strong>{v.value}</strong>
          </div>
        ))}
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <section className="list-panel alert-rules">
        <div className="section-heading">
          <h2>
            <Bell size={18} />
            {t("Alert rules")}
            <span>{rules.length}</span>
          </h2>
          <div className="alert-filters" aria-label={t("Filter rules")}>
            {["all", "firing", "paused"].map((v) => (
              <button
                key={v}
                className={filter === v ? "active" : ""}
                onClick={() => setFilter(v)}
              >
                {t(v === "all" ? "All" : v === "firing" ? "Firing" : "Paused")}
              </button>
            ))}
          </div>
        </div>
        {!loaded && !error ? (
          <div className="empty-workspace">{t("Loading rules…")}</div>
        ) : shown.length === 0 ? (
          <div className="empty-workspace">
            <Bell size={32} />
            <h2>
              {t(
                rules.length
                  ? "No rules match this filter"
                  : "No alert rules yet",
              )}
            </h2>
            <p>
              {t(
                "Watch an exporter, service or metric with a single PromQL condition.",
              )}
            </p>
            {rules.length === 0 && (
              <button className="primary" onClick={() => setEditor("new")}>
                {t("Create alert")}
              </button>
            )}
          </div>
        ) : (
          <div className="alert-rule-list">
            {shown.map((rule) => {
              const states = rule.runtime.instances;
              const state = rule.paused
                ? "Paused"
                : states.some((v) => firing(v.state))
                  ? "Firing"
                  : states.some((v) => v.state === "Pending")
                    ? "Pending"
                    : rule.runtime.last_evaluation
                      ? "Normal"
                      : "Not evaluated";
              return (
                <article key={rule.uid} className="alert-rule">
                  <div className="alert-rule-row">
                    <button
                      className="alert-rule-title"
                      onClick={() =>
                        setExpanded(expanded === rule.uid ? null : rule.uid)
                      }
                      aria-expanded={expanded === rule.uid}
                    >
                      {expanded === rule.uid ? (
                        <ChevronDown size={17} />
                      ) : (
                        <ChevronRight size={17} />
                      )}
                      <div>
                        <strong>{rule.title}</strong>
                        <code>
                          {rule.execution === "grafana"
                            ? t("Grafana query graph")
                            : rule.expr}
                        </code>
                      </div>
                    </button>
                    <div className="alert-rule-status">
                      <span className={`status ${stateClass(state)}`}>
                        <i />
                        {stateText(state)}
                      </span>
                      <small>
                        {rule.runtime.last_evaluation
                          ? timeAgo(rule.runtime.last_evaluation)
                          : t("Waiting for evaluation")}
                      </small>
                    </div>
                    <div className="row-actions">
                      <button
                        className="icon-button"
                        aria-label={`${t("Evaluate")} ${rule.title}`}
                        title={t("Evaluate now")}
                        disabled={busy === rule.uid || rule.paused}
                        onClick={() => void act(rule, "evaluate")}
                      >
                        <Play size={16} />
                      </button>
                      <button
                        className="icon-button"
                        aria-label={`${t(rule.paused ? "Resume" : "Pause")} ${rule.title}`}
                        disabled={busy === rule.uid}
                        onClick={() => void act(rule, "pause")}
                      >
                        <Pause size={16} />
                      </button>
                      <button
                        className="icon-button"
                        aria-label={`${t("Edit")} ${rule.title}`}
                        disabled={busy === rule.uid}
                        onClick={() => setEditor(rule)}
                      >
                        <Pencil size={16} />
                      </button>
                      <button
                        className="icon-button danger-hover"
                        aria-label={`${t("Delete")} ${rule.title}`}
                        disabled={busy === rule.uid}
                        onClick={() => setRemoving(rule)}
                      >
                        <Trash2 size={16} />
                      </button>
                    </div>
                  </div>
                  {rule.runtime.error && (
                    <p className="collector-error alert-rule-error">
                      {rule.runtime.error}
                    </p>
                  )}
                  {expanded === rule.uid && (
                    <div className="alert-detail">
                      <div className="alert-rule-meta">
                        <span>
                          {t("Every")} <b>{rule.interval_seconds}s</b>
                        </span>
                        <span>
                          {t("Pending period")} <b>{rule.for_seconds}s</b>
                        </span>
                        <span>
                          {t("Keep firing for")}{" "}
                          <b>{rule.keep_firing_for_seconds}s</b>
                        </span>
                        <span>
                          {t("Evaluation")} <b>{rule.runtime.duration_ms}ms</b>
                        </span>
                        <span>
                          {t("Group")}{" "}
                          <b>
                            {rule.folder_uid}/{rule.group}
                          </b>
                        </span>
                        {rule.record && (
                          <span>
                            {t("Recording metric")} <b>{rule.record}</b>
                          </span>
                        )}
                      </div>
                      {states.length ? (
                        <div className="table-scroll">
                          <table>
                            <thead>
                              <tr>
                                <th>{t("Instance labels")}</th>
                                <th>{t("State")}</th>
                                <th>{t("Value")}</th>
                                <th>{t("Active since")}</th>
                              </tr>
                            </thead>
                            <tbody>
                              {states.map((v) => (
                                <tr key={v.key}>
                                  <td className="mono">
                                    {labelText(v.labels)}
                                    {v.reason && (
                                      <small className="subtle">
                                        {t(v.reason)}
                                      </small>
                                    )}
                                  </td>
                                  <td>
                                    <span
                                      className={`status ${stateClass(v.state)}`}
                                    >
                                      <i />
                                      {stateText(v.state)}
                                    </span>
                                  </td>
                                  <td className="mono">
                                    {v.value_text ||
                                      (v.value === null
                                        ? "—"
                                        : v.value.toLocaleString(undefined, {
                                            maximumFractionDigits: 4,
                                          }))}
                                  </td>
                                  <td>
                                    {v.active_at ? dateText(v.active_at) : "—"}
                                  </td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      ) : (
                        <p className="subtle">
                          {t(
                            rule.paused
                              ? "Evaluation is paused."
                              : rule.record
                                ? "Results are written as stored metrics."
                                : "No instances in the last evaluation.",
                          )}
                        </p>
                      )}
                    </div>
                  )}
                </article>
              );
            })}
          </div>
        )}
      </section>
      <section className="list-panel alert-history">
        <div className="section-heading">
          <h2>
            <History size={18} />
            {t("Recent state changes")}
          </h2>
          <span className="subtle">{t("Last 20 transitions")}</span>
        </div>
        {history.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("Rule / Instance")}</th>
                  <th>{t("Transition")}</th>
                  <th>{t("Time")}</th>
                </tr>
              </thead>
              <tbody>
                {history.map((event) => (
                  <tr key={event.id}>
                    <td>
                      <strong>
                        {event.labels.alertname ||
                          rules.find((r) => r.uid === event.uid)?.title ||
                          event.uid}
                      </strong>
                      <div className="subtle mono">
                        {labelText(event.labels)}
                      </div>
                      {event.reason && (
                        <small className="subtle">{t(event.reason)}</small>
                      )}
                    </td>
                    <td>
                      <span
                        className={`alert-transition ${stateClass(event.to)}`}
                      >
                        {stateText(event.from)} <ChevronRight size={13} />{" "}
                        {stateText(event.to)}
                      </span>
                    </td>
                    <td>{dateText(event.timestamp)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="alert-history-empty">
            {t("Transitions appear when an instance changes state.")}
          </div>
        )}
      </section>
      <p className="subtle alert-note">
        {t(
          "Local evaluations and durable history. External notification delivery is not available yet.",
        )}
      </p>
      {editor && (
        <AlertEditor
          rule={editor}
          metrics={metrics}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            reload();
            notify(t("Rule saved"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={t("Delete alert rule")}
          onClose={() => setRemoving(null)}
        >
          <p>
            {t(
              "Delete this rule and stop evaluating it? Its transition history is retained.",
            )}
          </p>
          <strong>{removing.title}</strong>
          <div className="form-actions">
            <button onClick={() => setRemoving(null)}>{t("Cancel")}</button>
            <button
              className="danger"
              disabled={busy === removing.uid}
              onClick={async () => {
                setBusy(removing.uid);
                try {
                  await api(
                    `/alerts/rules/${encodeURIComponent(removing.uid)}`,
                    { method: "DELETE" },
                  );
                  setRemoving(null);
                  reload();
                  notify(t("Rule deleted"));
                } catch (e) {
                  notify(message(e), true);
                } finally {
                  setBusy(null);
                }
              }}
            >
              {t("Delete")}
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
function AlertEditor({
  rule,
  metrics,
  onClose,
  onSaved,
}: {
  rule: RuleView | "new";
  metrics: Metric[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const original = rule === "new" ? null : rule;
  const [title, setTitle] = useState(original?.title || ""),
    [execution, setExecution] = useState(original?.execution || "promql"),
    [graphJSON, setGraphJSON] = useState(
      JSON.stringify(
        original?.grafana || {
          condition: "C",
          data: [
            {
              refId: "A",
              datasourceUid: "metricspanel",
              relativeTimeRange: { from: 60, to: 0 },
              model: { expr: "sum(metricspanel_memory_bytes)", instant: true },
            },
            {
              refId: "C",
              datasourceUid: "__expr__",
              model: { type: "math", expression: "$A > 1073741824" },
            },
          ],
        },
        null,
        2,
      ),
    ),
    [expr, setExpr] = useState(
      original?.expr || "metricspanel_memory_bytes > bool 1073741824",
    ),
    [condition, setCondition] = useState(original?.condition || "nonzero"),
    [interval, setInterval] = useState(original?.interval_seconds || 30),
    [pending, setPending] = useState(original?.for_seconds || 0),
    [keep, setKeep] = useState(original?.keep_firing_for_seconds || 0),
    [noData, setNoData] = useState(original?.no_data_state || "OK"),
    [errorState, setErrorState] = useState(original?.error_state || "Error"),
    [labelJSON, setLabelJSON] = useState(
      JSON.stringify(original?.labels || {}),
    ),
    [annotationJSON, setAnnotationJSON] = useState(
      JSON.stringify(original?.annotations || {}),
    ),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Dialog
      title={t(original ? "Edit alert rule" : "Create alert")}
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            const { runtime: _, ...config } = original || ({} as RuleView);
            const payload = {
              ...config,
              title,
              execution,
              expr: execution === "grafana" ? "" : expr,
              condition: execution === "grafana" ? "nonzero" : condition,
              interval_seconds: interval,
              for_seconds: pending,
              keep_firing_for_seconds: keep,
              no_data_state: noData,
              error_state: errorState,
              labels: JSON.parse(labelJSON),
              annotations: JSON.parse(annotationJSON),
              ...(execution === "grafana"
                ? { grafana: JSON.parse(graphJSON) }
                : original &&
                    (expr !== original.expr || condition !== original.condition)
                  ? { grafana: undefined }
                  : {}),
            };
            await api(
              original
                ? `/alerts/rules/${encodeURIComponent(original.uid)}`
                : "/alerts/rules",
              { method: original ? "PUT" : "POST", body: jsonBody(payload) },
            );
            onSaved();
          } catch (err) {
            setError(message(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {t("Rule name")}
          <input
            autoFocus
            required
            maxLength={256}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("API unavailable")}
          />
        </label>
        <label>
          {t("Query mode")}
          <select
            aria-label={t("Query mode")}
            value={execution}
            onChange={(event) =>
              setExecution(event.target.value as "promql" | "grafana")
            }
          >
            <option value="promql">PromQL</option>
            <option value="grafana">{t("Grafana query graph")}</option>
          </select>
        </label>
        {execution === "grafana" ? (
          <label>
            {t("Queries and condition (Grafana JSON)")}
            <textarea
              aria-label={t("Queries and condition (Grafana JSON)")}
              className="mono"
              rows={12}
              required
              maxLength={524288}
              value={graphJSON}
              onChange={(event) => setGraphJSON(event.target.value)}
            />
          </label>
        ) : (
          <>
            <label>
              {t("PromQL condition")}
              <textarea
                aria-label={t("PromQL condition")}
                className="mono"
                rows={3}
                required
                maxLength={10000}
                value={expr}
                onChange={(e) => setExpr(e.target.value)}
              />
            </label>
            <div className="alert-preset-row">
              <span>{t("Quick start")}</span>
              <button
                type="button"
                onClick={() => {
                  setExpr("up == bool 0");
                  setCondition("nonzero");
                }}
              >
                {t("Exporter down")}
              </button>
              <select
                aria-label={t("Insert metric")}
                defaultValue=""
                onChange={(e) => {
                  if (e.target.value) {
                    setExpr(`${e.target.value} > bool 0`);
                    setCondition("nonzero");
                    e.target.value = "";
                  }
                }}
              >
                <option value="">{t("Insert metric")}</option>
                {metrics.map((m) => (
                  <option key={m.name} value={m.name}>
                    {m.name}
                  </option>
                ))}
              </select>
            </div>
            <label>
              {t("Condition behavior")}
              <select
                value={condition}
                onChange={(e) => setCondition(e.target.value)}
              >
                <option value="nonzero">
                  {t("Nonzero values fire (use bool comparisons)")}
                </option>
                <option value="presence">
                  {t("Returned samples fire (Prometheus rules)")}
                </option>
              </select>
            </label>
          </>
        )}
        <div className="form-row">
          <label>
            {t("Evaluate every (s)")}
            <input
              type="number"
              min={5}
              max={86400}
              required
              value={interval}
              onChange={(e) => setInterval(Number(e.target.value))}
            />
          </label>
          <label>
            {t("Pending period (s)")}
            <input
              type="number"
              min={0}
              max={604800}
              required
              value={pending}
              onChange={(e) => setPending(Number(e.target.value))}
            />
          </label>
          <label>
            {t("Keep firing for (s)")}
            <input
              type="number"
              min={0}
              max={604800}
              required
              value={keep}
              onChange={(e) => setKeep(Number(e.target.value))}
            />
          </label>
        </div>
        <div className="form-row">
          <label>
            {t("When there is no data")}
            <select value={noData} onChange={(e) => setNoData(e.target.value)}>
              {["OK", "Alerting", "NoData", "KeepLast"].map((p) => (
                <option key={p} value={p}>
                  {t(p)}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("When the query fails")}
            <select
              value={errorState}
              onChange={(e) => setErrorState(e.target.value)}
            >
              {["Error", "OK", "Alerting", "KeepLast"].map((p) => (
                <option key={p} value={p}>
                  {t(p)}
                </option>
              ))}
            </select>
          </label>
        </div>
        <details className="alert-extra">
          <summary>{t("Labels and annotations")}</summary>
          <label>
            {t("Labels (JSON)")}
            <textarea
              className="mono"
              rows={2}
              value={labelJSON}
              onChange={(e) => setLabelJSON(e.target.value)}
            />
          </label>
          <label>
            {t("Annotations (JSON)")}
            <textarea
              className="mono"
              rows={2}
              value={annotationJSON}
              onChange={(e) => setAnnotationJSON(e.target.value)}
            />
          </label>
        </details>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={busy} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy}>
            {t(busy ? "Saving…" : "Save rule")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
