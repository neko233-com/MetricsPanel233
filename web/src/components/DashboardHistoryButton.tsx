import { useEffect, useRef, useState } from "react";
import { Download, History } from "lucide-react";
import {
  api,
  apiText,
  ApiError,
  dashboardUID,
  downloadRaw,
  jsonBody,
  message,
  type Dashboard,
  type DashboardVersion,
  type DashboardVersions,
} from "../api";
import { getLocale, t } from "../i18n";
import { Dialog } from "./Dialog";

const pageSize = 20;
const validVersion = (value: string) => /^[1-9]\d*$/.test(value);

export function DashboardHistoryButton({
  dashboard,
  reload,
}: {
  dashboard: Dashboard;
  reload: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>
        <History size={17} />
        {t("Version history")}
      </button>
      {open && (
        <DashboardHistory
          dashboard={dashboard}
          reload={reload}
          close={() => setOpen(false)}
        />
      )}
    </>
  );
}

function DashboardHistory({
  dashboard,
  reload,
  close,
}: {
  dashboard: Dashboard;
  reload: () => Promise<void>;
  close: () => void;
}) {
  const baseline = useRef(dashboard.updated_at);
  const [page, setPage] = useState(0),
    [cursors, setCursors] = useState([""]),
    [attempt, setAttempt] = useState(0);
  const [versions, setVersions] = useState<DashboardVersion[]>([]);
  const [next, setNext] = useState(""),
    [loading, setLoading] = useState(true);
  const [listError, setListError] = useState("");
  const [current, setCurrent] = useState("");
  const [base, setBase] = useState(""),
    [newer, setNewer] = useState("");
  const [format, setFormat] = useState<"native" | "grafana">("native");
  const [viewed, setViewed] = useState("");
  const [document, setDocument] = useState("");
  const [comparison, setComparison] = useState("");
  const [restore, setRestore] = useState<DashboardVersion | null>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(""),
    [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => {
    const abort = new AbortController();
    setLoading(true);
    setListError("");
    const query = new URLSearchParams({ limit: String(pageSize) });
    if (cursors[page]) query.set("continueToken", cursors[page]);
    else query.set("start", "0");
    void api<DashboardVersions>(
      `/dashboard-versions/${encodeURIComponent(dashboard.id)}?${query}`,
      { signal: abort.signal },
    )
      .then((result) => {
        if (abort.signal.aborted) return;
        setVersions(result.versions);
        setNext(result.continueToken);
        setCurrent(result.currentVersionKey);
        if (page === 0 && result.versions.length) {
          setBase(
            (old) =>
              old ||
              result.versions[1]?.versionKey ||
              result.versions[0].versionKey,
          );
          setNewer((old) => old || result.versions[0].versionKey);
        }
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setListError(message(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [dashboard.id, page, cursors, attempt]);

  const exportPath = (version: string, kind = format) =>
    `/dashboard-version-exports/${encodeURIComponent(dashboard.id)}/${encodeURIComponent(version)}?format=${kind}`;
  const run = async (
    operation: string,
    action: (signal: AbortSignal) => Promise<void>,
  ) => {
    request.current?.abort();
    const abort = new AbortController();
    request.current = abort;
    setBusy(operation);
    setError("");
    try {
      await action(abort.signal);
    } catch (cause) {
      if (!abort.signal.aborted) {
        setError(message(cause));
        if (cause instanceof ApiError && cause.status === 409)
          setConflict(true);
      }
    } finally {
      if (!abort.signal.aborted) setBusy("");
    }
  };
  const view = (version: string) =>
    void run("view", async (signal) => {
      setDocument("");
      setViewed(version);
      const raw = await apiText(exportPath(version), { signal });
      if (!signal.aborted) setDocument(raw);
    });
  const changePage = (value: number) => {
    setLoading(true);
    if (value > page) setCursors((old) => [...old.slice(0, page + 1), next]);
    setPage(value);
    setRestore(null);
  };
  const leave = () => {
    if (busy !== "restore") {
      request.current?.abort();
      close();
    }
  };
  return (
    <Dialog
      title={t("Version history")}
      onClose={leave}
      style={{ width: 1040 }}
    >
      <div className="dashboard-history">
        <p className="subtle history-dashboard-name">{dashboard.name}</p>
        {listError ? (
          <div className="history-status">
            <p className="form-error" role="alert">
              {t(listError)}
            </p>
            <button
              disabled={loading || Boolean(busy)}
              onClick={() => {
                setLoading(true);
                setAttempt((value) => value + 1);
              }}
            >
              {t("Retry")}
            </button>
          </div>
        ) : loading ? (
          <p role="status" className="history-status">
            {t("Loading versions…")}
          </p>
        ) : !versions.length ? (
          <p className="history-status">{t("No saved versions")}</p>
        ) : (
          <>
            <div className="table-scroll history-table">
              <table>
                <thead>
                  <tr>
                    <th>{t("Version")}</th>
                    <th>{t("Saved")}</th>
                    <th>{t("Saved by")}</th>
                    <th>{t("Change")}</th>
                    <th>{t("Actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {versions.map((item) => (
                    <tr key={item.versionKey}>
                      <td>
                        <strong className="mono">{item.versionKey}</strong>
                        {item.versionKey === current && (
                          <small className="history-current">
                            {t("Current version")}
                          </small>
                        )}
                        <small className="history-mobile-meta">
                          {new Date(item.created).toLocaleString(
                            getLocale() === "zh" ? "zh-CN" : "en-US",
                          )}{" "}
                          · {item.createdBy}
                        </small>
                      </td>
                      <td>
                        <time dateTime={item.created}>
                          {new Date(item.created).toLocaleString(
                            getLocale() === "zh" ? "zh-CN" : "en-US",
                          )}
                        </time>
                      </td>
                      <td>{item.createdBy || "—"}</td>
                      <td>
                        {item.message === "Current version imported during upgrade" ? t(item.message) : item.message || "—"}
                        {item.restoredFromKey !== "0" &&
                          item.restoredFromKey && (
                            <small className="history-restored">
                              {t("Restored from version")}{" "}
                              {item.restoredFromKey}
                            </small>
                          )}
                      </td>
                      <td>
                        <div className="history-row-actions">
                          <button
                            disabled={Boolean(busy)}
                            aria-label={`${t("View version")} ${item.versionKey}`}
                            onClick={() => view(item.versionKey)}
                          >
                            {t("View JSON")}
                          </button>
                          <button
                            disabled={
                              Boolean(busy) ||
                              conflict ||
                              item.versionKey === current
                            }
                            aria-label={`${t("Restore version")} ${item.versionKey}`}
                            onClick={() => {
                              setRestore(item);
                              setNote("");
                              setError("");
                            }}
                          >
                            {t("Restore")}
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="history-pagination">
              <button
                disabled={!page || loading || Boolean(busy)}
                onClick={() => changePage(page - 1)}
              >
                {t("Previous")}
              </button>
              <span>
                {t("Page")} {page + 1}
              </span>
              <button
                disabled={!next || loading || Boolean(busy)}
                onClick={() => changePage(page + 1)}
              >
                {t("Next")}
              </button>
            </div>
          </>
        )}
        <fieldset
          className="history-controls"
          disabled={
            Boolean(busy) || loading || Boolean(listError) || !versions.length
          }
        >
          <legend>{t("Compare versions")}</legend>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              setRestore(null);
              void run("compare", async (signal) => {
                setComparison("");
                const raw = await apiText(
                  `/dashboard-compare/${encodeURIComponent(dashboard.id)}`,
                  {
                    method: "POST",
                    body: jsonBody({ baseVersion: base, newVersion: newer }),
                    signal,
                  },
                );
                if (!signal.aborted) setComparison(raw);
              });
            }}
          >
            <div className="history-compare-fields">
              <label>
                {t("Base version")}
                <input
                  className="mono"
                  aria-label={t("Base version")}
                  value={base}
                  onChange={(event) => {
                    setBase(event.target.value);
                    setComparison("");
                  }}
                  inputMode="numeric"
                  pattern="[1-9][0-9]*"
                  required
                />
              </label>
              <label>
                {t("Compare version")}
                <input
                  className="mono"
                  aria-label={t("Compare version")}
                  value={newer}
                  onChange={(event) => {
                    setNewer(event.target.value);
                    setComparison("");
                  }}
                  inputMode="numeric"
                  pattern="[1-9][0-9]*"
                  required
                />
              </label>
              <button disabled={!validVersion(base) || !validVersion(newer)}>
                {t(busy === "compare" ? "Comparing…" : "Compare")}
              </button>
            </div>
          </form>
        </fieldset>
        {comparison && (
          <section className="history-document">
            <h3>{t("Version changes")}</h3>
            <p className="subtle">
              {t("Paths and JSON values returned by the server.")}
            </p>
            <pre className="mono" aria-label={t("Version changes")}>
              {comparison}
            </pre>
          </section>
        )}
        {viewed && (
          <section className="history-document">
            <div className="history-document-heading">
              <h3>
                {t("Version")} {viewed}
              </h3>
              <label>
                {t("Export format")}
                <select
                  aria-label={t("Export format")}
                  value={format}
                  disabled={Boolean(busy)}
                  onChange={(event) => {
                    setFormat(event.target.value as "native" | "grafana");
                    setDocument("");
                  }}
                >
                  <option value="native">{t("Native JSON")}</option>
                  <option value="grafana">{t("Grafana JSON")}</option>
                </select>
              </label>
              <button disabled={Boolean(busy)} onClick={() => view(viewed)}>
                {t("View JSON")}
              </button>
              <button
                disabled={Boolean(busy)}
                onClick={() =>
                  void run("download", async (signal) =>
                    downloadRaw(
                      `${dashboard.id}-v${viewed}-${format}.json`,
                      exportPath(viewed),
                      { signal },
                    ),
                  )
                }
              >
                <Download size={15} />
                {t("Download JSON")}
              </button>
            </div>
            {busy === "view" && (
              <p className="subtle" role="status">
                {t("Loading JSON…")}
              </p>
            )}
            {busy === "download" && (
              <p className="subtle" role="status">
                {t("Downloading…")}
              </p>
            )}
            {document && (
              <pre className="mono" aria-label={t("Version JSON")}>
                {document}
              </pre>
            )}
          </section>
        )}
        {restore && (
          <section className="history-restore">
            <h3>
              {t("Restore version")} {restore.versionKey}
            </h3>
            <p>
              {t(
                "This creates a new current version. The dashboard UID and current folder are kept.",
              )}
            </p>
            <label>
              {t("Restore note (optional)")}
              <input
                value={note}
                onChange={(event) => setNote(event.target.value)}
                maxLength={500}
                disabled={Boolean(busy)}
              />
            </label>
            <div className="form-actions">
              <button disabled={Boolean(busy)} onClick={() => setRestore(null)}>
                {t("Cancel")}
              </button>
              <button
                className="primary"
                disabled={Boolean(busy) || conflict}
                onClick={() =>
                  void run("restore", async (signal) => {
                    const restored = await api<Dashboard>(
                      `/dashboard-restore/${encodeURIComponent(dashboard.id)}`,
                      {
                        method: "POST",
                        body: jsonBody({
                          version: restore.versionKey,
                          revision: baseline.current,
                          ...(note.trim() ? { message: note } : {}),
                        }),
                        signal,
                      },
                    );
                    if (signal.aborted) return;
                    await reload();
                    window.location.assign(
                      `/d/${encodeURIComponent(dashboardUID(restored))}/${encodeURIComponent(restored.name)}`,
                    );
                  })
                }
              >
                {t(busy === "restore" ? "Restoring…" : "Confirm restore")}
              </button>
            </div>
          </section>
        )}
        {error && (
          <p className="form-error" role="alert">
            {t(error)}
          </p>
        )}
        {conflict && (
          <div className="history-conflict">
            <p>
              {t(
                "Dashboard changed. Reload the dashboard and reopen history before restoring.",
              )}
            </p>
            <button
              disabled={Boolean(busy)}
              onClick={() =>
                void run("reload", async () => {
                  await reload();
                  close();
                })
              }
            >
              {t("Reload dashboard")}
            </button>
          </div>
        )}
        <div className="form-actions">
          <button disabled={busy === "restore"} onClick={leave}>
            {t("Close")}
          </button>
        </div>
      </div>
    </Dialog>
  );
}
