import { useEffect, useState } from "react";
import { CalendarClock, Pencil, Plus, Trash2, RefreshCw } from "lucide-react";
import { api, jsonBody, message } from "../api";
import { Dialog } from "../components/Dialog";
import { getLocale } from "../i18n";
import "./mute-timings.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
type Interval = {
  times?: { start_time: string; end_time: string }[];
  weekdays?: string[];
  location?: string;
  days_of_month?: string[];
  months?: string[];
  years?: string[];
};
type MuteTiming = {
  name: string;
  time_intervals: Interval[];
  version: number;
  updated_at?: number;
};
const localDate = (date: Date) =>
  new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
const names = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
];
const chinese = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];

export function MuteTimingsPanel({
  tick,
  notify,
  onNames,
}: {
  tick: number;
  notify: (value: string, error?: boolean) => void;
  onNames: (names: string[]) => void;
}) {
  const [values, setValues] = useState<MuteTiming[]>([]),
    [error, setError] = useState(""),
    [revision, setRevision] = useState(0),
    [editing, setEditing] = useState<MuteTiming | "new" | null>(null),
    [removing, setRemoving] = useState<MuteTiming | null>(null),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    const c = new AbortController();
    void api<MuteTiming[]>("/alerting/mute-timings", { signal: c.signal })
      .then((v) => {
        if (c.signal.aborted) return;
        setValues(v);
        onNames(v.map((x) => x.name));
        setError("");
      })
      .catch((e) => {
        if (!c.signal.aborted) setError(message(e));
      });
    return () => c.abort();
  }, [tick, revision, onNames]);
  return (
    <section
      className="list-panel mute-timings-panel"
      aria-label={text("Mute time intervals", "静默时间段")}
    >
      <div className="section-heading">
        <div>
          <h2>
            <CalendarClock size={18} />
            {text("Mute time intervals", "静默时间段")}
            <span>{values.length}</span>
          </h2>
          <p>
            {text(
              "Recurring calendar windows in an IANA time zone. Assign them to notification routes.",
              "按时区设置周期性时间段，再将它们用于通知路由。",
            )}
          </p>
        </div>
        <div className="form-actions">
          <button
            onClick={() => setRevision((x) => x + 1)}
            aria-label={text("Refresh", "刷新")}
          >
            <RefreshCw size={15} />
          </button>
          <button className="primary" onClick={() => setEditing("new")}>
            <Plus size={15} />
            {text("Add time interval", "添加时间段")}
          </button>
        </div>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {values.length ? (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{text("Name", "名称")}</th>
                <th>{text("Calendar", "日历规则")}</th>
                <th>{text("Time zone", "时区")}</th>
                <th>{text("Actions", "操作")}</th>
              </tr>
            </thead>
            <tbody>
              {values.map((v) => (
                <tr key={v.name}>
                  <td>
                    <strong>{v.name}</strong>
                  </td>
                  <td>
                    {v.time_intervals.map((i, n) => (
                      <div key={n}>
                        {i.times
                          ?.map((t) => `${t.start_time}–${t.end_time}`)
                          .join(", ") || text("All day", "全天")}
                        {i.weekdays?.length
                          ? ` · ${i.weekdays.join(", ")}`
                          : ""}
                        {i.days_of_month?.length
                          ? ` · ${i.days_of_month.join(", ")}`
                          : ""}
                        {i.months?.length ? ` · ${i.months.join(", ")}` : ""}
                        {i.years?.length ? ` · ${i.years.join(", ")}` : ""}
                      </div>
                    ))}
                  </td>
                  <td>
                    {[
                      ...new Set(
                        v.time_intervals.map((i) => i.location || "UTC"),
                      ),
                    ].join(", ")}
                  </td>
                  <td className="notification-actions">
                    <button
                      onClick={() => setEditing(v)}
                      aria-label={text("Edit time interval", "编辑时间段")}
                    >
                      <Pencil size={14} />
                    </button>
                    <button
                      className="danger-hover"
                      onClick={() => setRemoving(v)}
                      aria-label={text("Delete time interval", "删除时间段")}
                    >
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="notification-empty">
          {text(
            "No recurring time intervals configured.",
            "尚未设置周期性时间段。",
          )}
        </p>
      )}
      {editing && (
        <MuteTimingEditor
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            setRevision((x) => x + 1);
            notify(text("Time interval saved", "时间段已保存"));
          }}
        />
      )}
      {removing && (
        <Dialog
          title={text("Delete time interval", "删除时间段")}
          onClose={() => {
            if (!busy) {
              setRemoving(null);
              setError("");
            }
          }}
        >
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              try {
                await api(
                  `/alerting/mute-timings/${encodeURIComponent(removing.name)}`,
                  { method: "DELETE" },
                );
                setRemoving(null);
                setRevision((x) => x + 1);
                notify(text("Time interval deleted", "时间段已删除"));
              } catch (e) {
                setError(message(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <p>
              {text("Delete", "删除")} <strong>{removing.name}</strong>?
            </p>
            {error && (
              <p role="alert" className="form-error">
                {error}
              </p>
            )}
            <div className="form-actions">
              <button
                type="button"
                disabled={busy}
                onClick={() => {
                  setRemoving(null);
                  setError("");
                }}
              >
                {text("Cancel", "取消")}
              </button>
              <button className="danger" disabled={busy}>
                {text("Delete", "删除")}
              </button>
            </div>
          </form>
        </Dialog>
      )}
    </section>
  );
}

function MuteTimingEditor({
  value,
  onClose,
  onSaved,
}: {
  value: MuteTiming | "new";
  onClose: () => void;
  onSaved: () => void;
}) {
  const isNew = value === "new";
  const [name, setName] = useState(isNew ? "" : value.name),
    [raw, setRaw] = useState(
      JSON.stringify(
        isNew
          ? [
              {
                times: [{ start_time: "22:00", end_time: "24:00" }],
                location: "Asia/Shanghai",
              },
            ]
          : value.time_intervals,
        null,
        2,
      ),
    ),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [at, setAt] = useState(localDate(new Date())),
    [preview, setPreview] = useState<boolean | null>(null);
  let intervals: Interval[] = [];
  try {
    const v: unknown = JSON.parse(raw);
    if (Array.isArray(v)) intervals = v as Interval[];
  } catch {
    /* Raw editor remains editable until validation. */
  }
  const first = intervals[0] || {};
  function patchFirst(update: Partial<Interval>) {
    if (!intervals.length) {
      setError(
        text(
          "Fix the calendar JSON before using the quick fields.",
          "请先修正日历 JSON，再使用快捷字段。",
        ),
      );
      return;
    }
    setRaw(
      JSON.stringify([{ ...first, ...update }, ...intervals.slice(1)], null, 2),
    );
    setPreview(null);
    setError("");
  }
  function input(): MuteTiming {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed))
      throw new Error(text("Calendar must be an array", "日历必须为数组"));
    return {
      name,
      time_intervals: parsed as Interval[],
      version: isNew ? 0 : value.version,
    };
  }
  return (
    <Dialog
      title={
        isNew
          ? text("Add time interval", "添加时间段")
          : text("Edit time interval", "编辑时间段")
      }
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="mute-timing-editor"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            const v = input();
            await api(
              isNew
                ? "/alerting/mute-timings"
                : `/alerting/mute-timings/${encodeURIComponent(name)}`,
              { method: isNew ? "POST" : "PUT", body: jsonBody(v) },
            );
            onSaved();
          } catch (e) {
            setError(message(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {text("Interval name", "时间段名称")}
          <input
            aria-label={text("Interval name", "时间段名称")}
            required
            maxLength={128}
            value={name}
            disabled={busy || !isNew}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <div className="form-row">
          <label>
            {text("Start time", "开始时间")}
            <input
              aria-label={text("Start time", "开始时间")}
              value={first.times?.[0]?.start_time || ""}
              placeholder="00:00"
              disabled={busy}
              onChange={(e) =>
                patchFirst({
                  times: [
                    {
                      start_time: e.target.value,
                      end_time: first.times?.[0]?.end_time || "24:00",
                    },
                    ...(first.times?.slice(1) || []),
                  ],
                })
              }
            />
          </label>
          <label>
            {text("End time", "结束时间")}
            <input
              aria-label={text("End time", "结束时间")}
              value={first.times?.[0]?.end_time || ""}
              placeholder="24:00"
              disabled={busy}
              onChange={(e) =>
                patchFirst({
                  times: [
                    {
                      start_time: first.times?.[0]?.start_time || "00:00",
                      end_time: e.target.value,
                    },
                    ...(first.times?.slice(1) || []),
                  ],
                })
              }
            />
          </label>
          <label>
            {text("IANA time zone", "IANA 时区")}
            <input
              aria-label={text("IANA time zone", "IANA 时区")}
              value={first.location || "UTC"}
              disabled={busy}
              placeholder="Asia/Shanghai"
              onChange={(e) => patchFirst({ location: e.target.value })}
            />
          </label>
        </div>
        <fieldset>
          <legend>
            {text("Weekdays (none means every day)", "星期（不选则每天）")}
          </legend>
          <div className="mute-weekdays">
            {names.map((v, i) => (
              <label className="checkbox-label" key={v}>
                <input
                  type="checkbox"
                  checked={!!first.weekdays?.includes(v)}
                  disabled={busy}
                  onChange={(e) =>
                    patchFirst({
                      weekdays: e.target.checked
                        ? [...(first.weekdays || []), v]
                        : (first.weekdays || []).filter((x) => x !== v),
                    })
                  }
                />
                {text(v, chinese[i])}
              </label>
            ))}
          </div>
        </fieldset>
        <details>
          <summary>
            {text(
              "Calendar JSON · multiple windows, months, years and day ranges",
              "日历 JSON · 多个时间窗口、月份、年份与日期范围",
            )}
          </summary>
          <label>
            {text("Time interval JSON", "时间段 JSON")}
            <textarea
              aria-label={text("Time interval JSON", "时间段 JSON")}
              rows={10}
              spellCheck={false}
              value={raw}
              disabled={busy}
              onChange={(e) => {
                setRaw(e.target.value);
                setPreview(null);
              }}
            />
          </label>
          <p>
            {text(
              "End times are exclusive. Use two windows to cross midnight. Negative days count from the end of the month.",
              "结束时间不包含在时间段内。跨午夜请使用两个窗口；负数日期从月末倒数。",
            )}
          </p>
        </details>
        <div className="mute-preview">
          <label>
            {text("Preview timestamp", "预览时间")}
            <input
              type="datetime-local"
              aria-label={text("Preview timestamp", "预览时间")}
              value={at}
              onChange={(e) => {
                setAt(e.target.value);
                setPreview(null);
              }}
              disabled={busy}
            />
          </label>
          <button
            type="button"
            disabled={busy || !name || !at}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                const result = await api<{ matches: boolean }>(
                  "/alerting/mute-timings/preview",
                  {
                    method: "POST",
                    body: jsonBody({
                      timing: input(),
                      at: new Date(at).toISOString(),
                    }),
                  },
                );
                setPreview(result.matches);
              } catch (e) {
                setError(message(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            {text("Check time", "检查时间")}
          </button>
          {preview !== null && (
            <strong role="status">
              {preview
                ? text("Within interval", "在时间段内")
                : text("Outside interval", "不在时间段内")}
            </strong>
          )}
        </div>
        {error && (
          <p role="alert" className="form-error">
            {error}
          </p>
        )}
        <div className="form-actions">
          <button type="button" disabled={busy} onClick={onClose}>
            {text("Cancel", "取消")}
          </button>
          <button className="primary" disabled={busy}>
            {text("Save time interval", "保存时间段")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
