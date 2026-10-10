import { useState, useRef, useEffect, useId } from "react";
import { MessageSquarePlus, Pencil, Trash2, X } from "lucide-react";
import { api, dashboardUID, jsonBody, message, type Dashboard } from "../api";
import {
  annotationsChanged,
  annotationStateLabel,
  type Annotation,
} from "../grafana/annotations";
import { t } from "../i18n";

export function AnnotationEditor({
  dashboard,
  panelId = 0,
  time,
  events,
}: {
  dashboard: Dashboard;
  panelId?: number;
  time: number;
  events: Annotation[];
}) {
  const [editing, setEditing] = useState<Annotation | "new" | null>(null),
    [text, setText] = useState(""),
    [from, setFrom] = useState(""),
    [to, setTo] = useState(""),
    [tags, setTags] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [key, setKey] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const textID = useId();
  useEffect(() => {
    if (editing && !dialog.current?.open) dialog.current?.showModal();
  }, [editing]);
  const open = (event: Annotation | "new") => {
    setEditing(event);
    setText(event === "new" ? "" : event.text);
    setFrom(String(event === "new" ? time : event.time));
    setTo(String(event === "new" ? time : event.timeEnd));
    setTags(event === "new" ? "" : event.tags.join(", "));
    setError("");
    setKey(crypto.randomUUID());
  };
  return (
    <>
      <button
        className="icon-button"
        title={t("Annotations")}
        aria-label={t("Annotations")}
        onClick={() => open("new")}
      >
        <MessageSquarePlus size={16} />
      </button>
      {editing && (
        <dialog
          ref={dialog}
          aria-label={t("Annotations")}
          className="annotation-dialog"
          onCancel={() => setEditing(null)}
        >
          <div className="dialog-heading">
            <h2>{t("Annotations")}</h2>
            <button
              className="icon-button"
              aria-label={t("Close")}
              onClick={() => setEditing(null)}
            >
              <X size={18} />
            </button>
          </div>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              setBusy(true);
              setError("");
              try {
                const times = {
                  time: Number(from),
                  timeEnd: Number(to || from),
                };
                if (
                  !Number.isSafeInteger(times.time) ||
                  !Number.isSafeInteger(times.timeEnd)
                )
                  throw new Error("Use Unix milliseconds");
                await api(
                  editing === "new"
                    ? "/api/annotations"
                    : "/api/annotations/" + editing.id,
                  {
                    method: editing === "new" ? "POST" : "PUT",
                    body: jsonBody({
                      ...times,
                      text,
                      tags: tags
                        .split(",")
                        .map((tag) => tag.trim())
                        .filter(Boolean),
                      ...(editing === "new"
                        ? {
                            dashboardUID: dashboardUID(dashboard),
                            panelId,
                            idempotencyKey: key,
                          }
                        : {}),
                    }),
                  },
                );
                annotationsChanged();
                setEditing(null);
              } catch (e) {
                setError(message(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <div className="annotation-field">
              <label htmlFor={textID}>{t("Annotation text")}</label>
              <textarea
                id={textID}
                value={text}
                onChange={(e) => setText(e.target.value)}
                required
                maxLength={8192}
              />
            </div>
            <div className="form-grid">
              <label>
                {t("Start (milliseconds)")}
                <input
                  value={from}
                  onChange={(e) => setFrom(e.target.value)}
                  required
                  inputMode="numeric"
                />
              </label>
              <label>
                {t("End (milliseconds)")}
                <input
                  value={to}
                  onChange={(e) => setTo(e.target.value)}
                  inputMode="numeric"
                />
              </label>
            </div>
            <label>
              {t("Tags (comma separated)")}
              <input value={tags} onChange={(e) => setTags(e.target.value)} />
            </label>
            {error && (
              <p role="alert" className="form-error">
                {t(error)}
              </p>
            )}
            <button className="primary" disabled={busy} type="submit">
              {t("Save annotation")}
            </button>
          </form>
          <div className="annotation-list">
            {events.map((event) => (
              <div key={event.id}>
                <span>
                  {event.text}
                  {event.newState && (
                    <small className="annotation-alert-state">
                      {t("Alert state")}:{" "}
                      {annotationStateLabel(event.prevState || "Normal", t)} →{" "}
                      {annotationStateLabel(event.newState, t)}
                    </small>
                  )}
                </span>
                {!event.alertId && (
                  <>
                    <button
                      className="icon-button"
                      aria-label={t("Edit annotation")}
                      onClick={() => open(event)}
                    >
                      <Pencil size={15} />
                    </button>
                    <button
                      className="icon-button"
                      aria-label={t("Delete annotation")}
                      disabled={busy}
                      onClick={async () => {
                        setBusy(true);
                        try {
                          await api("/api/annotations/" + event.id, {
                            method: "DELETE",
                          });
                          annotationsChanged();
                        } catch (e) {
                          setError(message(e));
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      <Trash2 size={15} />
                    </button>
                  </>
                )}
              </div>
            ))}
          </div>
        </dialog>
      )}
    </>
  );
}
