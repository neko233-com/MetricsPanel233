import { useEffect, useState } from "react";
import {
  ArrowRight,
  Link,
  Pencil,
  Plus,
  Search,
  Trash2,
  Unlink,
} from "lucide-react";
import {
  api,
  apiText,
  dashboardUID,
  jsonBody,
  message,
  type Dashboard,
  type Folder,
  type LibraryConnection,
  type LibraryPanel,
  type Panel,
} from "../api";
import { Dialog } from "../components/Dialog";
import { t } from "../i18n";
import { FolderSelect, folderTrail } from "./Folders";

type Props = {
  folders: Folder[];
  dashboards: Dashboard[];
  reload: () => Promise<void>;
  open: (dashboard: Dashboard) => void;
  notify: (text: string, error?: boolean) => void;
};
type Action = {
  kind: "create" | "edit" | "delete" | "insert" | "connections";
  item?: LibraryPanel;
};
type Page = {
  totalCount: number;
  elements: LibraryPanel[];
  page: number;
  perPage: number;
};

function libraryUID(panel: Panel): string | undefined {
  return (panel.config as { libraryPanel?: { uid?: string } } | undefined)
    ?.libraryPanel?.uid;
}

// Preserve the raw model, including unknown integers beyond JavaScript's range.
function modelBody(raw: string, fields: Record<string, unknown>): string {
  const parsed: unknown = JSON.parse(raw);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object")
    throw new Error(t("Panel model must be a JSON object."));
  if (new Blob([raw]).size > 512 * 1024)
    throw new Error(t("Panel model must be under 512 KiB."));
  return `{"model":${raw},${JSON.stringify(fields).slice(1, -1)}}`;
}

export function LibraryPanels({
  folders,
  dashboards,
  reload,
  open,
  notify,
}: Props) {
  const [search, setSearch] = useState(""),
    [folder, setFolder] = useState("*");
  const [query, setQuery] = useState({
    search: "",
    folder: "*",
    page: 1,
    revision: 0,
  });
  const [data, setData] = useState<Page | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [action, setAction] = useState<Action | null>(null);
  const refresh = () =>
    setQuery((old) => ({ ...old, revision: old.revision + 1 }));
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    const params = new URLSearchParams({
      kind: "1",
      page: String(query.page),
      perPage: "20",
    });
    if (query.search) params.set("searchString", query.search);
    if (query.folder !== "*")
      params.set("folderFilterUIDs", query.folder || "general");
    void api<{ result: Page }>(`/api/library-elements?${params}`, {
      signal: controller.signal,
    })
      .then((result) => {
        if (!controller.signal.aborted) {
          setData(result.result);
          const lastPage = Math.max(
            1,
            Math.ceil(result.result.totalCount / result.result.perPage),
          );
          if (query.page > lastPage)
            setQuery((old) => ({ ...old, page: lastPage }));
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [query]);
  const changed = async (caption: string) => {
    await reload();
    refresh();
    setAction(null);
    notify(t(caption));
  };
  return (
    <section
      className="list-panel library-panels"
      id="library-panels-panel"
      role="tabpanel"
      aria-labelledby="library-panels-tab"
    >
      <div className="section-heading">
        <h2>{t("Library panels")}</h2>
        <button
          className="primary"
          onClick={() => setAction({ kind: "create" })}
        >
          <Plus size={17} />
          {t("New library panel")}
        </button>
      </div>
      <form
        className="library-filters"
        onSubmit={(event) => {
          event.preventDefault();
          setQuery((old) => ({
            search: search.trim(),
            folder,
            page: 1,
            revision: old.revision + 1,
          }));
        }}
      >
        <label>
          {t("Search library panels")}
          <input
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            maxLength={200}
          />
        </label>
        <label>
          {t("Folder")}
          <select
            value={folder}
            onChange={(event) => setFolder(event.target.value)}
          >
            <option value="*">{t("All folders")}</option>
            <option value="">{t("General")}</option>
            {folders.map((item) => (
              <option key={item.uid} value={item.uid}>
                {folderTrail(item.uid, folders)
                  .map((node) => node.title)
                  .join(" / ")}
              </option>
            ))}
          </select>
        </label>
        <button>
          <Search size={16} />
          {t("Search")}
        </button>
      </form>
      {loading ? (
        <p className="library-status" role="status">
          {t("Loading library panels…")}
        </p>
      ) : error ? (
        <div className="library-status">
          <p className="form-error" role="alert">
            {error}
          </p>
          <button onClick={refresh}>{t("Retry")}</button>
        </div>
      ) : !data?.elements.length ? (
        <p className="library-status">{t("No library panels found.")}</p>
      ) : (
        <>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("Panel")}</th>
                  <th>{t("Folder")}</th>
                  <th>{t("Version")}</th>
                  <th>{t("Connections")}</th>
                  <th>{t("Actions")}</th>
                </tr>
              </thead>
              <tbody>
                {data.elements.map((item) => (
                  <tr key={item.uid}>
                    <td>
                      <button
                        className="dashboard-title"
                        onClick={() => setAction({ kind: "edit", item })}
                      >
                        {item.name}
                        <Pencil size={15} />
                      </button>
                      <div className="subtle mono">
                        {item.uid} ·{" "}
                        {item.type || String(item.model.type || "")}
                      </div>
                      <div className="library-mobile-meta subtle">
                        {folderTrail(item.folderUid, folders)
                          .map((node) => node.title)
                          .join(" / ") || t("General")}{" "}
                        · v{item.version}
                      </div>
                    </td>
                    <td>
                      {folderTrail(item.folderUid, folders)
                        .map((node) => node.title)
                        .join(" / ") || t("General")}
                    </td>
                    <td>{item.version}</td>
                    <td>
                      <button
                        aria-label={`${t("Connections")} ${item.name}`}
                        onClick={() => setAction({ kind: "connections", item })}
                      >
                        {item.meta?.connectedDashboards || 0}
                      </button>
                    </td>
                    <td>
                      <div className="row-actions">
                        <button
                          className="icon-button"
                          title={t("Add to dashboard")}
                          aria-label={`${t("Add to dashboard")} ${item.name}`}
                          onClick={() => setAction({ kind: "insert", item })}
                        >
                          <Plus size={17} />
                        </button>
                        <button
                          className="icon-button"
                          title={t("Edit library panel")}
                          aria-label={`${t("Edit library panel")} ${item.name}`}
                          onClick={() => setAction({ kind: "edit", item })}
                        >
                          <Pencil size={17} />
                        </button>
                        <button
                          className="icon-button danger-hover"
                          title={t("Delete library panel")}
                          aria-label={`${t("Delete library panel")} ${item.name}`}
                          onClick={() => setAction({ kind: "delete", item })}
                        >
                          <Trash2 size={17} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="library-pagination">
            <span className="subtle">
              {data.totalCount} {t("panels")} · {t("Page")} {query.page}
            </span>
            <div className="row-actions">
              <button
                disabled={query.page <= 1}
                onClick={() =>
                  setQuery((old) => ({ ...old, page: old.page - 1 }))
                }
              >
                {t("Previous")}
              </button>
              <button
                disabled={query.page * data.perPage >= data.totalCount}
                onClick={() =>
                  setQuery((old) => ({ ...old, page: old.page + 1 }))
                }
              >
                {t("Next")}
              </button>
            </div>
          </div>
        </>
      )}
      {action?.kind === "create" || action?.kind === "edit" ? (
        <ModelDialog
          item={action.item}
          folders={folders}
          close={() => setAction(null)}
          saved={() => changed("Library panel saved")}
        />
      ) : action?.kind === "delete" ? (
        <DeleteDialog
          item={action.item!}
          close={() => setAction(null)}
          deleted={() => changed("Library panel deleted")}
        />
      ) : action?.kind === "insert" ? (
        <InsertDialog
          item={action.item!}
          dashboards={dashboards}
          folders={folders}
          close={() => setAction(null)}
          saved={() => changed("Library panel added")}
        />
      ) : action?.kind === "connections" ? (
        <ConnectionsDialog
          item={action.item!}
          dashboards={dashboards}
          reload={async () => {
            await reload();
            refresh();
          }}
          open={open}
          close={() => setAction(null)}
          notify={notify}
        />
      ) : null}
    </section>
  );
}

const defaultModel = JSON.stringify(
  {
    type: "timeseries",
    title: "Panel",
    targets: [{ refId: "A", expr: "up" }],
    fieldConfig: { defaults: {}, overrides: [] },
    options: {},
  },
  null,
  2,
);
function ModelDialog({
  item,
  folders,
  close,
  saved,
}: {
  item?: LibraryPanel;
  folders: Folder[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [name, setName] = useState(item?.name || ""),
    [uid, setUID] = useState("");
  const [folder, setFolder] = useState(item?.folderUid || ""),
    [raw, setRaw] = useState(item ? "" : defaultModel);
  const [version, setVersion] = useState(item?.version),
    [loading, setLoading] = useState(Boolean(item)),
    [retry, setRetry] = useState(0);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    if (!item) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void (async () => {
      const path = `/api/library-elements/${encodeURIComponent(item.uid)}`;
      const details = await api<{ result: LibraryPanel }>(path, {
        signal: controller.signal,
      });
      const model = await apiText(
        `/library-models/${encodeURIComponent(item.uid)}`,
        { signal: controller.signal },
      );
      const current = await api<{ result: LibraryPanel }>(path, {
        signal: controller.signal,
      });
      if (details.result.version !== current.result.version)
        throw new Error(
          t("Library panel changed while loading; reload the model."),
        );
      return { details, model };
    })()
      .then(({ details, model }) => {
        if (!controller.signal.aborted) {
          setName(details.result.name);
          setFolder(details.result.folderUid);
          setVersion(details.result.version);
          setRaw(model);
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [item, retry]);
  return (
    <Dialog
      title={t(item ? "Edit library panel" : "New library panel")}
      onClose={() => {
        if (!busy) close();
      }}
      style={{ width: 720 }}
    >
      {loading && <p role="status">{t("Loading panel model…")}</p>}
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setError("");
          try {
            const fields = item
              ? { name, kind: 1, folderUid: folder, version }
              : {
                  name,
                  kind: 1,
                  folderUid: folder,
                  ...(uid.trim() ? { uid: uid.trim() } : {}),
                };
            await api(
              `/api/library-elements${item ? `/${encodeURIComponent(item.uid)}` : ""}`,
              { method: item ? "PATCH" : "POST", body: modelBody(raw, fields) },
            );
            await saved();
          } catch (cause) {
            setError(message(cause));
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="form-row">
          <label>
            {t("Library panel name")}
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
              maxLength={100}
              disabled={busy || loading}
            />
          </label>
          <label>
            {t("Folder")}
            <FolderSelect
              folders={folders}
              value={folder}
              onChange={setFolder}
              disabled={busy || loading}
            />
          </label>
        </div>
        {!item && (
          <label>
            {t("Library panel UID (optional)")}
            <input
              value={uid}
              onChange={(event) => setUID(event.target.value)}
              pattern="(?:[A-Za-z0-9_]|-){1,40}"
              maxLength={40}
              disabled={busy}
            />
          </label>
        )}
        <label>
          {t("Panel model JSON")}
          <textarea
            className="mono library-model"
            rows={13}
            value={raw}
            onChange={(event) => setRaw(event.target.value)}
            required
            disabled={busy || loading}
            spellCheck={false}
          />
        </label>
        {item && (
          <p className="subtle">
            {t(
              "Changes to this model update linked panels in every dashboard.",
            )}
          </p>
        )}
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          {item && (
            <button
              type="button"
              disabled={busy || loading}
              onClick={() => {
                setLoading(true);
                setRetry((old) => old + 1);
              }}
            >
              {t("Reload model")}
            </button>
          )}
          <button type="button" disabled={busy} onClick={close}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy || loading || !raw}>
            {t(busy ? "Saving…" : "Save library panel")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function DeleteDialog({
  item,
  close,
  deleted,
}: {
  item: LibraryPanel;
  close: () => void;
  deleted: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Dialog
      title={t("Delete library panel")}
      onClose={() => {
        if (!busy) close();
      }}
    >
      <p>
        <strong>{item.name}</strong>
      </p>
      <p>{t("Unlink this panel from every dashboard before deleting it.")}</p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions">
        <button disabled={busy} onClick={close}>
          {t("Cancel")}
        </button>
        <button
          className="danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            setError("");
            try {
              await api(
                `/api/library-elements/${encodeURIComponent(item.uid)}`,
                { method: "DELETE" },
              );
              await deleted();
            } catch (cause) {
              setError(message(cause));
            } finally {
              setBusy(false);
            }
          }}
        >
          {t(busy ? "Deleting…" : "Delete library panel")}
        </button>
      </div>
    </Dialog>
  );
}

function InsertDialog({
  item,
  dashboards,
  folders,
  close,
  saved,
}: {
  item: LibraryPanel;
  dashboards: Dashboard[];
  folders: Folder[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [id, setID] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Dialog
      title={t("Add to dashboard")}
      onClose={() => {
        if (!busy) close();
      }}
    >
      <p>
        <strong>{item.name}</strong>
      </p>
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setError("");
          try {
            const dashboard = dashboards.find(
              (candidate) => candidate.id === id,
            );
            if (!dashboard) throw new Error(t("Select a dashboard."));
            await api(`/dashboard-library/${encodeURIComponent(id)}`, {
              method: "POST",
              body: jsonBody({
                libraryUid: item.uid,
                revision: dashboard.updated_at,
              }),
            });
            await saved();
          } catch (cause) {
            setError(message(cause));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {t("Dashboard")}
          <select
            value={id}
            onChange={(event) => setID(event.target.value)}
            required
            disabled={busy}
          >
            <option value="">{t("Select a dashboard")}</option>
            {dashboards.map((dashboard) => (
              <option value={dashboard.id} key={dashboard.id}>
                {dashboard.name} ·{" "}
                {folderTrail(dashboard.folder_uid || "", folders)
                  .map((node) => node.title)
                  .join(" / ") || t("General")}
              </option>
            ))}
          </select>
        </label>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={busy} onClick={close}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy || !id}>
            <Link size={16} />
            {t(busy ? "Saving…" : "Add library panel")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function ConnectionsDialog({
  item,
  dashboards,
  reload,
  open,
  close,
  notify,
}: {
  item: LibraryPanel;
  dashboards: Dashboard[];
  reload: () => Promise<void>;
  open: (dashboard: Dashboard) => void;
  close: () => void;
  notify: Props["notify"];
}) {
  const [connections, setConnections] = useState<LibraryConnection[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [revision, setRevision] = useState(0),
    [busy, setBusy] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void api<{ result: LibraryConnection[] }>(
      `/api/library-elements/${encodeURIComponent(item.uid)}/connections`,
      { signal: controller.signal },
    )
      .then((result) => {
        if (!controller.signal.aborted) setConnections(result.result);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [item.uid, revision]);
  return (
    <Dialog
      title={t("Panel connections")}
      onClose={() => {
        if (!busy) close();
      }}
      style={{ width: 680 }}
    >
      <p>
        <strong>{item.name}</strong>
      </p>
      {loading ? (
        <p role="status">{t("Loading connections…")}</p>
      ) : !connections.length && !error ? (
        <p>{t("This panel is not connected to a dashboard.")}</p>
      ) : (
        <ul className="library-connections">
          {connections.map((connection) => {
            const dashboard = dashboards.find(
              (candidate) =>
                candidate.id === connection.dashboardUid ||
                dashboardUID(candidate) === connection.connectionUid ||
                dashboardUID(candidate) === connection.dashboardUid,
            );
            const panels =
              dashboard?.panels.filter(
                (panel) => libraryUID(panel) === item.uid,
              ) || [];
            return (
              <li key={connection.connectionUid}>
                <div className="library-connection-heading">
                  {dashboard ? (
                    <button
                      className="dashboard-title"
                      onClick={() => {
                        close();
                        open(dashboard);
                      }}
                    >
                      {dashboard.name}
                      <ArrowRight size={15} />
                    </button>
                  ) : (
                    <strong>
                      {connection.name || connection.connectionUid}
                    </strong>
                  )}
                  <span className="subtle mono">
                    {connection.connectionUid}
                  </span>
                </div>
                {panels.map((panel) => (
                  <div className="library-linked-panel" key={panel.id}>
                    <span>{panel.title}</span>
                    <button
                      disabled={Boolean(busy)}
                      aria-label={`${t("Unlink panel")} ${panel.title} ${dashboard!.name}`}
                      onClick={async () => {
                        setBusy(`${dashboard!.id}/${panel.id}`);
                        setError("");
                        try {
                          await api(
                            `/dashboard-library/${encodeURIComponent(dashboard!.id)}/unlink`,
                            {
                              method: "POST",
                              body: jsonBody({
                                panelId: panel.id,
                                revision: dashboard!.updated_at,
                              }),
                            },
                          );
                          await reload();
                          setRevision((old) => old + 1);
                          notify(t("Panel unlinked"));
                        } catch (cause) {
                          setError(message(cause));
                        } finally {
                          setBusy("");
                        }
                      }}
                    >
                      <Unlink size={14} />
                      {t("Unlink")}
                    </button>
                  </div>
                ))}
              </li>
            );
          })}
        </ul>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions">
        <button
          disabled={Boolean(busy) || loading}
          onClick={async () => {
            try {
              await reload();
              setRevision((old) => old + 1);
            } catch (cause) {
              setError(message(cause));
            }
          }}
        >
          {t("Refresh")}
        </button>
        <button disabled={Boolean(busy)} onClick={close}>
          {t("Close")}
        </button>
      </div>
    </Dialog>
  );
}
