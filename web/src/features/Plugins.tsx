import { useEffect, useState } from "react";
import {
  Check,
  Download,
  Pencil,
  Plus,
  Puzzle,
  ShieldCheck,
  Trash2,
  Upload,
} from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";
import type { InstalledPlugin } from "../grafana/plugin-runtime";

type Source = {
  id: number;
  uid: string;
  name: string;
  type: string;
  url: string;
  access: string;
  orgId: number;
  version: number;
  jsonData: Record<string, unknown>;
  secureJsonFields: Record<string, boolean>;
  isDefault: boolean;
  basicAuth: boolean;
  basicAuthUser?: string;
};
type Notify = (text: string, error?: boolean) => void;
export function Plugins({ notify }: { notify: Notify }) {
  const [plugins, setPlugins] = useState<InstalledPlugin[]>([]),
    [sources, setSources] = useState<Source[]>([]),
    [error, setError] = useState(""),
    [install, setInstall] = useState(false),
    [editing, setEditing] = useState<Source | "new" | null>(null),
    [removing, setRemoving] = useState<{
      kind: "plugin" | "source";
      id: string;
      name: string;
    } | null>(null),
    [busy, setBusy] = useState("");
  async function reload() {
    try {
      const [p, s] = await Promise.all([
        api<InstalledPlugin[]>("/plugins"),
        api<Source[]>("/api/datasources"),
      ]);
      setPlugins(p);
      setSources(s);
      setError("");
    } catch (e) {
      setError(message(e));
    }
  }
  useEffect(() => {
    void reload();
  }, []);
  async function toggle(p: InstalledPlugin) {
    setBusy(p.id);
    try {
      await api(`/plugins/${p.id}`, {
        method: "PUT",
        body: jsonBody({ enabled: !p.enabled }),
      });
      await reload();
      notify(t(p.enabled ? "Plugin disabled" : "Plugin enabled"));
    } catch (e) {
      notify(message(e), true);
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{t("Plugins & datasources")}</h1>
          <p>
            {t(
              "Install Grafana plugins and connect the data behind your dashboards.",
            )}
          </p>
        </div>
        <button className="primary" onClick={() => setInstall(true)}>
          <Download size={18} />
          {t("Install plugin")}
        </button>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <section className="list-panel">
        <div className="section-heading">
          <h2>
            {t("Installed plugins")}
            <span>{plugins.length}</span>
          </h2>
          <span className="subtle">
            <ShieldCheck size={15} /> {t("Signatures verified")}
          </span>
        </div>
        {plugins.length === 0 ? (
          <div className="empty-workspace">
            <Puzzle size={32} />
            <h2>{t("Add a visualization or datasource")}</h2>
            <p>
              {t(
                "Use a plugin ID and exact version, or upload its original ZIP package.",
              )}
            </p>
            <button onClick={() => setInstall(true)}>
              <Plus size={17} />
              {t("Install plugin")}
            </button>
          </div>
        ) : (
          <div className="plugin-list">
            {plugins.map((p) => (
              <article className="plugin-row" key={p.id}>
                <div className="plugin-icon">
                  <Puzzle size={22} />
                </div>
                <div className="plugin-description">
                  <strong>{p.name}</strong>
                  <code>
                    {p.id} · {p.version}
                  </code>
                  <div className="plugin-meta">
                    <span>{t(p.type)}</span>
                    <span>
                      {p.signature === "unsigned"
                        ? t("Development package")
                        : t("Signed package")}
                    </span>
                    {p.backend && <span>{t("Backend")}</span>}
                  </div>
                </div>
                <label className="plugin-enabled">
                  <input
                    type="checkbox"
                    checked={p.enabled}
                    disabled={busy === p.id}
                    onChange={() => void toggle(p)}
                  />
                  {t("Enabled")}
                </label>
                <button
                  className="icon-button"
                  aria-label={`${t("Uninstall")} ${p.name}`}
                  onClick={() =>
                    setRemoving({
                      kind: "plugin",
                      id: p.package_id,
                      name: p.name,
                    })
                  }
                >
                  <Trash2 size={17} />
                </button>
              </article>
            ))}
          </div>
        )}
      </section>
      <section className="list-panel plugin-sources">
        <div className="section-heading">
          <h2>
            {t("Datasources")}
            <span>{sources.length}</span>
          </h2>
          <button onClick={() => setEditing("new")}>
            <Plus size={17} />
            {t("Add datasource")}
          </button>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{t("Name / Endpoint")}</th>
                <th>{t("Type")}</th>
                <th>{t("Status")}</th>
                <th>{t("Actions")}</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((ds) => (
                <tr key={ds.uid}>
                  <td>
                    <strong>{ds.name}</strong>
                    <div className="endpoint mono">{ds.url || ds.uid}</div>
                  </td>
                  <td>
                    <code>{ds.type}</code>
                  </td>
                  <td>
                    {ds.isDefault && (
                      <span className="status healthy">
                        <i />
                        {t("Default")}
                      </span>
                    )}
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="icon-button"
                        aria-label={`${t("Test connection")} ${ds.name}`}
                        disabled={busy === ds.uid}
                        onClick={async () => {
                          setBusy(ds.uid);
                          try {
                            const result = await api<{
                              status: string;
                              message: string;
                            }>(`/api/datasources/uid/${ds.uid}/health`);
                            notify(result.message, result.status !== "OK");
                          } catch (e) {
                            notify(message(e), true);
                          } finally {
                            setBusy("");
                          }
                        }}
                      >
                        <Check size={17} />
                      </button>
                      {ds.uid !== "metricspanel" && (
                        <>
                          <button
                            className="icon-button"
                            aria-label={`${t("Edit")} ${ds.name}`}
                            onClick={() => setEditing(ds)}
                          >
                            <Pencil size={17} />
                          </button>
                          <button
                            className="icon-button"
                            aria-label={`${t("Delete")} ${ds.name}`}
                            onClick={() =>
                              setRemoving({
                                kind: "source",
                                id: ds.uid,
                                name: ds.name,
                              })
                            }
                          >
                            <Trash2 size={17} />
                          </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      {install && (
        <InstallDialog
          onClose={() => setInstall(false)}
          onInstalled={async () => {
            setInstall(false);
            await reload();
            notify(t("Plugin installed"));
          }}
        />
      )}
      {editing && (
        <SourceDialog
          source={editing}
          plugins={plugins}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await reload();
            notify(t("Datasource saved"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={t(
            removing.kind === "plugin"
              ? "Uninstall plugin"
              : "Delete datasource",
          )}
          onClose={() => setRemoving(null)}
        >
          <p>{removing.name}</p>
          <p className="subtle">
            {t(
              removing.kind === "plugin"
                ? "Remove its datasources before uninstalling this package."
                : "Dashboards using this datasource will need another connection.",
            )}
          </p>
          <div className="dialog-actions">
            <button onClick={() => setRemoving(null)}>{t("Cancel")}</button>
            <button
              className="danger"
              disabled={Boolean(busy)}
              onClick={async () => {
                setBusy(removing.id);
                try {
                  await api(
                    removing.kind === "plugin"
                      ? `/plugins/${removing.id}`
                      : `/api/datasources/uid/${removing.id}`,
                    { method: "DELETE" },
                  );
                  setRemoving(null);
                  await reload();
                  notify(t("Removed"));
                } catch (e) {
                  notify(message(e), true);
                } finally {
                  setBusy("");
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

function InstallDialog({
  onClose,
  onInstalled,
}: {
  onClose: () => void;
  onInstalled: () => Promise<void>;
}) {
  const [id, setID] = useState(""),
    [version, setVersion] = useState(""),
    [file, setFile] = useState<File | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Dialog title={t("Install plugin")} onClose={onClose}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            if (file) {
              if (file.size > 64 * 1024 * 1024)
                throw new Error(t("ZIP package must be under 64 MiB"));
              await api("/plugins/install", {
                method: "POST",
                headers: { "Content-Type": "application/zip" },
                body: file,
              });
            } else {
              await api("/plugins/catalog", {
                method: "POST",
                body: jsonBody({ id, version }),
              });
            }
            await onInstalled();
          } catch (err) {
            setError(message(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <p className="subtle">
          {t("Download a signed package from the Grafana catalog.")}
        </p>
        <label>
          {t("Plugin ID")}
          <input
            value={id}
            onChange={(e) => setID(e.target.value)}
            placeholder="grafana-clock-panel"
            required={!file}
            disabled={Boolean(file)}
          />
        </label>
        <label>
          {t("Exact version")}
          <input
            value={version}
            onChange={(e) => setVersion(e.target.value)}
            placeholder="3.2.4"
            required={!file}
            disabled={Boolean(file)}
          />
        </label>
        <label className="plugin-upload">
          <Upload size={17} />
          {t("Or upload a ZIP package")}
          <input
            type="file"
            accept=".zip,application/zip"
            onChange={(e) => setFile(e.target.files?.[0] || null)}
          />
        </label>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy}>
            {t(busy ? "Installing…" : "Install plugin")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
function SourceDialog({
  source,
  plugins,
  onClose,
  onSaved,
}: {
  source: Source | "new";
  plugins: InstalledPlugin[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const initial =
    source === "new"
      ? {
          name: "",
          type: "prometheus",
          url: "",
          version: 0,
          uid: "",
          isDefault: false,
          jsonData: {},
          basicAuth: false,
          basicAuthUser: "",
        }
      : source;
  const [form, setForm] = useState(initial),
    [json, setJSON] = useState(JSON.stringify(initial.jsonData, null, 2)),
    [key, setKey] = useState(""),
    [password, setPassword] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Dialog
      title={t(source === "new" ? "Add datasource" : "Edit datasource")}
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            const jsonData = JSON.parse(json);
            const secureJsonData: Record<string, string> = {};
            if (key) secureJsonData.apiKey = key;
            if (password) secureJsonData.basicAuthPassword = password;
            await api(
              source === "new"
                ? "/api/datasources"
                : `/api/datasources/uid/${form.uid}`,
              {
                method: source === "new" ? "POST" : "PUT",
                body: jsonBody({
                  ...form,
                  access: "proxy",
                  orgId: 1,
                  jsonData,
                  secureJsonData,
                }),
              },
            );
            await onSaved();
          } catch (err) {
            setError(message(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {t("Datasource name")}
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            required
          />
        </label>
        <label>
          {t("Plugin type")}
          <select
            value={form.type}
            onChange={(e) => setForm({ ...form, type: e.target.value })}
          >
            <option value="prometheus">Prometheus</option>
            {plugins
              .filter((p) => p.type === "datasource" && p.enabled)
              .map((p) => (
                <option value={p.id} key={p.id}>
                  {p.name}
                </option>
              ))}
          </select>
        </label>
        <label>
          URL
          <input
            type="url"
            value={form.url}
            onChange={(e) => setForm({ ...form, url: e.target.value })}
            placeholder="http://localhost:9090"
          />
        </label>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={form.isDefault}
            onChange={(e) => setForm({ ...form, isDefault: e.target.checked })}
          />
          {t("Use as default datasource")}
        </label>
        <label>
          {t("API key")}
          <input
            type="password"
            autoComplete="new-password"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder={
              source !== "new" && source.secureJsonFields.apiKey
                ? t("Configured; leave empty to keep")
                : ""
            }
          />
        </label>
        <details>
          <summary>{t("Advanced settings")}</summary>
          <label>
            {t("Connection settings (JSON)")}
            <textarea
              className="mono"
              rows={5}
              value={json}
              onChange={(e) => setJSON(e.target.value)}
            />
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={form.basicAuth}
              onChange={(e) =>
                setForm({ ...form, basicAuth: e.target.checked })
              }
            />
            {t("HTTP Basic authentication")}
          </label>
          {form.basicAuth && (
            <>
              <label>
                {t("Username")}
                <input
                  value={form.basicAuthUser || ""}
                  onChange={(e) =>
                    setForm({ ...form, basicAuthUser: e.target.value })
                  }
                />
              </label>
              <label>
                {t("Password")}
                <input
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={
                    source !== "new" &&
                    source.secureJsonFields.basicAuthPassword
                      ? t("Configured; leave empty to keep")
                      : ""
                  }
                />
              </label>
            </>
          )}
        </details>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy}>
            {t(busy ? "Saving…" : "Save datasource")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
