import { useEffect, useMemo, useState } from "react";
import { Download, ExternalLink, Search } from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";
import type { InstalledPlugin } from "../grafana/plugin-runtime";

type CatalogPlugin = {
  id: string;
  name: string;
  type: string;
  version: string;
  description: string;
  author: string;
  downloads: number;
  deprecated: boolean;
  angular_detected: boolean;
  grafana_dependency: string;
  url: string;
  backend: boolean;
};
type CatalogVersion = {
  version: string;
  created_at: string;
  grafana_dependency: string;
  angular_detected: boolean;
  platform_available: boolean;
};
type Props = {
  plugins: InstalledPlugin[];
  onInstalled: () => Promise<void>;
  notify: (text: string, error?: boolean) => void;
};

export function PluginCatalog({ plugins, onInstalled, notify }: Props) {
  const [search, setSearch] = useState("");
  const [type, setType] = useState("");
  const [query, setQuery] = useState({ search: "", type: "", retry: 0 });
  const [items, setItems] = useState<CatalogPlugin[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<CatalogPlugin | null>(null);
  const installed = useMemo(
    () => new Map(plugins.map((plugin) => [plugin.id, plugin])),
    [plugins],
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setItems([]);
    const params = new URLSearchParams();
    if (query.search) params.set("q", query.search);
    if (query.type) params.set("type", query.type);
    void api<{ items: CatalogPlugin[] }>(
      `/plugins/catalog${params.size ? `?${params}` : ""}`,
      { signal: controller.signal },
    )
      .then((result) => {
        if (!controller.signal.aborted) setItems(result.items);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [query]);

  return (
    <section
      className="list-panel plugin-catalog"
      id="plugin-library-panel"
      role="tabpanel"
      aria-labelledby="plugin-library-tab"
    >
      <div className="section-heading">
        <h2>{t("Grafana plugin library")}</h2>
      </div>
      <form
        className="plugin-catalog-filters"
        onSubmit={(event) => {
          event.preventDefault();
          setQuery((current) => ({
            search: search.trim(),
            type,
            retry: current.retry + 1,
          }));
        }}
      >
        <label>
          {t("Search plugins")}
          <input
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            maxLength={200}
            placeholder="grafana-clock-panel"
          />
        </label>
        <label>
          {t("Type")}
          <select
            value={type}
            onChange={(event) => {
              setType(event.target.value);
              setQuery((current) => ({
                ...current,
                type: event.target.value,
              }));
            }}
          >
            <option value="">{t("All types")}</option>
            <option value="panel">{t("panel")}</option>
            <option value="datasource">{t("datasource")}</option>
            <option value="app">{t("app")}</option>
          </select>
        </label>
        <button type="submit">
          <Search size={16} />
          {t("Search")}
        </button>
      </form>
      {loading ? (
        <p className="plugin-catalog-status subtle" role="status">
          {t("Loading…")}
        </p>
      ) : error ? (
        <div className="plugin-catalog-status">
          <p className="form-error" role="alert">
            {error}
          </p>
          <button
            onClick={() =>
              setQuery((current) => ({ ...current, retry: current.retry + 1 }))
            }
          >
            {t("Retry")}
          </button>
        </div>
      ) : items.length === 0 ? (
        <p className="plugin-catalog-status subtle" role="status">
          {t("No plugins match this filter")}
        </p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{t("Name")}</th>
                <th>{t("Type")}</th>
                <th>{t("Latest version")}</th>
                <th className="plugin-catalog-downloads">{t("Downloads")}</th>
                <th>{t("Installed version")}</th>
                <th>{t("Actions")}</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const local = installed.get(item.id);
                return (
                  <tr key={item.id}>
                    <td className="plugin-catalog-name">
                      <button onClick={() => setSelected(item)}>
                        {item.name || item.id}
                      </button>
                      <div className="endpoint mono">{item.id}</div>
                      <p className="plugin-catalog-description">
                        {item.description}
                      </p>
                      <div className="plugin-catalog-flags">
                        {item.deprecated && <span>{t("Deprecated")}</span>}
                        {item.angular_detected && <span>{t("Angular")}</span>}
                        {item.backend && <span>{t("Backend")}</span>}
                      </div>
                    </td>
                    <td>{t(item.type)}</td>
                    <td>
                      <code>{item.version || "—"}</code>
                    </td>
                    <td className="plugin-catalog-downloads">
                      {Number.isFinite(item.downloads)
                        ? item.downloads.toLocaleString()
                        : "—"}
                    </td>
                    <td>
                      {local ? (
                        <>
                          <code>{local.version}</code>
                          {item.version && item.version !== local.version && (
                            <div className="subtle">
                              {t("Available version")}: {item.version}
                            </div>
                          )}
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>
                      <button onClick={() => setSelected(item)}>
                        {t("Details")}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {selected && (
        <PluginDetails
          key={selected.id}
          initial={selected}
          installed={installed.get(selected.id)}
          onClose={() => setSelected(null)}
          onInstalled={async () => {
            await onInstalled();
            setSelected(null);
            notify(t("Plugin installed"));
          }}
        />
      )}
    </section>
  );
}

function PluginDetails({
  initial,
  installed,
  onClose,
  onInstalled,
}: {
  initial: CatalogPlugin;
  installed?: InstalledPlugin;
  onClose: () => void;
  onInstalled: () => Promise<void>;
}) {
  const [plugin, setPlugin] = useState(initial);
  const [versions, setVersions] = useState<CatalogVersion[]>([]);
  const [version, setVersion] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [installError, setInstallError] = useState("");
  const [retry, setRetry] = useState(0);
  const [busy, setBusy] = useState(false);
  const selected = versions.find((item) => item.version === version);
  const sameVersion = installed?.version === version;
  const unsupportedType = !["panel", "datasource", "app"].includes(plugin.type);
  const unavailable =
    unsupportedType ||
    !selected ||
    !selected.platform_available ||
    selected.angular_detected;

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setLoadError("");
    const id = encodeURIComponent(initial.id);
    void Promise.all([
      api<CatalogPlugin>(`/plugin-catalog/${id}`, {
        signal: controller.signal,
      }),
      api<{ items: CatalogVersion[] }>(`/plugin-catalog/${id}/versions`, {
        signal: controller.signal,
      }),
    ])
      .then(([detail, result]) => {
        if (controller.signal.aborted) return;
        setPlugin(detail);
        setVersions(result.items);
        const stable = result.items.find(
          (item) =>
            !item.version.includes("-") &&
            item.platform_available &&
            !item.angular_detected,
        );
        setVersion((stable || result.items[0])?.version || "");
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setLoadError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [initial.id, retry]);

  return (
    <Dialog
      title={`${t("Plugin details")} · ${plugin.name || plugin.id}`}
      onClose={() => {
        if (!busy) onClose();
      }}
      style={{
        width: "min(680px, calc(100vw - 32px))",
        overflowWrap: "anywhere",
      }}
    >
      <div className="plugin-catalog-detail">
        <code>{plugin.id}</code>
        <p>{plugin.description}</p>
        <dl className="plugin-catalog-facts">
          <div>
            <dt>{t("Type")}</dt>
            <dd>{t(plugin.type)}</dd>
          </div>
          <div>
            <dt>{t("Author")}</dt>
            <dd>{plugin.author || "—"}</dd>
          </div>
          {installed && (
            <div>
              <dt>{t("Installed version")}</dt>
              <dd>{installed.version}</dd>
            </div>
          )}
          <div>
            <dt>{t("Backend")}</dt>
            <dd>{t(plugin.backend ? "Included" : "Not included")}</dd>
          </div>
        </dl>
        {plugin.deprecated && (
          <p className="plugin-catalog-warning">{t("Deprecated")}</p>
        )}
        <a
          href={`https://grafana.com/grafana/plugins/${encodeURIComponent(plugin.id)}/`}
          target="_blank"
          rel="noopener noreferrer"
        >
          {t("View on grafana.com")} <ExternalLink size={14} />
        </a>
        {loading ? (
          <p className="subtle" role="status">
            {t("Loading…")}
          </p>
        ) : loadError ? (
          <div>
            <p className="form-error" role="alert">
              {loadError}
            </p>
            <button onClick={() => setRetry((current) => current + 1)}>
              {t("Retry")}
            </button>
          </div>
        ) : (
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              if (busy || unavailable || sameVersion) return;
              setBusy(true);
              setInstallError("");
              try {
                await api("/plugins/catalog", {
                  method: "POST",
                  body: jsonBody({ id: plugin.id, version }),
                });
                await onInstalled();
              } catch (cause) {
                setInstallError(message(cause));
              } finally {
                setBusy(false);
              }
            }}
          >
            <label>
              {t("Version")}
              <select
                value={version}
                disabled={busy || versions.length === 0}
                onChange={(event) => {
                  setVersion(event.target.value);
                  setInstallError("");
                }}
              >
                {versions.length === 0 && <option value="">—</option>}
                {versions.map((item) => (
                  <option key={item.version} value={item.version}>
                    {item.version}
                  </option>
                ))}
              </select>
            </label>
            <dl
              className="plugin-catalog-facts"
              aria-label={t("Version availability")}
            >
              <div>
                <dt>{t("Grafana requirement")}</dt>
                <dd>{selected?.grafana_dependency || "—"}</dd>
              </div>
              <div>
                <dt>{t("Platform package")}</dt>
                <dd>
                  {t(
                    selected?.platform_available ? "Available" : "Unavailable",
                  )}
                </dd>
              </div>
            </dl>
            {selected?.angular_detected && (
              <p className="plugin-catalog-warning" role="status">
                {t("Angular code detected; this version is not supported.")}
              </p>
            )}
            {selected && !selected.platform_available && (
              <p className="plugin-catalog-warning" role="status">
                {t("No package is available for the server platform.")}
              </p>
            )}
            {unsupportedType && (
              <p className="plugin-catalog-warning" role="status">
                {plugin.type === "renderer"
                  ? t("Renderer packages are not supported.")
                  : `${t("Unsupported plugin type")}: ${plugin.type}`}
              </p>
            )}
            <p className="subtle">
              {t(
                "Catalog metadata does not confirm runtime compatibility. Installation verifies the package signature.",
              )}
            </p>
            {installError && (
              <p className="form-error" role="alert">
                {installError}
              </p>
            )}
            <div className="form-actions">
              <button type="button" disabled={busy} onClick={onClose}>
                {t("Cancel")}
              </button>
              <button
                className="primary"
                disabled={busy || unavailable || sameVersion}
              >
                <Download size={16} />
                {t(
                  busy
                    ? "Installing…"
                    : sameVersion
                      ? "Installed version"
                      : installed
                        ? "Update plugin"
                        : "Install plugin",
                )}
              </button>
            </div>
          </form>
        )}
      </div>
    </Dialog>
  );
}
