import { useEffect, useRef, useState } from "react";
import { Ban, Pencil, Play, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { ApiError, api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";
import type { NotificationMatcher } from "./NotificationRoutingEditor";
import "./InhibitionRulesPanel.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
type InhibitionRule = {
  uid: string;
  name: string;
  disabled: boolean;
  source_matchers: NotificationMatcher[];
  target_matchers: NotificationMatcher[];
  equal: string[];
  version: number;
  updated_at: number;
};
type ActiveInstance = {
  rule_uid: string;
  instance_key: string;
  labels: Record<string, string>;
  state: string;
  started_at: number;
};
type Match = {
  inhibited: boolean;
  excluded_self_match: boolean;
  sources: ActiveInstance[];
  source_count: number;
};
type Preview = Match & {
  target_matches: boolean;
  source_matches: boolean;
  targets: (Match & { target: ActiveInstance })[];
  total_targets: number;
  truncated: boolean;
};
const base = "/alerting/inhibition-rules";
const conflict = (reason: unknown, removal = false) =>
  reason instanceof ApiError && reason.status === 409
    ? removal
      ? text(
          "This rule changed. Close this dialog and refresh the list before deleting it.",
          "规则已被修改。请关闭此窗口并刷新列表，再删除最新版本。",
        )
      : text(
          "This rule changed. Close and reopen the editor to load its latest version.",
          "规则已被修改。请关闭并重新打开编辑器，读取最新版本。",
        )
    : message(reason);

function MatcherSummary({ values }: { values: NotificationMatcher[] }) {
  return (
    <span>
      {values.map((matcher, index) => (
        <code key={index}>
          {matcher.name}
          {matcher.operator}
          {JSON.stringify(matcher.value)}
        </code>
      ))}
    </span>
  );
}
function InstanceLabels({ value }: { value: ActiveInstance }) {
  return (
    <div className="inhibition-rule-summary">
      <strong>{value.labels.alertname || value.rule_uid}</strong>
      <span className="notification-secondary">{t(value.state)}</span>
      <code>
        {Object.entries(value.labels)
          .map(([name, label]) => `${name}=${JSON.stringify(label)}`)
          .join(", ")}
      </code>
    </div>
  );
}

export function InhibitionRulesPanel({
  tick,
  notify,
}: {
  tick: number;
  notify: (value: string, error?: boolean) => void;
}) {
  const [rules, setRules] = useState<InhibitionRule[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<InhibitionRule | "new" | null>(null);
  const [removing, setRemoving] = useState<InhibitionRule | null>(null);
  const [removalError, setRemovalError] = useState("");
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState("");
  const reload = () => setRevision((value) => value + 1);
  useEffect(() => {
    const controller = new AbortController();
    void api<InhibitionRule[]>(base, { signal: controller.signal })
      .then((values) => {
        if (!controller.signal.aborted) {
          setRules(values);
          setLoaded(true);
          setError("");
        }
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError(message(reason));
      });
    return () => controller.abort();
  }, [tick, revision]);
  const query = search.trim().toLowerCase();
  const matches = rules.filter(
    (rule) =>
      !query ||
      JSON.stringify([
        rule.name,
        rule.source_matchers,
        rule.target_matchers,
        rule.equal,
      ])
        .toLowerCase()
        .includes(query),
  );
  return (
    <section
      className="list-panel inhibition-rules"
      aria-label={text("Inhibition rules", "告警抑制规则")}
    >
      <div className="section-heading">
        <div>
          <h2>
            <Ban size={18} />
            {text("Inhibition rules", "告警抑制规则")}
            <span>{rules.length}</span>
          </h2>
          <p>
            {text(
              "Suppress target notifications while a matching source alert is active. Evaluation and alert history continue.",
              "匹配的来源告警处于活动状态期间，抑制目标告警通知。告警评估和历史记录继续保留。",
            )}
          </p>
        </div>
        <div className="row-actions">
          <button onClick={reload}>
            <RefreshCw size={15} />
            {t("Refresh")}
          </button>
          <button
            className="primary"
            disabled={rules.length >= 100}
            onClick={() => setEditor("new")}
          >
            <Plus size={15} />
            {text("Add inhibition rule", "添加抑制规则")}
          </button>
        </div>
      </div>
      <div className="inhibition-search">
        <label>
          {text("Search rules", "搜索规则")}
          <input
            aria-label={text("Search inhibition rules", "搜索抑制规则")}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {!loaded && !error ? (
        <p role="status" className="notification-empty">
          {text("Loading inhibition rules…", "正在加载抑制规则…")}
        </p>
      ) : !matches.length ? (
        <p className="notification-empty">
          {text(
            "No inhibition rules match this view.",
            "当前筛选没有抑制规则。",
          )}
        </p>
      ) : (
        <div className="table-scroll">
          <table className="notification-table">
            <thead>
              <tr>
                <th>{t("Name")}</th>
                <th>{text("Source → Target", "来源 → 目标")}</th>
                <th>{text("Equal labels", "相同标签")}</th>
                <th>{t("Status")}</th>
                <th>{text("Actions", "操作")}</th>
              </tr>
            </thead>
            <tbody>
              {matches.map((rule) => (
                <tr key={rule.uid}>
                  <td>
                    <strong>{rule.name}</strong>
                    <div className="notification-secondary mono">
                      {rule.uid} · {text("Version", "版本")} {rule.version}
                    </div>
                  </td>
                  <td>
                    <div className="inhibition-rule-summary">
                      <MatcherSummary values={rule.source_matchers} />
                      <span>↓</span>
                      <MatcherSummary values={rule.target_matchers} />
                    </div>
                  </td>
                  <td>
                    {rule.equal.length
                      ? rule.equal.join(", ")
                      : text("Any label values", "不限制标签值")}
                  </td>
                  <td>
                    {text(
                      rule.disabled ? "Disabled" : "Enabled",
                      rule.disabled ? "已停用" : "已启用",
                    )}
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        aria-label={`${text("Edit inhibition rule", "编辑抑制规则")} ${rule.name}`}
                        onClick={() => setEditor(structuredClone(rule))}
                      >
                        <Pencil size={14} />
                        {t("Edit")}
                      </button>
                      <button
                        aria-label={`${text("Delete inhibition rule", "删除抑制规则")} ${rule.name}`}
                        onClick={() => {
                          setRemovalError("");
                          setRemoving(structuredClone(rule));
                        }}
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
      )}
      {editor && (
        <InhibitionEditor
          key={editor === "new" ? "new" : editor.uid}
          value={editor}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            reload();
            notify(text("Inhibition rule saved", "抑制规则已保存"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={text("Delete inhibition rule", "删除抑制规则")}
          onClose={() => {
            if (!busy) setRemoving(null);
          }}
        >
          <strong>{removing.name}</strong>
          <p className="inhibition-help">
            {text(
              "Still-firing target alerts can resume notifications after this rule is removed.",
              "移除规则后，仍在触发的目标告警可以恢复发送通知。",
            )}
          </p>
          {removalError && (
            <p role="alert" className="form-error">
              {removalError}
            </p>
          )}
          <div className="form-actions">
            <button disabled={busy} onClick={() => setRemoving(null)}>
              {t("Cancel")}
            </button>
            <button
              className="danger"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                setRemovalError("");
                try {
                  await api(
                    `${base}/${encodeURIComponent(removing.uid)}?version=${removing.version}`,
                    { method: "DELETE" },
                  );
                  setRemoving(null);
                  reload();
                  notify(text("Inhibition rule deleted", "抑制规则已删除"));
                } catch (reason) {
                  setRemovalError(conflict(reason, true));
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

function InhibitionEditor({
  value,
  onClose,
  onSaved,
}: {
  value: InhibitionRule | "new";
  onClose: () => void;
  onSaved: () => void;
}) {
  const original = value === "new" ? null : value;
  const [name, setName] = useState(original?.name || "");
  const [disabled, setDisabled] = useState(original?.disabled || false);
  const [source, setSource] = useState<NotificationMatcher[]>(
    original?.source_matchers || [
      { name: "severity", operator: "=", value: "critical" },
    ],
  );
  const [target, setTarget] = useState<NotificationMatcher[]>(
    original?.target_matchers || [
      { name: "severity", operator: "=", value: "warning" },
    ],
  );
  const [equal, setEqual] = useState(
    original ? original.equal.join(", ") : "cluster",
  );
  const [labels, setLabels] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  function clearPreview() {
    controller.current?.abort();
    controller.current = null;
    setPreview(null);
    setError("");
    setBusy((state) => (state === "preview" ? "" : state));
  }
  const draft = () => ({
    uid: original?.uid,
    name: name.trim(),
    disabled,
    source_matchers: source,
    target_matchers: target,
    equal: [...new Set(equal.split(/[\s,]+/).filter(Boolean))],
    version: original?.version || 0,
  });
  return (
    <Dialog
      title={text(
        original ? "Edit inhibition rule" : "Add inhibition rule",
        original ? "编辑抑制规则" : "添加抑制规则",
      )}
      style={{ width: 1000 }}
      onClose={() => {
        if (busy !== "save") onClose();
      }}
    >
      <form
        className="inhibition-editor"
        onSubmit={async (event) => {
          event.preventDefault();
          controller.current?.abort();
          setBusy("save");
          setError("");
          try {
            await api(
              original ? `${base}/${encodeURIComponent(original.uid)}` : base,
              { method: original ? "PUT" : "POST", body: jsonBody(draft()) },
            );
            onSaved();
          } catch (reason) {
            setError(conflict(reason));
          } finally {
            setBusy("");
          }
        }}
      >
        <div className="inhibition-editor-body">
          <label>
            {text("Rule name", "规则名称")}
            <input
              autoFocus
              aria-label={text("Rule name", "规则名称")}
              required
              maxLength={256}
              disabled={busy === "save"}
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                clearPreview();
              }}
            />
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={!disabled}
              disabled={busy === "save"}
              onChange={(event) => {
                setDisabled(!event.target.checked);
                clearPreview();
              }}
            />
            {text("Enabled", "启用")}
          </label>
          <MatcherFields
            title={text("Source alert matchers", "来源告警匹配条件")}
            prefix={text("Source", "来源")}
            values={source}
            disabled={busy === "save"}
            onChange={(values) => {
              setSource(values);
              clearPreview();
            }}
          />
          <MatcherFields
            title={text("Target alert matchers", "目标告警匹配条件")}
            prefix={text("Target", "目标")}
            values={target}
            disabled={busy === "save"}
            onChange={(values) => {
              setTarget(values);
              clearPreview();
            }}
          />
          <label>
            {text("Equal labels", "相同标签")}
            <input
              aria-label={text("Equal labels", "相同标签")}
              value={equal}
              maxLength={8192}
              disabled={busy === "save"}
              placeholder="cluster, namespace"
              onChange={(event) => {
                setEqual(event.target.value);
                clearPreview();
              }}
            />
          </label>
          <p className="inhibition-help">
            {text(
              "Comma-separated labels must have the same values on both alerts. Missing and empty labels are equivalent. Without equal labels, any matching source can inhibit matching targets.",
              "逗号分隔的标签必须在来源与目标告警中具有相同值。缺失标签与空值等价。未填写时，任意匹配的来源都能抑制匹配的目标。",
            )}
          </p>
          <div className="inhibition-preview">
            <h3>{text("Match preview", "匹配预览")}</h3>
            <p className="inhibition-help">
              {text(
                "Uses real active alert instances. Optional target labels test one candidate without creating an alert. Alerts matching both sides are excluded from self-inhibition.",
                "使用实际活动告警实例。可选目标标签用于测试一个候选，不会创建告警。同时匹配来源与目标条件的告警会排除自我抑制。",
              )}
            </p>
            <label>
              {text("Target labels (JSON, optional)", "目标标签（JSON，可选）")}
              <textarea
                className="mono"
                rows={3}
                aria-label={text("Target labels JSON", "目标标签 JSON")}
                value={labels}
                disabled={busy === "save"}
                placeholder={'{"severity":"warning","cluster":"production"}'}
                onChange={(event) => {
                  setLabels(event.target.value);
                  clearPreview();
                }}
              />
            </label>
            <button
              type="button"
              disabled={!!busy}
              onClick={async () => {
                setError("");
                setBusy("preview");
                const current = new AbortController();
                controller.current = current;
                try {
                  let candidate: Record<string, string> | undefined;
                  if (labels.trim()) {
                    let parsed: unknown;
                    try {
                      parsed = JSON.parse(labels);
                    } catch {
                      throw new Error(
                        text(
                          "Invalid target labels JSON.",
                          "目标标签 JSON 格式无效。",
                        ),
                      );
                    }
                    if (
                      !parsed ||
                      typeof parsed !== "object" ||
                      Array.isArray(parsed) ||
                      Object.values(parsed).some(
                        (value) => typeof value !== "string",
                      )
                    )
                      throw new Error(
                        text(
                          "Target labels must be a JSON object with string values.",
                          "目标标签必须是值为字符串的 JSON 对象。",
                        ),
                      );
                    candidate = parsed as Record<string, string>;
                  }
                  const result = await api<Preview>(`${base}/preview`, {
                    method: "POST",
                    body: jsonBody({
                      source_matchers: source,
                      target_matchers: target,
                      equal: draft().equal,
                      disabled,
                      labels: candidate,
                      limit: 50,
                    }),
                    signal: current.signal,
                  });
                  if (!current.signal.aborted) setPreview(result);
                } catch (reason) {
                  if (!current.signal.aborted) setError(message(reason));
                } finally {
                  if (!current.signal.aborted) setBusy("");
                }
              }}
            >
              <Play size={14} />
              {text(
                busy === "preview" ? "Testing…" : "Test match",
                busy === "preview" ? "正在测试…" : "测试匹配",
              )}
            </button>
            {preview && (
              <div
                className="inhibition-preview-result"
                role="region"
                aria-label={text("Inhibition preview result", "抑制预览结果")}
              >
                {labels.trim() && (
                  <p role="status">
                    {preview.excluded_self_match
                      ? text(
                          "Candidate excluded from self-inhibition.",
                          "候选已排除自我抑制。",
                        )
                      : preview.inhibited
                        ? text(
                            "Candidate notifications are inhibited.",
                            "候选通知会被抑制。",
                          )
                        : text(
                            "Candidate notifications are not inhibited.",
                            "候选通知不会被抑制。",
                          )}
                  </p>
                )}
                <p>
                  {labels.trim() && (
                    <>
                      {text("Matching source alerts", "匹配的来源告警")}:{" "}
                      {preview.source_count} ·{" "}
                    </>
                  )}
                  {text("Matching target alerts", "匹配的目标告警")}:{" "}
                  {preview.total_targets}
                </p>
                {labels.trim() && (
                  <p>
                    {text("Matches target", "匹配目标")}:{" "}
                    {preview.target_matches ? text("Yes", "是") : text("No", "否")} ·{" "}
                    {text("Also matches source", "同时匹配来源")}:{" "}
                    {preview.source_matches ? text("Yes", "是") : text("No", "否")}
                  </p>
                )}
                {preview.sources.map((instance) => (
                  <InstanceLabels
                    key={`${instance.rule_uid}:${instance.instance_key}`}
                    value={instance}
                  />
                ))}
                {!!preview.targets.length && (
                  <table>
                    <thead>
                      <tr>
                        <th>{text("Target alert", "目标告警")}</th>
                        <th>{text("Result", "结果")}</th>
                        <th>{text("Source count", "来源数量")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {preview.targets.map((match) => (
                        <tr
                          key={`${match.target.rule_uid}:${match.target.instance_key}`}
                        >
                          <td>
                            <InstanceLabels value={match.target} />
                          </td>
                          <td>
                            {match.excluded_self_match
                              ? text("Self-match excluded", "已排除自我匹配")
                              : match.inhibited
                                ? text("Inhibited", "已抑制")
                                : text("Not inhibited", "未抑制")}
                          </td>
                          <td>
                            {match.source_count}
                            {match.sources.map((instance) => (
                              <InstanceLabels
                                key={`${instance.rule_uid}:${instance.instance_key}`}
                                value={instance}
                              />
                            ))}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
                {preview.truncated && (
                  <p className="inhibition-help">
                    {text(
                      "Showing the first 50 matching targets.",
                      "仅显示前 50 个匹配目标。",
                    )}
                  </p>
                )}
                {(preview.source_count > preview.sources.length ||
                  preview.targets.some(
                    (match) => match.source_count > match.sources.length,
                  )) && (
                  <p className="inhibition-help">
                    {text(
                      "Source labels are limited to 20 per target; counts include all matches.",
                      "每个目标最多列出 20 个来源标签，数量统计包括全部匹配。",
                    )}
                  </p>
                )}
              </div>
            )}
          </div>
          {error && (
            <p role="alert" className="form-error">
              {error}
            </p>
          )}
        </div>
        <div className="form-actions">
          <button type="button" disabled={busy === "save"} onClick={onClose}>
            {t("Cancel")}
          </button>
          <button className="primary" disabled={busy === "save"}>
            {text(
              busy === "save" ? "Saving…" : "Save inhibition rule",
              busy === "save" ? "正在保存…" : "保存抑制规则",
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function MatcherFields({
  title,
  prefix,
  values,
  onChange,
  disabled,
}: {
  title: string;
  prefix: string;
  values: NotificationMatcher[];
  onChange: (values: NotificationMatcher[]) => void;
  disabled: boolean;
}) {
  return (
    <fieldset className="inhibition-matchers" disabled={disabled}>
      <legend>{title}</legend>
      {values.map((matcher, index) => (
        <div className="inhibition-matcher-row" key={index}>
          <label>
            {text("Label", "标签")}
            <input
              aria-label={`${prefix} ${text("label", "标签")} ${index + 1}`}
              value={matcher.name}
              required
              maxLength={256}
              placeholder="severity"
              onChange={(event) =>
                onChange(
                  values.map((item, i) =>
                    i === index ? { ...item, name: event.target.value } : item,
                  ),
                )
              }
            />
          </label>
          <label>
            {text("Operator", "运算符")}
            <select
              aria-label={`${prefix} ${text("operator", "运算符")} ${index + 1}`}
              value={matcher.operator}
              onChange={(event) =>
                onChange(
                  values.map((item, i) =>
                    i === index
                      ? {
                          ...item,
                          operator: event.target
                            .value as NotificationMatcher["operator"],
                        }
                      : item,
                  ),
                )
              }
            >
              {["=", "!=", "=~", "!~"].map((operator) => (
                <option key={operator}>{operator}</option>
              ))}
            </select>
          </label>
          <label>
            {text("Value", "值")}
            <input
              aria-label={`${prefix} ${text("value", "值")} ${index + 1}`}
              value={matcher.value}
              maxLength={4096}
              onChange={(event) =>
                onChange(
                  values.map((item, i) =>
                    i === index ? { ...item, value: event.target.value } : item,
                  ),
                )
              }
            />
          </label>
          <button
            type="button"
            className="icon-button"
            aria-label={`${text("Remove", "移除")} ${prefix} ${index + 1}`}
            disabled={values.length < 2}
            onClick={() => onChange(values.filter((_, i) => i !== index))}
          >
            <X size={15} />
          </button>
        </div>
      ))}
      <button
        type="button"
        disabled={values.length >= 32}
        onClick={() =>
          onChange([...values, { name: "", operator: "=", value: "" }])
        }
      >
        <Plus size={14} />
        {text("Add matcher", "添加匹配条件")}
      </button>
    </fieldset>
  );
}
