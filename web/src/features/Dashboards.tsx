import { t as tr } from "../i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowRight, Download, Folder as FolderIcon, Move, Plus, Trash2, Upload } from "lucide-react";
import { Button } from "../ui";
import { useSession } from "../ui/session";
import { api, downloadRaw, jsonBody, message, type Dashboard, type Folder } from "../api";
import { Dialog } from "../components/Dialog";
import { DashboardImport } from "./DashboardImport";
import { grafanaImportBody } from "./grafana-import-body";
import { Folders, FolderBreadcrumbs, FolderSelect, folderTrail } from "./Folders";
import { LibraryPanels } from "./LibraryPanels";
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
  const { canEdit } = useSession();
  const [creating, setCreating] = useState(false),
    [name, setName] = useState(""),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false),
    [exporting, setExporting] = useState(""),
    [removing, setRemoving] = useState<Dashboard | null>(null),
    [importing, setImporting] = useState<{ initial?: string } | null>(null);
  const [tab, setTab] = useState<"dashboards" | "library">("dashboards");
  const [folders, setFolders] = useState<Folder[]>([]), [folderLoading, setFolderLoading] = useState(true), [folderError, setFolderError] = useState("");
  const [selectedFolder, setSelectedFolder] = useState<string | null>(null), [creationFolder, setCreationFolder] = useState("");
  const [moving, setMoving] = useState<Dashboard | null>(null);
  const [search, setSearch] = useState("");
  const folderRequest = useRef<AbortController | null>(null);
  const loadFolders = useCallback(async () => {
    folderRequest.current?.abort();
    const controller = new AbortController(); folderRequest.current = controller;
    setFolderLoading(true); setFolderError("");
    try {
      const result = await api<Folder[]>("/folders", { signal: controller.signal });
      if (!controller.signal.aborted) setFolders(result);
    } catch (cause) { if (!controller.signal.aborted) { setFolderError(message(cause)); throw cause; } }
    finally { if (!controller.signal.aborted) setFolderLoading(false); }
  }, []);
  useEffect(() => { void loadFolders().catch(() => {}); return () => folderRequest.current?.abort(); }, [loadFolders]);
  const visibleDashboards = dashboards.filter((dashboard) => (selectedFolder === null || (dashboard.folder_uid || "") === selectedFolder) && dashboard.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()));
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{tr("Dashboards")}</h1>
          <p>{dashboards.length} {tr("dashboards")} · {folders.length} {tr("folders")}</p>
        </div>
        {tab === "dashboards" && canEdit && <div className="toolbar">
          <Button onClick={() => setImporting({})}>
            <Upload size={17} />
            {tr("Import dashboard")}
          </Button>
          <Button onClick={() => input.current?.click()}>
            <Upload size={17} />
            {tr("Import JSON")}
          </Button>
          <Button
            variant="primary"
            onClick={() => {
              setCreating(true);
              setName("");
              setError("");
              setCreationFolder(selectedFolder || "");
            }}
          >
            <Plus size={18} />
            {tr("New dashboard")}
          </Button>
        </div>}
      </div>
      <div className="dashboard-tabs" role="tablist" aria-label={tr("Dashboard resources")} onKeyDown={(event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault(); const next = event.key === "Home" ? "dashboards" : event.key === "End" ? "library" : tab === "dashboards" ? "library" : "dashboards";
        setTab(next); event.currentTarget.querySelector<HTMLButtonElement>(`#${next === "dashboards" ? "dashboard-browser" : "library-panels"}-tab`)?.focus();
      }}>
        <button role="tab" id="dashboard-browser-tab" aria-controls="dashboard-browser-panel" aria-selected={tab === "dashboards"} tabIndex={tab === "dashboards" ? 0 : -1} onClick={() => setTab("dashboards")}>{tr("Dashboards")}</button>
        <button role="tab" id="library-panels-tab" aria-controls="library-panels-panel" aria-selected={tab === "library"} tabIndex={tab === "library" ? 0 : -1} onClick={() => setTab("library")}>{tr("Library panels")}</button>
      </div>
      {folderError && <div className="folder-load-error"><p className="form-error" role="alert">{folderError}</p><button onClick={() => void loadFolders().catch(() => {})}>{tr("Retry")}</button></div>}
      <input
        ref={input}
        type="file"
        className="dashboard-file-input"
        accept=".json,application/json"
        hidden
        onChange={async (e) => {
          const file = e.target.files?.[0];
          if (!file) return;
          try {
            if (file.size > 4 * 1024 * 1024)
              throw new Error("Import must be under 4 MiB");
            const text = await file.text();
            const data = JSON.parse(text) as Dashboard & {
              title?: string;
              dashboard?: unknown;
              spec?: unknown;
            };
            let imported: Dashboard;
            if (data.title || data.dashboard || data.spec) {
              const preview = await api<{ inputs: unknown[] }>(
                "/import/grafana-preview",
                {
                  method: "POST",
                  body: grafanaImportBody(text),
                },
              );
              if (preview.inputs.length) {
                setImporting({ initial: text });
                return;
              }
              const result = await api<{
                dashboard: Dashboard;
                warnings: string[];
              }>("/import/grafana", { method: "POST", body: text });
              imported = result.dashboard;
              if (result.warnings.length) {
                window.alert(
                  `${tr("Unsupported template features")}:\n${result.warnings.join("\n")}`,
                );
              }
            } else
              imported = await api<Dashboard>("/dashboards", {
                method: "POST",
                body: text,
              });
            if (selectedFolder) imported = await api<Dashboard>(`/dashboard-folders/${encodeURIComponent(imported.id)}`, { method: "PUT", body: jsonBody({ folderUid: selectedFolder, revision: imported.updated_at }) });
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
      {importing && (
        <DashboardImport
          initial={importing.initial}
          folders={folders}
          defaultFolder={selectedFolder || ""}
          close={() => setImporting(null)}
          imported={async (dashboard, warnings) => {
            await reload();
            setImporting(null);
            open(dashboard);
            notify(tr("Dashboard imported"));
            if (warnings.length) notify(warnings.join("\n"), true);
          }}
        />
      )}
      {tab === "library" ? <LibraryPanels folders={folders} dashboards={dashboards} reload={reload} open={open} notify={notify} /> : <div className="dashboard-browser" id="dashboard-browser-panel" role="tabpanel" aria-labelledby="dashboard-browser-tab">
      <div>{folderLoading ? <p className="subtle" role="status">{tr("Loading folders…")}</p> : <Folders folders={folders} selected={selectedFolder} select={setSelectedFolder} changed={async () => { await Promise.all([loadFolders(), reload()]); }} notify={notify} />}</div>
      <div className="dashboard-folder-content">
      <FolderBreadcrumbs folders={folders} selected={selectedFolder} select={setSelectedFolder} />
      <label className="dashboard-search">{tr("Search dashboards")}<input type="search" value={search} onChange={(event) => setSearch(event.target.value)} maxLength={200} /></label>
      {selectedFolder !== null && folders.some((folder) => (folder.parentUid || "") === selectedFolder) && <div className="folder-children">{folders.filter((folder) => (folder.parentUid || "") === selectedFolder).map((folder) => <button key={folder.uid} onClick={() => setSelectedFolder(folder.uid)}><FolderIcon size={17} />{folder.title}<ArrowRight size={15} /></button>)}</div>}
      <section className="list-panel">
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{tr("Dashboard")}</th>
                <th>{tr("Folder")}</th>
                <th>{tr("Panels")}</th>
                <th>{tr("Last updated")}</th>
                <th>{tr("Actions")}</th>
              </tr>
            </thead>
            <tbody>
              {visibleDashboards.map((d) => (
                <tr key={d.id}>
                  <td>
                    <button className="dashboard-title" onClick={() => open(d)}>
                      {tr(d.name)}
                      <ArrowRight size={17} />
                    </button>
                    <div className="subtle mono">{d.id}</div>
                    <div className="dashboard-mobile-meta subtle">{folderTrail(d.folder_uid || "", folders).map((folder) => folder.title).join(" / ") || tr("General")} · {new Date(d.updated_at).toLocaleString()}</div>
                    {(d.grafana as { snapshot?: unknown } | undefined)
                      ?.snapshot ? (
                      <span className="subtle">{tr("Snapshot")}</span>
                    ) : null}
                  </td>
                  <td>{folderTrail(d.folder_uid || "", folders).map((folder) => folder.title).join(" / ") || tr("General")}</td>
                  <td>{d.panels.length}</td>
                  <td>{new Date(d.updated_at).toLocaleString()}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        aria-label={`Export ${tr(d.name)}`}
                        title={tr("Export JSON")}
                        className="icon-button"
                        disabled={Boolean(exporting)}
                        onClick={async () => {
                          setExporting(d.id);
                          try {
                            await downloadRaw(
                              `${d.id}.json`,
                              `/dashboard-exports/${encodeURIComponent(d.id)}?format=native`,
                            );
                          } catch (e) {
                            notify(message(e), true);
                          } finally {
                            setExporting("");
                          }
                        }}
                      >
                        <Download size={17} />
                      </button>
                      <button className="icon-button" aria-label={`${tr("Move dashboard")} ${tr(d.name)}`} title={tr("Move dashboard")} onClick={() => setMoving(d)}><Move size={17} /></button>
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
      {!visibleDashboards.length && <p className="dashboard-empty">{tr("No dashboards in this folder.")}</p>}
      </div></div>}
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
                  body: jsonBody({ name, panels: [], folder_uid: creationFolder }),
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
                maxLength={5000}
              />
            </label>
            <label>{tr("Folder")}<FolderSelect folders={folders} value={creationFolder} onChange={setCreationFolder} disabled={saving || folderLoading} /></label>
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
      {moving && <MoveDashboard dashboard={moving} folders={folders} close={() => setMoving(null)} moved={async () => { await reload(); setMoving(null); notify(tr("Dashboard moved")); }} />}
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

function MoveDashboard({ dashboard, folders, close, moved }: { dashboard: Dashboard; folders: Folder[]; close: () => void; moved: () => Promise<void> }) {
  const [folder, setFolder] = useState(dashboard.folder_uid || ""), [busy, setBusy] = useState(false), [error, setError] = useState("");
  return <Dialog title={tr("Move dashboard")} onClose={() => { if (!busy) close(); }}><p><strong>{dashboard.name}</strong></p><form onSubmit={async (event) => { event.preventDefault(); setBusy(true); setError(""); try { await api(`/dashboard-folders/${encodeURIComponent(dashboard.id)}`, { method: "PUT", body: jsonBody({ folderUid: folder, revision: dashboard.updated_at }) }); await moved(); } catch (cause) { setError(message(cause)); } finally { setBusy(false); } }}><label>{tr("Folder")}<FolderSelect folders={folders} value={folder} onChange={setFolder} disabled={busy} /></label>{error && <p className="form-error" role="alert">{error}</p>}<div className="form-actions"><button type="button" disabled={busy} onClick={close}>{tr("Cancel")}</button><button className="primary" disabled={busy}>{tr(busy ? "Saving…" : "Move dashboard")}</button></div></form></Dialog>;
}
