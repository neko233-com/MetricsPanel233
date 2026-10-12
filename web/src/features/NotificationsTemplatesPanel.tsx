import { useEffect, useState } from "react";
import { Code2, Pencil, Play, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";
import "./NotificationsTemplatesPanel.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
type NotificationTemplate = {
  name: string;
  template: string;
  version: number;
  updated_at: number;
  definitions: string[];
};
type Preview = {
  results: { name: string; text: string }[];
  errors: { name: string; error: string }[];
};
const base = "/alerting/notification-templates";
const sampleData = JSON.stringify(
  {
    receiver: "Operations",
    status: "firing",
    groupLabels: { alertname: "Service unavailable" },
    commonLabels: { alertname: "Service unavailable", instance: "server-01" },
    commonAnnotations: { summary: "Health check failed" },
    alerts: [
      {
        status: "firing",
        labels: { alertname: "Service unavailable", instance: "server-01" },
        annotations: { summary: "Health check failed" },
        ruleUID: "service-health",
        startsAt: "2026-01-01T12:00:00Z",
        values: { A: 1 },
      },
    ],
  },
  null,
  2,
);

function PreviewOutput({ value }: { value: Preview | null }) {
  if (!value) return null;
  return (
    <div className="notification-template-results" aria-live="polite">
      {value.results.map((result) => (
        <div key={result.name}>
          <h4>{result.name}</h4>
          <pre>{result.text || text("(empty)", "（空内容）")}</pre>
        </div>
      ))}
      {value.errors.map((error) => (
        <div className="form-error" role="alert" key={error.name}>
          <strong>{error.name}</strong>
          <pre>{error.error}</pre>
        </div>
      ))}
    </div>
  );
}

export function NotificationsTemplatesPanel({
  tick,
  notify,
}: {
  tick: number;
  notify: (value: string, error?: boolean) => void;
}) {
  const [templates, setTemplates] = useState<NotificationTemplate[]>([]);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<NotificationTemplate | "new" | null>(
    null,
  );
  const [removing, setRemoving] = useState<NotificationTemplate | null>(null);
  const [busy, setBusy] = useState(false);
  const reload = () => setRevision((value) => value + 1);
  useEffect(() => {
    const controller = new AbortController();
    void api<NotificationTemplate[]>(base, { signal: controller.signal })
      .then((values) => {
        if (controller.signal.aborted) return;
        setTemplates(values);
        setError("");
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError(message(reason));
      });
    return () => controller.abort();
  }, [tick, revision]);
  return (
    <section
      className="list-panel notification-templates"
      aria-label={text("Notification templates", "通知模板")}
    >
      <div className="section-heading">
        <div>
          <h2>
            <Code2 size={18} />
            {text("Notification templates", "通知模板")}
          </h2>
          <p>
            {text(
              "Reusable Go templates for webhook titles and messages.",
              "为 Webhook 标题和正文编写可复用的 Go 模板。",
            )}
          </p>
        </div>
        <div className="row-actions">
          <button
            onClick={reload}
            aria-label={text("Refresh templates", "刷新模板")}
          >
            <RefreshCw size={15} />
          </button>
          <button className="primary" onClick={() => setEditor("new")}>
            <Plus size={15} />
            {text("Add template", "添加模板")}
          </button>
        </div>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!templates.length ? (
        <div className="notification-empty">
          {text(
            "No saved templates. Contact points can also use inline expressions.",
            "暂无已保存的模板。联系点也可以直接使用内联表达式。",
          )}
        </div>
      ) : (
        <table className="notification-table">
          <thead>
            <tr>
              <th>{t("Name")}</th>
              <th>{text("Definitions", "定义")}</th>
              <th>{text("Version", "版本")}</th>
              <th>{text("Updated", "更新时间")}</th>
              <th aria-label={text("Actions", "操作")} />
            </tr>
          </thead>
          <tbody>
            {templates.map((value) => (
              <tr key={value.name}>
                <td>
                  <strong>{value.name}</strong>
                </td>
                <td>{value.definitions?.join(", ") || value.name}</td>
                <td>{value.version}</td>
                <td>
                  {new Date(value.updated_at).toLocaleString(
                    getLocale() === "zh" ? "zh-CN" : "en",
                  )}
                </td>
                <td>
                  <div className="row-actions">
                    <button onClick={() => setEditor(value)}>
                      <Pencil size={14} />
                      {t("Edit")}
                    </button>
                    <button onClick={() => setRemoving(value)}>
                      <Trash2 size={14} />
                      {t("Delete")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {editor && (
        <TemplateEditor
          value={editor}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            reload();
            notify(text("Template saved", "模板已保存"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={text("Delete notification template", "删除通知模板")}
          onClose={() => {
            if (!busy) setRemoving(null);
          }}
        >
          <p>
            {text(
              "Delete this template group? Contact points and other templates must no longer reference its definitions. Queued deliveries retain their saved version.",
              "删除此模板组？请先移除联系点或其他模板对它的引用。待发送记录会保留创建时的版本。",
            )}
          </p>
          <strong>{removing.name}</strong>
          <div className="form-actions">
            <button disabled={busy} onClick={() => setRemoving(null)}>
              {t("Cancel")}
            </button>
            <button
              className="danger"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await api(
                    `${base}/${encodeURIComponent(removing.name)}?version=${removing.version}`,
                    { method: "DELETE" },
                  );
                  setRemoving(null);
                  reload();
                  notify(text("Template deleted", "模板已删除"));
                } catch (reason) {
                  notify(message(reason), true);
                } finally {
                  setBusy(false);
                }
              }}
            >
              {t("Delete")}
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}

function TemplateEditor({
  value,
  onClose,
  onSaved,
}: {
  value: NotificationTemplate | "new";
  onClose: () => void;
  onSaved: () => void;
}) {
  const original = value === "new" ? null : value;
  const [name, setName] = useState(original?.name || "");
  const [source, setSource] = useState(
    original?.template || "{{ .Receiver }}: {{ .Status | toUpper }}",
  );
  const [data, setData] = useState(sampleData);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  return (
    <Dialog
      title={text(
        original ? "Edit notification template" : "Add notification template",
        original ? "编辑通知模板" : "添加通知模板",
      )}
      style={{ width: 1040 }}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="notification-template-editor"
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy("save");
          setError("");
          try {
            await api(
              original ? `${base}/${encodeURIComponent(original.name)}` : base,
              {
                method: original ? "PUT" : "POST",
                body: jsonBody({
                  name: name.trim(),
                  template: source,
                  version: original?.version || 0,
                }),
              },
            );
            onSaved();
          } catch (reason) {
            setError(message(reason));
          } finally {
            setBusy("");
          }
        }}
      >
        <label>
          {text("Template group name", "模板组名称")}
          <input
            autoFocus
            required
            maxLength={128}
            disabled={!!original || !!busy}
            value={name}
            onChange={(event) => {
              setName(event.target.value);
              setPreview(null);
            }}
            placeholder="operations"
          />
        </label>
        <div className="notification-template-columns">
          <div>
            <label>
              {text("Template source", "模板源码")}
              <textarea
                aria-label={text("Template source", "模板源码")}
                className="notification-template-code"
                required
                maxLength={65536}
                rows={19}
                spellCheck={false}
                disabled={!!busy}
                value={source}
                onChange={(event) => {
                  setSource(event.target.value);
                  setPreview(null);
                }}
              />
            </label>
            <p className="notification-help">
              {text(
                'Plain source is named after this group. Use {{ define "name" }}…{{ end }} for multiple definitions. Names must be unique across groups.',
                '普通源码以模板组名称作为定义名。用 {{ define "name" }}…{{ end }} 创建多个定义，各组中的定义名必须唯一。',
              )}
            </p>
          </div>
          <div>
            <label>
              {text("Preview data (JSON)", "预览数据（JSON）")}
              <textarea
                aria-label={text("Preview data (JSON)", "预览数据（JSON）")}
                className="notification-template-code"
                maxLength={1048576}
                rows={19}
                spellCheck={false}
                disabled={!!busy}
                value={data}
                onChange={(event) => {
                  setData(event.target.value);
                  setPreview(null);
                }}
              />
            </label>
            <p className="notification-help">
              {text(
                "Preview renders the current draft without saving or sending a notification. It contains no contact point credentials.",
                "预览只渲染当前草稿，不会保存或发送通知，数据中不包含联系点凭据。",
              )}
            </p>
            <button
              type="button"
              disabled={!!busy || !name.trim() || !source.trim()}
              onClick={async () => {
                setBusy("preview");
                setError("");
                setPreview(null);
                try {
                  const sample: unknown = JSON.parse(data);
                  setPreview(
                    await api<Preview>(`${base}/preview`, {
                      method: "POST",
                      body: jsonBody({
                        name: name.trim(),
                        template: source,
                        data: sample,
                      }),
                    }),
                  );
                } catch (reason) {
                  setError(message(reason));
                } finally {
                  setBusy("");
                }
              }}
            >
              <Play size={14} />
              {text(
                busy === "preview" ? "Rendering…" : "Preview draft",
                busy === "preview" ? "正在渲染…" : "预览草稿",
              )}
            </button>
          </div>
        </div>
        <PreviewOutput value={preview} />
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={!!busy} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={!!busy}>
            {text(
              busy === "save" ? "Saving…" : "Save template",
              busy === "save" ? "正在保存…" : "保存模板",
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

export function NotificationTemplateFields({
  title,
  message: body,
  disabled,
  onTitle,
  onMessage,
}: {
  title: string;
  message: string;
  disabled: boolean;
  onTitle: (value: string) => void;
  onMessage: (value: string) => void;
}) {
  const [templates, setTemplates] = useState<NotificationTemplate[]>([]);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void api<NotificationTemplate[]>(base, { signal: controller.signal })
      .then((values) => {
        if (!controller.signal.aborted) setTemplates(values);
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError(message(reason));
      });
    return () => controller.abort();
  }, []);
  const names = templates.flatMap((value) => value.definitions || [value.name]);
  return (
    <fieldset className="notification-contact-templates">
      <legend>{text("Notification templates", "通知模板")}</legend>
      <p className="notification-help">
        {text(
          'Leave both fields empty to keep the default webhook payload. Each expression can call a saved definition with {{ template "name" . }}.',
          '两项留空时使用默认 Webhook 载荷。表达式可以用 {{ template "name" . }} 调用已保存的定义。',
        )}
      </p>
      {(["title", "message"] as const).map((field) => (
        <label key={field}>
          {text(
            field === "title" ? "Title template" : "Message template",
            field === "title" ? "标题模板" : "正文模板",
          )}
          <textarea
            aria-label={text(
              field === "title" ? "Title template" : "Message template",
              field === "title" ? "标题模板" : "正文模板",
            )}
            className="notification-template-code"
            maxLength={65536}
            rows={field === "title" ? 2 : 4}
            disabled={disabled || busy}
            value={field === "title" ? title : body}
            onChange={(event) => {
              (field === "title" ? onTitle : onMessage)(event.target.value);
              setPreview(null);
            }}
            placeholder={
              field === "title"
                ? "{{ .Receiver }}: {{ .Status | toUpper }}"
                : "{{ range .Alerts }}{{ .Annotations.summary }}{{ end }}"
            }
          />
          {!!names.length && (
            <select
              aria-label={text(
                field === "title"
                  ? "Use saved title definition"
                  : "Use saved message definition",
                field === "title"
                  ? "使用已保存的标题定义"
                  : "使用已保存的正文定义",
              )}
              value=""
              disabled={disabled || busy}
              onChange={(event) => {
                (field === "title" ? onTitle : onMessage)(
                  `{{ template ${JSON.stringify(event.target.value)} . }}`,
                );
                setPreview(null);
              }}
            >
              <option value="">
                {text("Use saved definition…", "使用已保存的定义…")}
              </option>
              {names.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          )}
        </label>
      ))}
      <button
        type="button"
        disabled={disabled || busy || (!title && !body)}
        onClick={async () => {
          setBusy(true);
          setError("");
          setPreview(null);
          try {
            setPreview(
              await api<Preview>(`${base}/preview`, {
                method: "POST",
                body: jsonBody({ title, message: body }),
              }),
            );
          } catch (reason) {
            setError(message(reason));
          } finally {
            setBusy(false);
          }
        }}
      >
        <Play size={14} />
        {text(
          busy ? "Rendering…" : "Preview with sample data",
          busy ? "正在渲染…" : "使用示例数据预览",
        )}
      </button>
      <PreviewOutput value={preview} />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
    </fieldset>
  );
}
