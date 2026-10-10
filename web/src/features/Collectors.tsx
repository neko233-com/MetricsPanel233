import { t as tr } from "../i18n";
import { useEffect, useState } from "react";
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
type Integration = { kind: string; name: string; endpoint: string; protocol: string; metrics: string; docs: string };
type ExporterPreset = { id:string; name:string; endpoint:string; docs:string };
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
              "Collect from databases, service metrics APIs or Prometheus exporters.",
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
            {tr("Collection targets")}
            <span>{targets.length}</span>
          </h2>
        </div>
        {targets.length === 0 ? (
          <div className="empty-workspace">
            <h2>{tr("No collection targets")}</h2>
            <p>
              {tr("Choose a collector type and enter its connection address.")}
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
                      <div className="subtle">{t.kind || "prometheus"}</div>
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
        <h3>{tr("Collection protocols")}</h3>
        <p>
          {tr(
            "MySQL, Redis and PostgreSQL support direct connections. ClickHouse, Elasticsearch, Hadoop, HDFS, Hive, Kafka, Spark and Flink use their metrics APIs. Existing Prometheus exporters use the HTTP collector. Push samples with",
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
  const [catalog, setCatalog] = useState<Integration[]>([]);
  const [exporters, setExporters] = useState<ExporterPreset[]>([]);
  const [preset,setPreset] = useState("");
  const [kind, setKind] = useState(target?.kind || "prometheus");
  const [username, setUsername] = useState(target?.username || "");
  const [database, setDatabase] = useState(target?.database || "");
  const [tlsMode, setTLSMode] = useState(target?.tls_mode || "disable");
  const [password, setPassword] = useState("");
  const [bearer, setBearer] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [clearBearer, setClearBearer] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void api<Integration[]>("/collectors/catalog", { signal: controller.signal })
      .then(setCatalog).catch((error) => { if (!controller.signal.aborted) setError(message(error)); });
    void api<ExporterPreset[]>("/collectors/exporters", {signal:controller.signal}).then(setExporters).catch((error)=>{ if(!controller.signal.aborted) setError(message(error)); });
    return () => controller.abort();
  }, []);
  const integration = catalog.find((entry) => entry.kind === kind);
  const sql = kind === "mysql" || kind === "postgresql";
  const tcp = sql || kind === "redis";
  return (
    <Dialog
      title={tr(target ? "Edit collector" : "Add collector")}
      onClose={saving ? () => {} : onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setSaving(true);
          setError("");
          try {
            await onSave({
              kind, username, database, tls_mode: sql ? tlsMode : undefined,
              secure_settings: {
                ...(password || clearPassword ? { password: clearPassword ? "" : password } : {}),
                ...(bearer || clearBearer ? { bearer_token: clearBearer ? "" : bearer } : {}),
              },
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
        <fieldset className="query-editor-fields" disabled={saving}>
        <label>{tr("Collector type")}
          <select aria-label={tr("Collector type")} value={kind} onChange={(event) => {
            const value=event.target.value; setKind(value);
            const preset=catalog.find((entry)=>entry.kind===value);
            if(preset) { setURL(preset.endpoint); if(!name) setName(value); }
          }}>
            {catalog.length ? catalog.map((entry)=><option key={entry.kind} value={entry.kind}>{entry.name}</option>) : <option value={kind}>{kind}</option>}
          </select>
        </label>
        {integration ? <p className="subtle">{integration.protocol} · {tr(integration.metrics)} <a href={integration.docs} target="_blank" rel="noreferrer">{tr("Documentation")}</a></p> : null}
        {kind === "prometheus" ? <label>{tr("Exporter preset")}
          <select aria-label={tr("Exporter preset")} value={preset} onChange={(event)=>{
            setPreset(event.target.value); const source=exporters.find((item)=>item.id===event.target.value);
            if(source) { setURL(source.endpoint); if(!name || name==="prometheus") setName(source.id); }
          }}><option value="">{tr("Custom endpoint")}</option>{exporters.map((item)=><option key={item.id} value={item.id}>{item.name}</option>)}</select>
          <small>{tr("The exporter must run on the monitored host. Adjust the address, port and probe parameters.")}</small>
        </label> : null}
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
            type={tcp ? "text" : "url"}
            placeholder={integration?.endpoint || "http://localhost:8080/metrics"}
            value={url}
            onChange={(e) => setURL(e.target.value)}
            required
          />
        </label>
        <div className="form-row">
          <label>{tr("Username")}<input aria-label={tr("Username")} value={username} autoComplete="off" onChange={(event)=>setUsername(event.target.value)} /></label>
          <label>{tr("Password")}<input type="password" aria-label={tr("Password")} value={password} autoComplete="new-password" placeholder={target?.secure_fields?.password ? tr("Configured; leave blank to keep") : ""} disabled={clearPassword} onChange={(event)=>setPassword(event.target.value)} /></label>
        </div>
        {target?.secure_fields?.password ? <label className="checkbox-label"><input type="checkbox" checked={clearPassword} onChange={(event)=>setClearPassword(event.target.checked)} />{tr("Clear saved password")}</label> : null}
        {sql ? <div className="form-row">
          <label>{tr("Database")}<input aria-label={tr("Database")} value={database} placeholder={kind === "postgresql" ? "postgres" : ""} onChange={(event)=>setDatabase(event.target.value)} /></label>
          <label>{tr("TLS mode")}<select aria-label={tr("TLS mode")} value={tlsMode} onChange={(event)=>setTLSMode(event.target.value)}><option value="disable">{tr("Disabled")}</option><option value="require">{tr("Encrypt connection")}</option><option value="verify-full">{tr("Verify certificate and hostname")}</option></select></label>
        </div> : null}
        {!tcp ? <label>{tr("Bearer token")}<input type="password" aria-label={tr("Bearer token")} value={bearer} autoComplete="new-password" placeholder={target?.secure_fields?.bearer_token ? tr("Configured; leave blank to keep") : ""} disabled={clearBearer} onChange={(event)=>setBearer(event.target.value)} /></label> : null}
        {!tcp && target?.secure_fields?.bearer_token ? <label className="checkbox-label"><input type="checkbox" checked={clearBearer} onChange={(event)=>setClearBearer(event.target.checked)} />{tr("Clear saved token")}</label> : null}
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
        </fieldset>
      </form>
    </Dialog>
  );
}
