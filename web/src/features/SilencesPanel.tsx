import { useEffect, useRef, useState } from "react";
import { Clock, Pencil, Plus, RefreshCw, VolumeX, X } from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale, t } from "../i18n";
import "./silences.css";

type Matcher = {
  name: string;
  value: string;
  isRegex: boolean;
  isEqual?: boolean;
};
type Silence = {
  id: string;
  matchers: Matcher[];
  startsAt: string;
  endsAt: string;
  updatedAt: string;
  createdBy: string;
  comment: string;
  annotations?: Record<string, string>;
  status: { state: "active" | "pending" | "expired" };
};
const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
const date = (value: string) =>
  new Date(value).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en");
const operator = (m: Matcher) =>
  `${m.isEqual === false ? "!" : "="}${m.isRegex ? "~" : m.isEqual === false ? "=" : ""}`;
const stateLabel = (state: Silence["status"]["state"]) =>
  ({
    active: text("Active", "生效中"),
    pending: text("Scheduled", "尚未开始"),
    expired: text("Expired", "已结束"),
  })[state];

export function SilencesPanel({
  tick,
  notify,
}: {
  tick: number;
  notify: (value: string, error?: boolean) => void;
}) {
  const [silences, setSilences] = useState<Silence[]>([]);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<Silence | "new" | null>(null);
  const [expiring, setExpiring] = useState<Silence | null>(null);
  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState("active");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void api<Silence[]>("/alerting/silences", { signal: controller.signal })
      .then((values) => {
        setSilences(values);
        setLoaded(true);
        setError("");
      })
      .catch((reason) => {
        if (!controller.signal.aborted) setError(message(reason));
      });
    return () => controller.abort();
  }, [tick, revision]);
  const query = search.trim().toLowerCase();
  const matches = silences.filter(
    (silence) =>
      (!filter || silence.status.state === filter) &&
      (!query ||
        `${silence.comment} ${silence.createdBy} ${silence.matchers.map((m) => `${m.name}${operator(m)}${m.value}`).join(" ")}`
          .toLowerCase()
          .includes(query)),
  );
  const currentPage = Math.min(
    page,
    Math.max(0, Math.ceil(matches.length / 40) - 1),
  );
  return (
    <section
      className="list-panel silences-panel"
      aria-label={text("Silences", "静默")}
    >
      <div className="section-heading">
        <div>
          <h2>
            <VolumeX size={18} />
            {text("Silences", "静默")}
          </h2>
          <p>
            {text(
              "Pause notifications by labels and time. Alert evaluation and history continue.",
              "按标签和时间暂停通知，告警评估与历史记录继续保留。",
            )}
          </p>
        </div>
        <div className="row-actions">
          <button onClick={() => setRevision((n) => n + 1)}>
            <RefreshCw size={15} />
            {t("Refresh")}
          </button>
          <button className="primary" onClick={() => setEditor("new")}>
            <Plus size={15} />
            {text("New silence", "新建静默")}
          </button>
        </div>
      </div>
      <div className="silence-filters">
        <label>
          <span>{text("State", "状态")}</span>
          <select
            aria-label={text("Silence state", "静默状态")}
            value={filter}
            onChange={(event) => {
              setFilter(event.target.value);
              setPage(0);
            }}
          >
            <option value="">{text("All states", "所有状态")}</option>
            <option value="active">{text("Active", "生效中")}</option>
            <option value="pending">{text("Scheduled", "尚未开始")}</option>
            <option value="expired">{text("Expired", "已结束")}</option>
          </select>
        </label>
        <label>
          <span>{text("Search", "搜索")}</span>
          <input
            aria-label={text("Search silences", "搜索静默")}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setPage(0);
            }}
            placeholder={text("Comment, author or labels", "备注、作者或标签")}
          />
        </label>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!loaded && !error ? (
        <p className="notification-empty" role="status">
          {text("Loading silences…", "正在加载静默…")}
        </p>
      ) : matches.length ? (
        <div className="table-scroll notification-table">
          <table>
            <thead>
              <tr>
                <th>{text("Labels / Comment", "标签 / 备注")}</th>
                <th>{t("Status")}</th>
                <th>{text("Schedule", "时间")}</th>
                <th>{text("Author", "作者")}</th>
                <th>{text("Actions", "操作")}</th>
              </tr>
            </thead>
            <tbody>
              {matches
                .slice(currentPage * 40, currentPage * 40 + 40)
                .map((silence) => (
                  <tr key={silence.id}>
                    <td>
                      <div className="silence-matchers">
                        {silence.matchers.map((m, i) => (
                          <code key={i}>
                            {m.name}
                            {operator(m)}
                            {JSON.stringify(m.value)}
                          </code>
                        ))}
                      </div>
                      <div className="notification-secondary">
                        {silence.comment}
                      </div>
                    </td>
                    <td>
                      <span className={`silence-state ${silence.status.state}`}>
                        <Clock size={13} />
                        {stateLabel(silence.status.state)}
                      </span>
                    </td>
                    <td>
                      <div>{date(silence.startsAt)}</div>
                      <div className="notification-secondary">
                        → {date(silence.endsAt)}
                      </div>
                    </td>
                    <td>{silence.createdBy}</td>
                    <td>
                      <div className="row-actions">
                        <button onClick={() => setEditor(silence)}>
                          <Pencil size={13} />
                          {t("Edit")}
                        </button>
                        {silence.status.state !== "expired" && (
                          <button onClick={() => setExpiring(silence)}>
                            {text("End silence", "结束静默")}
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      ) : loaded ? (
        <p className="notification-empty">
          {text("No silences match this view.", "当前筛选没有静默规则。")}
        </p>
      ) : null}
      {matches.length > 40 && (
        <div className="silence-pagination">
          <button
            disabled={currentPage === 0}
            onClick={() => setPage(currentPage - 1)}
          >
            {text("Previous", "上一页")}
          </button>
          <span>
            {currentPage + 1} / {Math.ceil(matches.length / 40)}
          </span>
          <button
            disabled={(currentPage + 1) * 40 >= matches.length}
            onClick={() => setPage(currentPage + 1)}
          >
            {text("Next", "下一页")}
          </button>
        </div>
      )}
      {editor !== null && (
        <SilenceEditor
          key={editor === "new" ? "new" : editor.id}
          value={editor === "new" ? null : editor}
          onClose={() => setEditor(null)}
          onSaved={() => {
            setEditor(null);
            setRevision((n) => n + 1);
            notify(text("Silence saved", "静默已保存"));
          }}
        />
      )}
      {expiring && (
        <Dialog
          title={text("End silence", "结束静默")}
          onClose={() => {
            if (!busy) setExpiring(null);
          }}
        >
          <p>{expiring.comment}</p>
          <p>
            {text(
              "Matching alerts that are still firing will resume notifications. Recovered alerts will not be replayed.",
              "仍在触发的匹配告警将恢复通知，已经恢复的告警不会补发旧通知。",
            )}
          </p>
          <div className="form-actions">
            <button disabled={busy} onClick={() => setExpiring(null)}>
              {t("Cancel")}
            </button>
            <button
              className="primary"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await api(
                    `/alerting/silences/${encodeURIComponent(expiring.id)}`,
                    { method: "DELETE" },
                  );
                  setExpiring(null);
                  setRevision((n) => n + 1);
                  notify(text("Silence ended", "静默已结束"));
                } catch (reason) {
                  notify(message(reason), true);
                } finally {
                  setBusy(false);
                }
              }}
            >
              {text(
                busy ? "Ending…" : "End silence",
                busy ? "正在结束…" : "结束静默",
              )}
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}

function localTime(value: string | number) {
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}
function SilenceEditor({
  value,
  onClose,
  onSaved,
}: {
  value: Silence | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [matchers, setMatchers] = useState<Matcher[]>(
    () =>
      value?.matchers.map((m) => ({ ...m })) || [
        { name: "alertname", value: "", isRegex: false, isEqual: true },
      ],
  );
  const [startsAt, setStartsAt] = useState(() =>
    localTime(
      value?.status.state === "expired"
        ? Date.now()
        : value?.startsAt || Date.now(),
    ),
  );
  const [endsAt, setEndsAt] = useState(() =>
    localTime(
      value?.status.state === "expired"
        ? Date.now() + 7200000
        : value?.endsAt || Date.now() + 7200000,
    ),
  );
  const [author, setAuthor] = useState(value?.createdBy || "");
  const [comment, setComment] = useState(value?.comment || "");
  const [labels, setLabels] = useState('{"alertname":"My alert"}');
  const [preview, setPreview] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const previewRequest = useRef(0);
  const changeMatchers = (values: Matcher[]) => {
    previewRequest.current++;
    setPreview("");
    setChecking(false);
    setMatchers(values);
  };
  return (
    <Dialog
      title={text(
        value ? "Edit silence" : "New silence",
        value ? "编辑静默" : "新建静默",
      )}
      onClose={() => {
        if (!busy) onClose();
      }}
      style={{ maxWidth: 820, width: "calc(100vw - 160px)" }}
    >
      <form
        className="silence-editor"
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setError("");
          try {
            await api("/alerting/silences", {
              method: "POST",
              body: jsonBody({
                id: value?.id || undefined,
                matchers,
                startsAt: new Date(startsAt).toISOString(),
                endsAt: new Date(endsAt).toISOString(),
                createdBy: author.trim(),
                comment: comment.trim(),
                annotations: value?.annotations || {},
              }),
            });
            onSaved();
          } catch (reason) {
            setError(message(reason));
          } finally {
            setBusy(false);
          }
        }}
      >
        <fieldset disabled={busy}>
          <legend>{text("Label matchers", "标签匹配")}</legend>
          {matchers.map((m, index) => (
            <div className="silence-matcher-row" key={index}>
              <input
                required
                aria-label={`${text("Matcher label", "匹配标签")} ${index + 1}`}
                maxLength={128}
                value={m.name}
                onChange={(event) =>
                  changeMatchers(
                    matchers.map((item, i) =>
                      i === index
                        ? { ...item, name: event.target.value }
                        : item,
                    ),
                  )
                }
              />
              <select
                aria-label={`${text("Matcher operator", "匹配运算符")} ${index + 1}`}
                value={operator(m)}
                onChange={(event) =>
                  changeMatchers(
                    matchers.map((item, i) =>
                      i === index
                        ? {
                            ...item,
                            isRegex: event.target.value.endsWith("~"),
                            isEqual: !event.target.value.startsWith("!"),
                          }
                        : item,
                    ),
                  )
                }
              >
                <option value="=">=</option>
                <option value="!=">!=</option>
                <option value="=~">=~</option>
                <option value="!~">!~</option>
              </select>
              <input
                aria-label={`${text("Matcher value", "匹配值")} ${index + 1}`}
                maxLength={4096}
                value={m.value}
                onChange={(event) =>
                  changeMatchers(
                    matchers.map((item, i) =>
                      i === index
                        ? { ...item, value: event.target.value }
                        : item,
                    ),
                  )
                }
              />
              <button
                type="button"
                className="icon-button"
                disabled={matchers.length <= 1}
                aria-label={`${text("Remove matcher", "删除匹配条件")} ${index + 1}`}
                onClick={() =>
                  changeMatchers(matchers.filter((_, i) => i !== index))
                }
              >
                <X size={15} />
              </button>
            </div>
          ))}
          <button
            type="button"
            disabled={matchers.length >= 32}
            onClick={() =>
              changeMatchers([
                ...matchers,
                { name: "", value: "", isRegex: false, isEqual: true },
              ])
            }
          >
            <Plus size={14} />
            {text("Add matcher", "添加匹配条件")}
          </button>
        </fieldset>
        <div className="silence-schedule">
          <label>
            {text("Starts at (local time)", "开始时间（本地时间）")}
            <input
              required
              type="datetime-local"
              value={startsAt}
              onChange={(event) => setStartsAt(event.target.value)}
            />
          </label>
          <label>
            {text("Ends at (local time)", "结束时间（本地时间）")}
            <input
              required
              type="datetime-local"
              value={endsAt}
              onChange={(event) => setEndsAt(event.target.value)}
            />
          </label>
        </div>
        <label>
          {text("Created by", "作者")}
          <input
            required
            maxLength={256}
            value={author}
            onChange={(event) => setAuthor(event.target.value)}
          />
        </label>
        <label>
          {text("Comment", "备注")}
          <textarea
          aria-label={text("Comment", "备注")}
            required
            maxLength={4096}
            value={comment}
            onChange={(event) => setComment(event.target.value)}
            rows={2}
          />
        </label>
        <fieldset disabled={busy}>
          <legend>{text("Test labels", "测试标签")}</legend>
          <textarea
            aria-label={text("Test labels JSON", "测试标签 JSON")}
            rows={2}
            className="mono"
            value={labels}
            onChange={(event) => {
              previewRequest.current++;
              setPreview("");
              setChecking(false);
              setLabels(event.target.value);
            }}
          />
          <button
            type="button"
            disabled={checking}
            onClick={async () => {
              const request = ++previewRequest.current;
              setChecking(true);
              setPreview("");
              setError("");
              try {
                const result = await api<{ matches: boolean }>(
                  "/alerting/silences/preview",
                  {
                    method: "POST",
                    body: jsonBody({
                      matchers,
                      labels: JSON.parse(labels) as unknown,
                    }),
                  },
                );
                if (request === previewRequest.current)
                  setPreview(
                    text(
                      result.matches
                        ? "Labels match this silence."
                        : "Labels do not match this silence.",
                      result.matches
                        ? "这些标签匹配此静默。"
                        : "这些标签不匹配此静默。",
                    ),
                  );
              } catch (reason) {
                if (request === previewRequest.current)
                  setError(message(reason));
              } finally {
                if (request === previewRequest.current) setChecking(false);
              }
            }}
          >
            {text(
              checking ? "Checking…" : "Test match",
              checking ? "正在检查…" : "测试匹配",
            )}
          </button>
          {preview && <p role="status">{preview}</p>}
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
              busy ? "Saving…" : "Save silence",
              busy ? "正在保存…" : "保存静默",
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
