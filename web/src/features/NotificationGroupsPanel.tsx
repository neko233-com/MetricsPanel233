import { useEffect, useState } from "react";
import { Layers } from "lucide-react";
import { api, message } from "../api";
import { getLocale } from "../i18n";
type Group = {
  key: string;
  receiver: string;
  group_labels: Record<string, string>;
  created_at: number;
  last_sent_at: number;
  next_at: number;
  inflight_id: number;
  resume_needed: boolean;
  firing: number;
  resolved: number;
  effective_repeat_interval: string;
};
const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
const date = (at: number) =>
  at ? new Date(at).toLocaleString(getLocale() === "zh" ? "zh-CN" : "en") : "—";
export function NotificationGroupsPanel({
  tick,
  points,
}: {
  tick: number;
  points: { uid: string; name: string }[];
}) {
  const [groups, setGroups] = useState<Group[]>([]),
    [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void api<Group[]>("/alerting/notification-groups", {
      signal: controller.signal,
    })
      .then((values) => {
        if (controller.signal.aborted) return;
        setGroups(values);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, [tick]);
  if (!groups.length && !error) return null;
  return (
    <section
      className="list-panel"
      aria-label={text("Notification groups", "通知分组")}
    >
      <div className="section-heading">
        <div>
          <h2>
            <Layers size={18} />
            {text("Notification groups", "通知分组")}
            <span>{groups.length}</span>
          </h2>
          <p>
            {text(
              "Current members and the next scheduled group check are retained across restarts.",
              "当前成员与下次分组检查时间保存在本地，重启后继续执行。",
            )}
          </p>
        </div>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{text("Contact point / group", "联系点 / 分组")}</th>
              <th>{text("Members", "成员")}</th>
              <th>{text("Last sent", "上次发送")}</th>
              <th>{text("Next check", "下次检查")}</th>
              <th>{text("Repeat interval", "重复间隔")}</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <tr key={g.key}>
                <td>
                  <strong>
                    {points.find((p) => p.uid === g.receiver)?.name ||
                      g.receiver}
                  </strong>
                  <div className="muted">
                    {Object.entries(g.group_labels)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(", ") || text("All alerts", "全部告警")}
                  </div>
                  {g.inflight_id > 0 && (
                    <small>
                      #{g.inflight_id} ·{" "}
                      {text("Delivery queued", "通知等待发送")}
                    </small>
                  )}
                  {g.resume_needed && (
                    <small>{text("Silenced members", "有静默成员")}</small>
                  )}
                </td>
                <td>
                  {g.firing} {text("firing", "触发")} · {g.resolved}{" "}
                  {text("resolved", "恢复")}
                </td>
                <td>{date(g.last_sent_at)}</td>
                <td>{date(g.next_at)}</td>
                <td>
                  {g.effective_repeat_interval === "0s"
                    ? text("Off", "关闭")
                    : g.effective_repeat_interval}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
