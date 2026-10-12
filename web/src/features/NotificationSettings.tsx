import { useEffect, useRef, useState } from "react";
import {
  CircleCheck,
  CircleX,
  Clock,
  Mail,
  Pencil,
  Plus,
  RefreshCw,
  Send,
  Trash2,
  Webhook,
  X,
} from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";
import "./NotificationSettings.css";
import { SilencesPanel } from "./SilencesPanel";
import {
  NotificationTimingFields,
  NotificationRoutingEditor,
  type NotificationPolicy,
} from "./NotificationRoutingEditor";
import { MuteTimingsPanel } from "./MuteTimingsPanel";
import { NotificationGroupsPanel } from "./NotificationGroupsPanel";
import {
  NotificationsTemplatesPanel,
  NotificationTemplateFields,
} from "./NotificationsTemplatesPanel";

type ContactPoint = {
  uid: string;
  name: string;
  type: "webhook";
  url: string;
  disabled: boolean;
  disable_resolve_message: boolean;
  timeout_seconds: number;
  version: number;
  updated_at: number;
  secure_fields: Record<string, boolean>;
  title_template?: string;
  message_template?: string;
};
type Delivery = {
  id: number;
  event_id: number;
  contact_point_uid: string;
  rule_uid: string;
  instance_key: string;
  status: "firing" | "resolved" | "test";
  state: "pending" | "sending" | "sent" | "failed" | "canceled" | "suppressed";
  attempts: number;
  max_attempts: number;
  next_attempt_at: number;
  last_attempt_at: number;
  created_at: number;
  delivered_at: number;
  last_error: string;
  http_status: number;
};
const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
const date = (value: number) =>
  value
    ? new Date(value).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en")
    : "—";
const deliveryState = (value: Delivery["state"]) =>
  ({
    pending: text("Pending", "等待发送"),
    sending: text("Sending", "发送中"),
    sent: text("Sent", "已发送"),
    failed: text("Failed", "发送失败"),
    canceled: text("Canceled", "已取消"),
    suppressed: text("Silenced", "已静默"),
  })[value];
const deliveryStatus = (value: Delivery["status"]) =>
  ({
    firing: text("Firing", "触发"),
    resolved: text("Resolved", "恢复"),
    test: text("Test", "测试"),
  })[value];

export function NotificationSettings({
  tick,
  notify,
}: {
  tick: number;
  notify: (value: string, error?: boolean) => void;
}) {
  const [points, setPoints] = useState<ContactPoint[]>([]);
  const [deliveries, setDeliveries] = useState<Delivery[]>([]);
  const [policy, setPolicy] = useState<NotificationPolicy | null>(null);
  const [policyDirty, setPolicyDirty] = useState(false);
  const dirty = useRef(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [filter, setFilter] = useState("");
  const [editor, setEditor] = useState<ContactPoint | "new" | null>(null);
  const [removing, setRemoving] = useState<ContactPoint | null>(null);
  const [busy, setBusy] = useState("");
  const [policyError, setPolicyError] = useState("");
  const [muteNames, setMuteNames] = useState<string[]>([]);
  const reload = () => setRevision((value) => value + 1);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      api<ContactPoint[]>("/alerting/contact-points", {
        signal: controller.signal,
      }),
      api<NotificationPolicy>("/alerting/notification-policy", {
        signal: controller.signal,
      }),
      api<Delivery[]>(
        `/alerting/notifications?limit=100${filter ? `&contact_point_uid=${encodeURIComponent(filter)}` : ""}`,
        { signal: controller.signal },
      ),
    ])
      .then(([contactPoints, currentPolicy, history]) => {
        if (controller.signal.aborted) return;
        setPoints(contactPoints);
        setDeliveries(history);
        setPolicy((current) =>
          dirty.current || (current && current.version > currentPolicy.version)
            ? current
            : currentPolicy,
        );
        setLoaded(true);
        setError("");
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError(message(reason));
      });
    return () => controller.abort();
  }, [tick, revision, filter]);

  function changePolicy(value: NotificationPolicy) {
    setPolicy(value);
    dirty.current = true;
    setPolicyDirty(true);
    setPolicyError("");
  }
  const pointName = (uid: string) =>
    points.find((point) => point.uid === uid)?.name || uid;
  function routeReferences(
    routes: NotificationPolicy["routes"],
    uid: string,
  ): boolean {
    return routes.some(
      (route) =>
        route.receiver === uid || routeReferences(route.routes || [], uid),
    );
  }
  const policyReferences = (uid: string) =>
    policy?.receiver === uid ||
    (policy ? routeReferences(policy.routes, uid) : false);

  return (
    <div className="notification-settings">
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!loaded ? (
        error ? (
          <button onClick={reload}>
            <RefreshCw size={15} />
            {t("Retry")}
          </button>
        ) : (
          <p role="status">{text("Loading notifications…", "正在加载通知…")}</p>
        )
      ) : (
        <>
          <section
            className="list-panel"
            aria-label={text("Contact points", "联系点")}
          >
            <div className="section-heading">
              <div>
                <h2>
                  <Webhook size={18} />
                  {text("Contact points", "联系点")}
                  <span>{points.length}</span>
                </h2>
                <p>
                  {text(
                    "Send alert transitions to an HTTP webhook.",
                    "通过 HTTP Webhook 发送告警状态变化。",
                  )}
                </p>
              </div>
              <button className="primary" onClick={() => setEditor("new")}>
                <Plus size={16} />
                {text("Add contact point", "添加联系点")}
              </button>
            </div>
            {points.length ? (
              <div className="table-scroll notification-table">
                <table>
                  <thead>
                    <tr>
                      <th>{t("Name")}</th>
                      <th>{text("Endpoint", "地址")}</th>
                      <th>{t("Status")}</th>
                      <th>{text("Actions", "操作")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {points.map((point) => (
                      <tr key={point.uid}>
                        <td>
                          <strong>{point.name}</strong>
                          <div className="notification-secondary mono">
                            {point.uid}
                          </div>
                        </td>
                        <td>
                          <div className="mono notification-secondary">
                            {point.url}
                          </div>
                          <div className="notification-secondary">
                            Webhook · {point.timeout_seconds}
                            {text("s timeout", " 秒超时")}
                            {point.secure_fields &&
                            Object.values(point.secure_fields).some(Boolean)
                              ? text(" · Credentials stored", " · 已保存凭据")
                              : ""}
                          </div>
                        </td>
                        <td>
                          <span
                            className={`notification-status ${point.disabled ? "canceled" : "sent"}`}
                          >
                            {point.disabled
                              ? text("Disabled", "已停用")
                              : text("Enabled", "已启用")}
                          </span>
                          <div className="notification-secondary">
                            {point.disable_resolve_message
                              ? text("Firing only", "仅触发通知")
                              : text("Firing and resolved", "触发和恢复通知")}
                          </div>
                        </td>
                        <td>
                          <div className="row-actions">
                            <button
                              disabled={!!busy || point.disabled}
                              onClick={async () => {
                                setBusy(`test:${point.uid}`);
                                try {
                                  await api(
                                    `/alerting/contact-points/${encodeURIComponent(point.uid)}/test`,
                                    { method: "POST" },
                                  );
                                  notify(
                                    text(
                                      "Test notification queued. Check delivery history for the result.",
                                      "测试通知已入队，可在发送记录中查看结果。",
                                    ),
                                  );
                                  reload();
                                } catch (reason) {
                                  notify(message(reason), true);
                                } finally {
                                  setBusy("");
                                }
                              }}
                            >
                              <Send size={14} />
                              {text("Test", "测试")}
                            </button>
                            <button
                              disabled={!!busy}
                              onClick={() => setEditor(point)}
                            >
                              <Pencil size={14} />
                              {t("Edit")}
                            </button>
                            <button
                              disabled={!!busy}
                              className="danger-hover"
                              onClick={() => setRemoving(point)}
                            >
                              <Trash2 size={14} />
                              {t("Delete")}
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="notification-empty">
                {text(
                  "Add a webhook, then select it in the notification policy.",
                  "添加 Webhook 后，在通知策略中选择联系点。",
                )}
              </p>
            )}
          </section>

          {policy && (
            <section
              className="list-panel"
              aria-label={text("Notification policy", "通知策略")}
            >
              <div className="section-heading">
                <div>
                  <h2>
                    <Mail size={18} />
                    {text("Notification policy", "通知策略")}
                  </h2>
                  <p>
                    {text(
                      "Matching routes take priority. Unmatched alerts use the default contact point.",
                      "告警优先发送到匹配的路由；未匹配的告警使用默认联系点。",
                    )}
                  </p>
                </div>
              </div>
              <form
                className="notification-policy-form"
                onSubmit={async (event) => {
                  event.preventDefault();
                  setBusy("policy");
                  setPolicyError("");
                  try {
                    const saved = await api<NotificationPolicy>(
                      "/alerting/notification-policy",
                      { method: "PUT", body: jsonBody(policy) },
                    );
                    setPolicy(saved);
                    dirty.current = false;
                    setPolicyDirty(false);
                    notify(text("Notification policy saved", "通知策略已保存"));
                    reload();
                  } catch (reason) {
                    setPolicyError(message(reason));
                  } finally {
                    setBusy("");
                  }
                }}
              >
                <label>
                  {text("Default contact point", "默认联系点")}
                  <select
                    aria-label={text("Default contact point", "默认联系点")}
                    value={policy.receiver}
                    disabled={!!busy}
                    onChange={(event) =>
                      changePolicy({ ...policy, receiver: event.target.value })
                    }
                  >
                    <option value="">
                      {text("No default delivery", "不发送默认通知")}
                    </option>
                    {points.map((point) => (
                      <option key={point.uid} value={point.uid}>
                        {point.name}
                        {point.disabled
                          ? text(" (disabled)", "（已停用）")
                          : ""}
                      </option>
                    ))}
                  </select>
                </label>
                <NotificationTimingFields
                  root
                  value={policy}
                  busy={!!busy}
                  onChange={(v) =>
                    changePolicy({
                      ...policy,
                      group_by: v.group_by,
                      group_wait: v.group_wait,
                      group_interval: v.group_interval,
                      repeat_interval: v.repeat_interval,
                    })
                  }
                />
                <NotificationRoutingEditor
                  routes={policy.routes}
                  points={points}
                  timings={muteNames}
                  inherited={policy}
                  busy={!!busy}
                  onChange={(routes) => changePolicy({ ...policy, routes })}
                />
                {policyError && (
                  <p role="alert" className="form-error">
                    {policyError}
                  </p>
                )}
                <div className="form-actions">
                  <button
                    type="button"
                    disabled={!!busy || !policyDirty}
                    onClick={() => {
                      dirty.current = false;
                      setPolicyDirty(false);
                      setPolicyError("");
                      reload();
                    }}
                  >
                    {text("Discard changes", "放弃修改")}
                  </button>
                  <button className="primary" disabled={!!busy || !policyDirty}>
                    {text(
                      busy === "policy" ? "Saving…" : "Save policy",
                      busy === "policy" ? "正在保存…" : "保存策略",
                    )}
                  </button>
                </div>
              </form>
            </section>
          )}

          <SilencesPanel tick={tick} notify={notify} />
          <MuteTimingsPanel
            tick={tick}
            notify={notify}
            onNames={setMuteNames}
          />
          <NotificationGroupsPanel tick={tick} points={points} />
          <NotificationsTemplatesPanel tick={tick} notify={notify} />

          <section
            className="list-panel"
            aria-label={text("Delivery history", "发送记录")}
          >
            <div className="section-heading">
              <div>
                <h2>
                  <Clock size={18} />
                  {text("Delivery history", "发送记录")}
                </h2>
                <p>
                  {text(
                    "Each delivery retains its attempts, HTTP result and next retry time.",
                    "每次发送保留尝试次数、HTTP 结果和下次重试时间。",
                  )}
                </p>
              </div>
              <div className="row-actions">
                <select
                  aria-label={text(
                    "Filter delivery contact point",
                    "按联系点筛选记录",
                  )}
                  value={filter}
                  onChange={(event) => setFilter(event.target.value)}
                >
                  <option value="">
                    {text("All contact points", "所有联系点")}
                  </option>
                  {points.map((point) => (
                    <option key={point.uid} value={point.uid}>
                      {point.name}
                    </option>
                  ))}
                </select>
                <button onClick={reload}>
                  <RefreshCw size={15} />
                  {t("Refresh")}
                </button>
              </div>
            </div>
            {deliveries.length ? (
              <div className="table-scroll notification-table">
                <table>
                  <thead>
                    <tr>
                      <th>{text("Contact point / Rule", "联系点 / 规则")}</th>
                      <th>{text("Notification", "通知")}</th>
                      <th>{t("Status")}</th>
                      <th>{text("Attempts", "尝试次数")}</th>
                      <th>{text("Time", "时间")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {deliveries.map((delivery) => (
                      <tr key={delivery.id}>
                        <td>
                          <strong>
                            {pointName(delivery.contact_point_uid)}
                          </strong>
                          <div className="notification-secondary mono">
                            {delivery.rule_uid ||
                              text("Test notification", "测试通知")}
                          </div>
                        </td>
                        <td>
                          {deliveryStatus(delivery.status)}
                          <div className="notification-secondary mono">
                            #{delivery.id}
                          </div>
                        </td>
                        <td>
                          <span
                            className={`notification-status ${delivery.state}`}
                          >
                            {delivery.state === "sent" ? (
                              <CircleCheck size={14} />
                            ) : delivery.state === "failed" ? (
                              <CircleX size={14} />
                            ) : (
                              <Clock size={14} />
                            )}
                            {deliveryState(delivery.state)}
                          </span>
                          {delivery.http_status > 0 && (
                            <div className="notification-secondary">
                              HTTP {delivery.http_status}
                            </div>
                          )}
                          {delivery.last_error && (
                            <div className="notification-secondary">
                              {delivery.last_error}
                            </div>
                          )}
                          {delivery.state === "failed" && (
                            <button
                              disabled={!!busy}
                              onClick={async () => {
                                setBusy(`retry:${delivery.id}`);
                                try {
                                  await api(
                                    `/alerting/notifications/${delivery.id}/retry`,
                                    { method: "POST" },
                                  );
                                  notify(
                                    text(
                                      "Notification queued for retry",
                                      "通知已重新入队",
                                    ),
                                  );
                                  reload();
                                } catch (reason) {
                                  notify(message(reason), true);
                                } finally {
                                  setBusy("");
                                }
                              }}
                            >
                              <RefreshCw size={14} />
                              {text("Retry delivery", "重试发送")}
                            </button>
                          )}
                        </td>
                        <td>
                          {delivery.attempts} / {delivery.max_attempts}
                          {delivery.state === "pending" &&
                            delivery.next_attempt_at > 0 && (
                              <div className="notification-secondary">
                                {text("Next retry", "下次重试")}
                                <br />
                                {date(delivery.next_attempt_at)}
                              </div>
                            )}
                        </td>
                        <td>
                          {date(delivery.created_at)}
                          {delivery.delivered_at > 0 && (
                            <div className="notification-secondary">
                              {text("Delivered", "已送达")}
                              <br />
                              {date(delivery.delivered_at)}
                            </div>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="notification-empty">
                {text(
                  "No deliveries yet. Test a contact point or wait for an alert transition.",
                  "暂无发送记录。可测试联系点，或等待告警状态变化。",
                )}
              </p>
            )}
          </section>
        </>
      )}
      {editor && (
        <ContactPointEditor
          point={editor}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            reload();
            notify(text("Contact point saved", "联系点已保存"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={text("Delete contact point", "删除联系点")}
          onClose={() => {
            if (!busy) setRemoving(null);
          }}
        >
          <p>
            {text(
              "Delete this contact point? Existing delivery history is retained.",
              "删除此联系点？已有发送记录会保留。",
            )}
          </p>
          <strong>{removing.name}</strong>
          {policyReferences(removing.uid) && (
            <p className="notification-delete-warning">
              {text(
                "This contact point is referenced by the notification policy. Remove those references and save the policy before deleting it.",
                "通知策略正在使用此联系点。请先删除策略中的引用并保存，再删除联系点。",
              )}
            </p>
          )}
          <div className="form-actions">
            <button disabled={!!busy} onClick={() => setRemoving(null)}>
              {t("Cancel")}
            </button>
            <button
              className="danger"
              disabled={!!busy || !!policyReferences(removing.uid)}
              onClick={async () => {
                setBusy(`delete:${removing.uid}`);
                try {
                  await api(
                    `/alerting/contact-points/${encodeURIComponent(removing.uid)}`,
                    { method: "DELETE" },
                  );
                  if (filter === removing.uid) setFilter("");
                  setRemoving(null);
                  reload();
                  notify(text("Contact point deleted", "联系点已删除"));
                } catch (reason) {
                  notify(message(reason), true);
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
    </div>
  );
}

type HeaderDraft = {
  key: string;
  value: string;
  stored: boolean;
  removed?: boolean;
};
function ContactPointEditor({
  point,
  onClose,
  onSaved,
}: {
  point: ContactPoint | "new";
  onClose: () => void;
  onSaved: () => void;
}) {
  const original = point === "new" ? null : point;
  const [name, setName] = useState(original?.name || "");
  const [url, setURL] = useState(original?.url || "");
  const [titleTemplate, setTitleTemplate] = useState(
    original?.title_template || "",
  );
  const [messageTemplate, setMessageTemplate] = useState(
    original?.message_template || "",
  );
  const [timeout, setTimeout] = useState(original?.timeout_seconds || 10);
  const [disabled, setDisabled] = useState(original?.disabled || false);
  const [disableResolve, setDisableResolve] = useState(
    original?.disable_resolve_message || false,
  );
  const [token, setToken] = useState("");
  const [clearToken, setClearToken] = useState(false);
  const [headers, setHeaders] = useState<HeaderDraft[]>(() =>
    Object.entries(original?.secure_fields || {})
      .filter(([key, exists]) => exists && key.startsWith("header:"))
      .map(([key]) => ({ key: key.slice(7), value: "", stored: true })),
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog
      title={text(
        original ? "Edit contact point" : "Add contact point",
        original ? "编辑联系点" : "添加联系点",
      )}
      style={{ width: 660 }}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="notification-contact-form"
        onSubmit={async (event) => {
          event.preventDefault();
          setError("");
          let endpoint: URL;
          try {
            endpoint = new URL(url);
          } catch {
            setError(
              text(
                "Enter a valid HTTP or HTTPS webhook URL",
                "请输入有效的 HTTP 或 HTTPS Webhook 地址",
              ),
            );
            return;
          }
          if (!["http:", "https:"].includes(endpoint.protocol)) {
            setError(
              text(
                "Webhook URL must use HTTP or HTTPS",
                "Webhook 地址必须使用 HTTP 或 HTTPS",
              ),
            );
            return;
          }
          const activeHeaders = headers.filter((header) => !header.removed);
          const headerNames = activeHeaders.map((header) =>
            header.key.trim().toLowerCase(),
          );
          if (new Set(headerNames).size !== headerNames.length) {
            setError(text("Header names must be unique", "请求头名称不能重复"));
            return;
          }
          const clearSecrets = headers
            .filter((header) => header.stored && header.removed)
            .map((header) => `header:${header.key}`);
          if (clearToken) clearSecrets.push("bearer_token");
          const changedHeaders = Object.fromEntries(
            activeHeaders
              .filter((header) => !header.stored || header.value !== "")
              .map((header) => [header.key.trim(), header.value]),
          );
          setBusy(true);
          try {
            await api(
              original
                ? `/alerting/contact-points/${encodeURIComponent(original.uid)}`
                : "/alerting/contact-points",
              {
                method: original ? "PUT" : "POST",
                body: jsonBody({
                  ...(original ? { uid: original.uid } : {}),
                  name: name.trim(),
                  type: "webhook",
                  url,
                  timeout_seconds: timeout,
                  disabled,
                  disable_resolve_message: disableResolve,
                  title_template: titleTemplate,
                  message_template: messageTemplate,
                  version: original?.version || 0,
                  ...(token && !clearToken ? { bearer_token: token } : {}),
                  ...(Object.keys(changedHeaders).length
                    ? { headers: changedHeaders }
                    : {}),
                  ...(clearSecrets.length
                    ? { clear_secrets: clearSecrets }
                    : {}),
                }),
              },
            );
            onSaved();
          } catch (reason) {
            setError(message(reason));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {text("Contact point name", "联系点名称")}
          <input
            autoFocus
            required
            maxLength={256}
            value={name}
            disabled={busy}
            onChange={(event) => setName(event.target.value)}
            placeholder={text("Operations webhook", "运维 Webhook")}
          />
        </label>
        <label>
          {text("Webhook URL", "Webhook 地址")}
          <input
            required
            type="url"
            maxLength={4096}
            value={url}
            disabled={busy}
            onChange={(event) => setURL(event.target.value)}
            placeholder="https://example.com/alerts"
          />
        </label>
        <div className="form-row">
          <label>
            {text("Timeout (seconds)", "超时（秒）")}
            <input
              type="number"
              min={1}
              max={30}
              required
              value={timeout}
              disabled={busy}
              onChange={(event) => setTimeout(Number(event.target.value))}
            />
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={disabled}
              disabled={busy}
              onChange={(event) => setDisabled(event.target.checked)}
            />
            {text("Disable contact point", "停用联系点")}
          </label>
        </div>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={disableResolve}
            disabled={busy}
            onChange={(event) => setDisableResolve(event.target.checked)}
          />
          {text("Do not send resolved notifications", "不发送恢复通知")}
        </label>
        <NotificationTemplateFields
          title={titleTemplate}
          message={messageTemplate}
          disabled={busy}
          onTitle={setTitleTemplate}
          onMessage={setMessageTemplate}
        />
        <fieldset>
          <legend>{text("Authentication", "身份验证")}</legend>
          <label>
            {text("Bearer token", "Bearer 令牌")}
            <input
              aria-label={text("Bearer token", "Bearer 令牌")}
              type="password"
              autoComplete="new-password"
              maxLength={16384}
              value={token}
              disabled={busy || clearToken}
              onChange={(event) => setToken(event.target.value)}
              placeholder={
                original?.secure_fields?.bearer_token
                  ? text("Stored · leave blank to keep", "已保存，留空保留")
                  : text("Optional", "可选")
              }
            />
            <span className="notification-help">
              {text(
                "Credentials are encrypted on the server and are never returned to the browser.",
                "凭据在服务端加密保存，不会返回到浏览器。",
              )}
            </span>
          </label>
          {original?.secure_fields?.bearer_token && (
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={clearToken}
                disabled={busy}
                onChange={(event) => setClearToken(event.target.checked)}
              />
              {text("Remove stored bearer token", "删除已保存的 Bearer 令牌")}
            </label>
          )}
        </fieldset>
        <fieldset>
          <legend>{text("Encrypted HTTP headers", "加密 HTTP 请求头")}</legend>
          {headers.map((header, index) =>
            header.removed ? null : (
              <div className="form-row" key={index}>
                <label>
                  {text("Header name", "请求头名称")}
                  <input
                    required
                    maxLength={128}
                    value={header.key}
                    disabled={busy || header.stored}
                    onChange={(event) =>
                      setHeaders((current) =>
                        current.map((value, position) =>
                          position === index
                            ? { ...value, key: event.target.value }
                            : value,
                        ),
                      )
                    }
                    placeholder="X-Webhook-Token"
                  />
                </label>
                <label>
                  {text("Header value", "请求头值")}
                  <input
                    type="password"
                    autoComplete="new-password"
                    required={!header.stored}
                    maxLength={16384}
                    value={header.value}
                    disabled={busy}
                    onChange={(event) =>
                      setHeaders((current) =>
                        current.map((value, position) =>
                          position === index
                            ? { ...value, value: event.target.value }
                            : value,
                        ),
                      )
                    }
                    placeholder={
                      header.stored
                        ? text(
                            "Stored · leave blank to keep",
                            "已保存，留空保留",
                          )
                        : text("Value", "请求头值")
                    }
                  />
                </label>
                <button
                  type="button"
                  disabled={busy}
                  className="icon-button"
                  aria-label={text("Remove header", "删除请求头")}
                  onClick={() =>
                    setHeaders((current) =>
                      current.map((value, position) =>
                        position === index
                          ? { ...value, removed: true }
                          : value,
                      ),
                    )
                  }
                >
                  <X size={16} />
                </button>
              </div>
            ),
          )}
          <button
            type="button"
            disabled={
              busy || headers.filter((header) => !header.removed).length >= 32
            }
            onClick={() =>
              setHeaders((current) => [
                ...current,
                { key: "", value: "", stored: false },
              ])
            }
          >
            <Plus size={14} />
            {text("Add header", "添加请求头")}
          </button>
        </fieldset>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={busy} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy}>
            {text(
              busy ? "Saving…" : "Save contact point",
              busy ? "正在保存…" : "保存联系点",
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
