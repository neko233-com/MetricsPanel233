import { useEffect, useRef, useState } from "react";
import { Camera } from "lucide-react";
import { api, jsonBody, message, type Dashboard } from "../api";
import { t } from "../i18n";
import { Dialog } from "./Dialog";
import type { TimeSelection } from "../grafana/time-range";
import type { SnapshotPanel } from "../grafana/dashboard-snapshot";

export function DashboardSnapshotButton({
  dashboard,
  panels,
  range,
  reload,
}: {
  dashboard: Dashboard;
  panels?: SnapshotPanel[];
  range: TimeSelection;
  reload: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [name, setName] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [saved, setSaved] = useState<{ uid: string; title: string }>();
  const controller = useRef<AbortController | null>(null);
  const close = () => {
    controller.current?.abort();
    setOpen(false);
  };
  useEffect(() => () => controller.current?.abort(), []);
  return (
    <>
      <button
        onClick={() => {
          const title = [...dashboard.name].slice(0, 55);
          while (new TextEncoder().encode(title.join("")).length > 80)
            title.pop();
          setName(`${title.join("")} ${t("Snapshot")}`);
          setError("");
          setSaved(undefined);
          setOpen(true);
        }}
        disabled={!dashboard.panels.length}
      >
        <Camera size={17} />
        {t("Create snapshot")}
      </button>
      {open ? (
        <Dialog title={t("Create snapshot")} onClose={close}>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              setBusy(true);
              setError("");
              const abort = new AbortController();
              controller.current = abort;
              try {
                const { captureDashboardSnapshot } = await import(
                  "../grafana/dashboard-snapshot"
                );
                const entries =
                  panels ||
                  dashboard.panels.map((panel, index) => ({
                    panel,
                    values: {},
                    x: (index % 2) * 12,
                    y: Math.floor(index / 2) * 10,
                    w: 12,
                    h: 10,
                  }));
                const snapshot = await captureDashboardSnapshot(
                  dashboard,
                  entries,
                  name,
                  range,
                  abort.signal,
                );
                if (abort.signal.aborted) return;
                await api("/import/grafana", {
                  method: "POST",
                  body: jsonBody(snapshot),
                  signal: abort.signal,
                });
                setSaved({ uid: snapshot.uid, title: snapshot.title });
                await reload();
              } catch (error) {
                if (!abort.signal.aborted) setError(t(message(error)));
              } finally {
                abort.abort();
                setBusy(false);
              }
            }}
          >
            <p className="subtle">
              {t(
                "Save the selected window and all panel data. The saved dashboard does not query its original data sources.",
              )}
            </p>
            <label>
              {t("Snapshot name")}
              <input
                aria-label={t("Snapshot name")}
                required
                maxLength={100}
                value={name}
                disabled={busy || Boolean(saved)}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            {error ? (
              <p role="alert" className="form-error">
                {error}
              </p>
            ) : null}
            {saved ? (
              <p role="status">
                <a href={`/d/${saved.uid}/${encodeURIComponent(saved.title)}`}>
                  {t("Open snapshot")}
                </a>
              </p>
            ) : null}
            <div className="form-actions">
              <button type="button" onClick={close}>
                {t(busy ? "Cancel capture" : "Close")}
              </button>
              {!saved ? (
                <button className="primary" disabled={busy}>
                  {t(busy ? "Capturing…" : "Save snapshot")}
                </button>
              ) : null}
            </div>
          </form>
        </Dialog>
      ) : null}
    </>
  );
}
