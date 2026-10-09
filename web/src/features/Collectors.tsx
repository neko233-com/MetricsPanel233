import { t as tr } from "../i18n";
import { useState } from "react";
import { Check, Pencil, Play, Plus, Trash2 } from "lucide-react";
import {
  api,
  jsonBody,
  message,
  parseLabels,
  timeAgo,
  type Target,
} from "../api";
import { Dialog } from "../components/Dialog";
export function Collectors({
  targets,
  reload,
  notify,
}: {
  targets: Target[];
  reload: () => Promise<void>;
  notify: (s: string, error?: boolean) => void;
}) {
  const [editor, setEditor] = useState<Target | "new" | null>(null),
    [removing, setRemoving] = useState<Target | null>(null),
    [busy, setBusy] = useState<number | null>(null);
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Collectors")}</h1>
          <p>
            {tr(
              "Connect a Prometheus exporter. Collection starts automatically.",
            )}
          </p>
        </div>
        <button className="primary" onClick={() => setEditor("new")}>
          <Plus size={18} />
          {tr("Add collector")}
        </button>
      </div>
      <div className="builtin-row">
        <div className="builtin-icon">
          <Check size={22} />
        </div>
        <div>
          <h2>{tr("MetricsPanel internal collector")}</h2>
          <p>
            {tr(
              "Memory, goroutines, HTTP requests and uptime \u00B7 every 5 seconds",
            )}
          </p>
        </div>
        <span className="status healthy">
          <i />
          {tr("healthy")}
        </span>
      </div>
      <section className="list-panel">
        <div className="section-heading">
          <h2>
            {tr("Prometheus exporters")}
            <span>{targets.length}</span>
          </h2>
        </div>
        {targets.length === 0 ? (
          <div className="empty-workspace">
            <h2>{tr("No exporters connected yet")}</h2>
            <p>
              {tr("Add an existing /metrics endpoint to start collecting.")}
            </p>
            <button onClick={() => setEditor("new")} className="primary">
              <Plus size={18} />
              {tr("Add collector")}
            </button>
          </div>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{tr("Name / Endpoint")}</th>
                  <th>{tr("Interval")}</th>
                  <th>{tr("Status")}</th>
                  <th>{tr("Last scrape")}</th>
                  <th>{tr("Samples")}</th>
                  <th>{tr("Actions")}</th>
                </tr>
              </thead>
              <tbody>
                {targets.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <strong>{t.name}</strong>
                      <div className="endpoint mono">{t.url}</div>
                      {t.last_error && (
                        <div className="collector-error">{t.last_error}</div>
                      )}
                    </td>
                    <td className="mono">
                      {t.interval_seconds}
                      {tr("s")}
                    </td>
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
                    <td>
                      {timeAgo(t.last_scrape)}
                      <div className="subtle">
                        {t.duration_ms}
                        {tr("ms")}
                      </div>
                    </td>
                    <td className="mono">{t.samples}</td>
                    <td>
                      <div className="row-actions">
                        <button
                          className="icon-button"
                          disabled={busy === t.id}
                          aria-label={`${tr("Scrape")} ${t.name}`}
                          title={tr("Scrape now")}
                          onClick={async () => {
                            setBusy(t.id);
                            try {
                              await api(`/targets/${t.id}/scrape`, {
                                method: "POST",
                              });
                              await reload();
                              notify(tr("Scrape completed"));
                            } catch (e) {
                              await reload();
                              notify(message(e), true);
                            } finally {
                              setBusy(null);
                            }
                          }}
                        >
                          <Play size={16} />
                        </button>
                        <button
                          className="icon-button"
                          aria-label={`${tr("Edit")} ${t.name}`}
                          onClick={() => setEditor(t)}
                        >
                          <Pencil size={16} />
                        </button>
                        <button
                          className="icon-button danger-hover"
                          aria-label={`${tr("Delete")} ${t.name}`}
                          onClick={() => setRemoving(t)}
                        >
                          <Trash2 size={16} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <div className="inline-note">
        <h3>{tr("One endpoint is all you need.")}</h3>
        <p>
          {tr(
            "Prometheus text-format gauges, counters, classic histograms and summaries are supported. Exporter labels are preserved; collector labels take priority. You can also push samples with",
          )}
          <code>{tr("metricspanel ingest")}</code>.
        </p>
      </div>
      {editor && (
        <CollectorEditor
          target={editor === "new" ? undefined : editor}
          onClose={() => setEditor(null)}
          onSave={async (target) => {
            await api(editor === "new" ? "/targets" : `/targets/${editor.id}`, {
              method: editor === "new" ? "POST" : "PUT",
              body: jsonBody(target),
            });
            await reload();
            setEditor(null);
            notify(tr("Collector saved"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={tr("Delete collector")}
          onClose={() => setRemoving(null)}
        >
          <p>
            {tr("Stop collecting from \u201C")}
            {removing.name}
            {tr(
              "\u201D? Stored samples remain available until the retention period ends.",
            )}
          </p>
          <div className="form-actions">
            <button onClick={() => setRemoving(null)}>{tr("Cancel")}</button>
            <button
              className="danger"
              onClick={async () => {
                try {
                  await api(`/targets/${removing.id}`, { method: "DELETE" });
                  await reload();
                  setRemoving(null);
                  notify(tr("Collector deleted"));
                } catch (e) {
                  notify(message(e), true);
                }
              }}
            >
              {tr("Delete collector")}
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
function CollectorEditor({
  target,
  onClose,
  onSave,
}: {
  target?: Target;
  onClose: () => void;
  onSave: (t: Partial<Target>) => Promise<void>;
}) {
  const [name, setName] = useState(target?.name || ""),
    [url, setURL] = useState(target?.url || ""),
    [interval, setInterval] = useState(target?.interval_seconds || 15),
    [labels, setLabels] = useState(JSON.stringify(target?.labels || {})),
    [enabled, setEnabled] = useState(target?.enabled ?? true),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false);
  return (
    <Dialog
      title={tr(target ? "Edit collector" : "Add collector")}
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setSaving(true);
          setError("");
          try {
            await onSave({
              name,
              url,
              interval_seconds: interval,
              labels: parseLabels(labels),
              enabled,
            });
          } catch (e) {
            setError(message(e));
            setSaving(false);
          }
        }}
      >
        <label>
          {tr("Collector name")}
          <input
            autoFocus
            placeholder={tr("e.g. api-server")}
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            maxLength={100}
          />
        </label>
        <label>
          {tr("Metrics endpoint")}
          <input
            type="url"
            placeholder={tr("http://localhost:8080/metrics")}
            value={url}
            onChange={(e) => setURL(e.target.value)}
            required
          />
        </label>
        <label>
          {tr("Scrape interval")}
          <small>{tr("seconds")}</small>
          <input
            type="number"
            min={5}
            max={86400}
            value={interval}
            onChange={(e) => setInterval(Number(e.target.value))}
            required
          />
        </label>
        <label>
          {tr("Labels")}
          <small>{tr("JSON object")}</small>
          <textarea
            className="mono"
            value={labels}
            onChange={(e) => setLabels(e.target.value)}
            rows={2}
          />
        </label>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          {tr("Automatic collection enabled")}
        </label>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            {tr("Cancel")}
          </button>
          <button className="primary" disabled={saving}>
            {tr(saving ? "Saving…" : "Save collector")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
