import { t as tr } from "../i18n";
import { useRef, useState } from "react";
import { ArrowRight, Download, Plus, Trash2, Upload } from "lucide-react";
import { api, download, jsonBody, message, type Dashboard } from "../api";
import { Dialog } from "../components/Dialog";
export function Dashboards({
  dashboards,
  open,
  reload,
  notify,
}: {
  dashboards: Dashboard[];
  open: (d: Dashboard) => void;
  reload: () => Promise<void>;
  notify: (s: string, error?: boolean) => void;
}) {
  const [creating, setCreating] = useState(false),
    [name, setName] = useState(""),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false),
    [removing, setRemoving] = useState<Dashboard | null>(null);
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Dashboards")}</h1>
          <p>{tr("A few useful panels. A clear view of your system.")}</p>
        </div>
        <div className="toolbar">
          <button onClick={() => input.current?.click()}>
            <Upload size={17} />
            {tr("Import JSON")}
          </button>
          <button
            className="primary"
            onClick={() => {
              setCreating(true);
              setName("");
              setError("");
            }}
          >
            <Plus size={18} />
            {tr("New dashboard")}
          </button>
        </div>
      </div>
      <input
        ref={input}
        type="file"
        accept=".json,application/json"
        hidden
        onChange={async (e) => {
          const file = e.target.files?.[0];
          if (!file) return;
          try {
            if (file.size > 4 * 1024 * 1024)
              throw new Error("Import must be under 4 MiB");
            const data = JSON.parse(await file.text()) as Dashboard & {
              title?: string;
              dashboard?: unknown;
            };
            let imported: Dashboard;
            if (data.title || data.dashboard) {
              const result = await api<{
                dashboard: Dashboard;
                warnings: string[];
              }>("/import/grafana", { method: "POST", body: jsonBody(data) });
              imported = result.dashboard;
              if (result.warnings.length) {
                window.alert(
                  `${tr("Unsupported template features")}:\n${result.warnings.join("\n")}`,
                );
              }
            } else
              imported = await api<Dashboard>("/dashboards", {
                method: "POST",
                body: jsonBody(data),
              });
            await reload();
            open(imported);
            notify(tr("Dashboard imported"));
          } catch (e) {
            notify(message(e), true);
          } finally {
            if (input.current) input.current.value = "";
          }
        }}
      />
      <section className="list-panel">
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{tr("Dashboard")}</th>
                <th>{tr("Panels")}</th>
                <th>{tr("Last updated")}</th>
                <th>{tr("Actions")}</th>
              </tr>
            </thead>
            <tbody>
              {dashboards.map((d) => (
                <tr key={d.id}>
                  <td>
                    <button className="dashboard-title" onClick={() => open(d)}>
                      {tr(d.name)}
                      <ArrowRight size={17} />
                    </button>
                    <div className="subtle mono">{d.id}</div>
                  </td>
                  <td>{d.panels.length}</td>
                  <td>{new Date(d.updated_at).toLocaleString()}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        aria-label={`Export ${tr(d.name)}`}
                        title={tr("Export JSON")}
                        className="icon-button"
                        onClick={() => download(`${d.id}.json`, d)}
                      >
                        <Download size={17} />
                      </button>
                      {d.id !== "system" && (
                        <button
                          className="icon-button danger-hover"
                          aria-label={`Delete ${tr(d.name)}`}
                          onClick={() => setRemoving(d)}
                        >
                          <Trash2 size={17} />
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      {creating && (
        <Dialog title={tr("New dashboard")} onClose={() => setCreating(false)}>
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              setSaving(true);
              setError("");
              try {
                const d = await api<Dashboard>("/dashboards", {
                  method: "POST",
                  body: jsonBody({ name, panels: [] }),
                });
                await reload();
                setCreating(false);
                open(d);
                notify(tr("Dashboard created"));
              } catch (e) {
                setError(message(e));
              } finally {
                setSaving(false);
              }
            }}
          >
            <label>
              {tr("Dashboard name")}
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={tr("e.g. API overview")}
                required
                maxLength={100}
              />
            </label>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <div className="form-actions">
              <button type="button" onClick={() => setCreating(false)}>
                {tr("Cancel")}
              </button>
              <button className="primary" disabled={saving}>
                {tr(saving ? "Creating…" : "Create dashboard")}
              </button>
            </div>
          </form>
        </Dialog>
      )}
      {removing && (
        <Dialog
          title={tr("Delete dashboard")}
          onClose={() => setRemoving(null)}
        >
          <p>
            {tr("Delete \u201C")}
            {removing.name}
            {tr("\u201D and its panel configuration?")}
          </p>
          <div className="form-actions">
            <button onClick={() => setRemoving(null)}>{tr("Cancel")}</button>
            <button
              className="danger"
              onClick={async () => {
                try {
                  await api(`/dashboards/${encodeURIComponent(removing.id)}`, {
                    method: "DELETE",
                  });
                  await reload();
                  setRemoving(null);
                  notify(tr("Dashboard deleted"));
                } catch (e) {
                  notify(message(e), true);
                }
              }}
            >
              {tr("Delete dashboard")}
            </button>
          </div>
        </Dialog>
      )}
    </>
  );
}
