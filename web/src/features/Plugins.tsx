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
import { getLocale, t } from "../i18n";
import { isSQLDatasource, POSTGRES_DATASOURCE } from "../grafana/sql-types";
import type { InstalledPlugin } from "../grafana/plugin-runtime";
import { AppSettingsEditor } from "./AppSettingsEditor";
import { PluginCatalog } from "./PluginCatalog";

type Source = {
  id: number;
  uid: string;
  name: string;
  type: string;
  url: string;
  user?: string;
  database?: string;
  access: string;
  orgId: number;
  version: number;
  jsonData: Record<string, unknown>;
  secureJsonFields: Record<string, boolean>;
  isDefault: boolean;
  readOnly?: boolean;
  basicAuth: boolean;
  basicAuthUser?: string;
};
type Notify = (text: string, error?: boolean) => void;
export function Plugins({ notify }: { notify: Notify }) {
  const [plugins, setPlugins] = useState<InstalledPlugin[]>([]),
    [sources, setSources] = useState<Source[]>([]),
    [error, setError] = useState(""),
    [tab, setTab] = useState<"installed" | "catalog">("installed"),
    [install, setInstall] = useState(false),
    [app, setApp] = useState<InstalledPlugin | null>(null),
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
            {t("Installed plugins")}: {plugins.length} · {t("Datasources")}:{" "}
            {sources.length}
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
      <div
        className="plugin-tabs"
        role="tablist"
        aria-label={t("Plugins")}
        onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
            return;
          event.preventDefault();
          const next =
            event.key === "Home"
              ? "installed"
              : event.key === "End"
                ? "catalog"
                : tab === "installed"
                  ? "catalog"
                  : "installed";
          setTab(next);
          event.currentTarget
            .querySelector<HTMLButtonElement>(
              `#${next === "installed" ? "installed-plugins" : "plugin-library"}-tab`,
            )
            ?.focus();
        }}
      >
        <button
          role="tab"
          id="installed-plugins-tab"
          aria-selected={tab === "installed"}
          tabIndex={tab === "installed" ? 0 : -1}
          aria-controls="installed-plugins-panel"
          onClick={() => setTab("installed")}
        >
          {t("Installed plugins")}
        </button>
        <button
          role="tab"
          id="plugin-library-tab"
          aria-selected={tab === "catalog"}
          tabIndex={tab === "catalog" ? 0 : -1}
          aria-controls="plugin-library-panel"
          onClick={() => setTab("catalog")}
        >
          {t("Plugin library")}
        </button>
      </div>
      {tab === "catalog" ? (
        <PluginCatalog plugins={plugins} onInstalled={reload} notify={notify} />
      ) : (
        <section
          className="list-panel"
          id="installed-plugins-panel"
          role="tabpanel"
          aria-labelledby="installed-plugins-tab"
        >
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
                  {p.type === "app" && (
                    <div className="plugin-app-actions">
                      <button onClick={() => setApp(p)}>
                        {t("Configure")}
                      </button>
                      {p.enabled && (
                        <a href={`/a/${encodeURIComponent(p.id)}/`}>
                          {t("Open application")}
                        </a>
                      )}
                    </div>
                  )}
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
      )}
      {app && (
        <AppSettingsEditor
          id={app.id}
          name={app.name}
          close={() => setApp(null)}
          saved={() => {
            setApp(null);
            void reload();
            notify(t("Application settings saved"));
          }}
        />
      )}
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
                      {!ds.readOnly &&
                        ds.uid !== "metricspanel" &&
                        ds.uid !== "grafana" && (
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
          user: "",
          database: "",
          version: 0,
          uid: "",
          isDefault: false,
          jsonData: {},
          basicAuth: false,
          basicAuthUser: "",
        }
      : {
          ...source,
          type: source.type === "postgres" ? POSTGRES_DATASOURCE : source.type,
        };
  const [form, setForm] = useState(initial),
    [json, setJSON] = useState(JSON.stringify(initial.jsonData, null, 2)),
    [key, setKey] = useState(""),
    [password, setPassword] = useState(""),
    [sqlPassword, setSQLPassword] = useState(""),
    [tlsSecrets, setTLSSecrets] = useState<Record<string, string>>({}),
    [clearSQLSecrets, setClearSQLSecrets] = useState<string[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const sql = isSQLDatasource(form.type);
  const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
  let sqlOptions: Record<string, unknown> = {};
  try {
    const parsed = JSON.parse(json);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed))
      sqlOptions = parsed;
  } catch {
    /* Invalid advanced JSON is reported on save. */
  }
  function patchSQLSettings(update: Record<string, unknown>) {
    try {
      const parsed = JSON.parse(json);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
        throw new Error(
          text(
            "Connection settings must be a JSON object",
            "连接设置必须是 JSON 对象",
          ),
        );
      setJSON(JSON.stringify({ ...parsed, ...update }, null, 2));
      setError("");
    } catch (reason) {
      setError(message(reason));
    }
  }
  function clearSecret(key: string, clear: boolean) {
    setClearSQLSecrets((current) =>
      clear
        ? [...current.filter((name) => name !== key), key]
        : current.filter((name) => name !== key),
    );
  }
  return (
    <Dialog
      title={t(source === "new" ? "Add datasource" : "Edit datasource")}
      onClose={() => {
        if (!busy) onClose();
      }}
      style={sql ? { width: 720 } : undefined}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            const jsonData = JSON.parse(json);
            const secureJsonData: Record<string, string> = {};
            if (sql) {
              if (sqlPassword && !clearSQLSecrets.includes("password"))
                secureJsonData.password = sqlPassword;
              for (const [name, value] of Object.entries(tlsSecrets))
                if (value && !clearSQLSecrets.includes(name))
                  secureJsonData[name] = value;
            } else {
              if (key) secureJsonData.apiKey = key;
              if (password) secureJsonData.basicAuthPassword = password;
            }
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
                  ...(sql
                    ? {
                        basicAuth: false,
                        secureJsonFields: {
                          ...(source === "new" ? {} : source.secureJsonFields),
                          ...Object.fromEntries(
                            clearSQLSecrets.map((name) => [name, false]),
                          ),
                        },
                      }
                    : {}),
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
            aria-label={t("Plugin type")}
            value={form.type}
            onChange={(e) => setForm({ ...form, type: e.target.value })}
          >
            <option value="prometheus">Prometheus</option>
            <option value="mysql">MySQL</option>
            <option value={POSTGRES_DATASOURCE}>PostgreSQL</option>
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
            type={sql ? "text" : "url"}
            aria-label="URL"
            value={form.url}
            onChange={(e) => setForm({ ...form, url: e.target.value })}
            placeholder={
              sql
                ? form.type === "mysql"
                  ? "127.0.0.1:3306"
                  : "127.0.0.1:5432"
                : "http://localhost:9090"
            }
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
        {sql ? (
          <>
            <div className="form-row">
              <label>
                {text("Database user", "数据库用户")}
                <input
                  aria-label={text("Database user", "数据库用户")}
                  required
                  maxLength={512}
                  value={form.user || ""}
                  disabled={busy}
                  onChange={(event) =>
                    setForm({ ...form, user: event.target.value })
                  }
                />
              </label>
              <label>
                {text("Database name", "数据库名称")}
                <input
                  aria-label={text("Database name", "数据库名称")}
                  required
                  maxLength={512}
                  value={form.database || String(sqlOptions.database || "")}
                  disabled={busy}
                  onChange={(event) =>
                    setForm({ ...form, database: event.target.value })
                  }
                />
              </label>
            </div>
            <label>
              {text("Database password", "数据库密码")}
              <input
                aria-label={text("Database password", "数据库密码")}
                type="password"
                autoComplete="new-password"
                maxLength={16384}
                value={sqlPassword}
                disabled={busy || clearSQLSecrets.includes("password")}
                onChange={(event) => setSQLPassword(event.target.value)}
                placeholder={
                  source !== "new" && source.secureJsonFields.password
                    ? t("Configured; leave empty to keep")
                    : ""
                }
              />
            </label>
            {source !== "new" && source.secureJsonFields.password && (
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  checked={clearSQLSecrets.includes("password")}
                  disabled={busy}
                  onChange={(event) =>
                    clearSecret("password", event.target.checked)
                  }
                />
                {text(
                  "Remove stored database password",
                  "删除已保存的数据库密码",
                )}
              </label>
            )}
            <div className="form-row">
              <label>
                {text("TLS mode", "TLS 模式")}
                <select
                  aria-label={text("TLS mode", "TLS 模式")}
                  disabled={busy}
                  value={String(
                    sqlOptions.sslmode ||
                      (form.type === "mysql" ? "disable" : "verify-full"),
                  )}
                  onChange={(event) =>
                    patchSQLSettings({ sslmode: event.target.value })
                  }
                >
                  {["disable", "require", "verify-ca", "verify-full"].map(
                    (mode) => (
                      <option key={mode}>{mode}</option>
                    ),
                  )}
                </select>
              </label>
              <label>
                {text("Query timeout (seconds)", "查询超时（秒）")}
                <input
                  aria-label={text("Query timeout (seconds)", "查询超时（秒）")}
                  type="number"
                  min={1}
                  max={20}
                  value={Number(sqlOptions.queryTimeout ?? 20)}
                  disabled={busy}
                  onChange={(event) =>
                    patchSQLSettings({
                      queryTimeout: Number(event.target.value),
                    })
                  }
                />
              </label>
              <label>
                {text("Maximum result rows", "最大结果行数")}
                <input
                  aria-label={text("Maximum result rows", "最大结果行数")}
                  type="number"
                  min={1}
                  max={10000}
                  value={Number(sqlOptions.maxRows ?? 10000)}
                  disabled={busy}
                  onChange={(event) =>
                    patchSQLSettings({ maxRows: Number(event.target.value) })
                  }
                />
              </label>
            </div>
            <details>
              <summary>
                {text(
                  "TLS certificates and connection pool",
                  "TLS 证书与连接池",
                )}
              </summary>
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  checked={Boolean(sqlOptions.tlsAuthWithCACert)}
                  disabled={busy}
                  onChange={(event) =>
                    patchSQLSettings({
                      tlsAuthWithCACert: event.target.checked,
                    })
                  }
                />
                {text("Use custom CA certificate", "使用自定义 CA 证书")}
              </label>
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  checked={Boolean(sqlOptions.tlsAuth)}
                  disabled={busy}
                  onChange={(event) =>
                    patchSQLSettings({ tlsAuth: event.target.checked })
                  }
                />
                {text(
                  "Client certificate authentication",
                  "客户端证书身份验证",
                )}
              </label>
              {[
                ["tlsCACert", text("CA certificate (PEM)", "CA 证书（PEM）")],
                [
                  "tlsClientCert",
                  text("Client certificate (PEM)", "客户端证书（PEM）"),
                ],
                [
                  "tlsClientKey",
                  text("Client private key (PEM)", "客户端私钥（PEM）"),
                ],
              ].map(([name, label]) => (
                <div key={name}>
                  <label>
                    {label}
                    <textarea
                      aria-label={label}
                      className="mono"
                      rows={3}
                      maxLength={16384}
                      value={tlsSecrets[name] || ""}
                      disabled={busy || clearSQLSecrets.includes(name)}
                      onChange={(event) =>
                        setTLSSecrets((current) => ({
                          ...current,
                          [name]: event.target.value,
                        }))
                      }
                      placeholder={
                        source !== "new" && source.secureJsonFields[name]
                          ? t("Configured; leave empty to keep")
                          : "-----BEGIN CERTIFICATE-----"
                      }
                    />
                  </label>
                  {source !== "new" && source.secureJsonFields[name] && (
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={clearSQLSecrets.includes(name)}
                        disabled={busy}
                        onChange={(event) =>
                          clearSecret(name, event.target.checked)
                        }
                      />
                      {text("Remove stored", "删除已保存的")} {label}
                    </label>
                  )}
                </div>
              ))}
              <div className="form-row">
                {[
                  [
                    "maxOpenConns",
                    text("Open connections", "最大连接数"),
                    2,
                    1,
                    10,
                  ],
                  [
                    "maxIdleConns",
                    text("Idle connections", "空闲连接数"),
                    2,
                    0,
                    10,
                  ],
                  [
                    "connMaxLifetime",
                    text("Connection lifetime (seconds)", "连接有效期（秒）"),
                    600,
                    60,
                    3600,
                  ],
                ].map(([name, label, defaultValue, minimum, maximum]) => (
                  <label key={name}>
                    {label}
                    <input
                      aria-label={String(label)}
                      type="number"
                      disabled={busy}
                      min={Number(minimum)}
                      max={
                        name === "maxIdleConns"
                          ? Number(sqlOptions.maxOpenConns ?? 2)
                          : Number(maximum)
                      }
                      value={Number(sqlOptions[String(name)] ?? defaultValue)}
                      onChange={(event) =>
                        patchSQLSettings({
                          [String(name)]: Number(event.target.value),
                        })
                      }
                    />
                  </label>
                ))}
              </div>
            </details>
          </>
        ) : (
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
        )}
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
          {!sql && (
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
          )}
          {!sql && form.basicAuth && (
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
