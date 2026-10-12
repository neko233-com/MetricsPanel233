import { ArrowDown, ArrowUp, Plus, Trash2, X } from "lucide-react";
import { getLocale } from "../i18n";
import "./notification-routing.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
function duration(value: string): number {
  let total = 0,
    matched = "";
  const units: Record<string, number> = {
    ms: 1,
    s: 1000,
    m: 60000,
    h: 3600000,
    d: 86400000,
    w: 604800000,
    y: 31536000000,
  };
  for (const m of value.matchAll(/(\d+)(ms|[smhdwy])/g)) {
    matched += m[0];
    total += Number(m[1]) * units[m[2]];
  }
  return matched === value ? total : NaN;
}
function repeatInterval(
  repeat: string | undefined,
  interval: string | undefined,
): string {
  if (!repeat || repeat === "0s") return text("Off", "关闭");
  const r = duration(repeat),
    i = duration(interval || "");
  if (!Number.isFinite(r) || !Number.isFinite(i) || i === 0) return repeat;
  const effective = Math.ceil(r / i) * i;
  return effective % 1000 ? `${effective}ms` : `${effective / 1000}s`;
}
export type NotificationMatcher = {
  name: string;
  operator: "=" | "!=" | "=~" | "!~";
  value: string;
};
export type NotificationTiming = {
  group_by?: string[];
  group_wait?: string;
  group_interval?: string;
  repeat_interval?: string;
};
export type NotificationRoute = NotificationTiming & {
  receiver: string;
  matchers?: NotificationMatcher[];
  continue: boolean;
  routes?: NotificationRoute[];
  mute_time_intervals?: string[];
  active_time_intervals?: string[];
};
export type NotificationPolicy = NotificationTiming & {
  version: number;
  receiver: string;
  routes: NotificationRoute[];
  updated_at?: number;
};
type Point = { uid: string; name: string };

export function NotificationTimingFields({
  value,
  inherited = {},
  onChange,
  busy = false,
  root = false,
}: {
  value: NotificationTiming;
  inherited?: NotificationTiming;
  onChange: (value: NotificationTiming) => void;
  busy?: boolean;
  root?: boolean;
}) {
  const effective = { ...inherited, ...value };
  const grouping =
    value.group_by === undefined
      ? "inherit"
      : value.group_by.length === 0
        ? "all"
        : value.group_by[0] === "..."
          ? "labels"
          : "custom";
  return (
    <div className="notification-timing-fields">
      <label>
        {text("Group by", "分组标签")}
        <select
          aria-label={text("Group by", "分组标签")}
          value={grouping}
          disabled={busy}
          onChange={(e) => {
            const next = { ...value };
            if (e.target.value === "inherit") delete next.group_by;
            else
              next.group_by =
                e.target.value === "all"
                  ? []
                  : e.target.value === "labels"
                    ? ["..."]
                    : ["alertname"];
            onChange(next);
          }}
        >
          <option value="inherit">
            {root
              ? text("Unspecified", "未指定")
              : text("Inherit parent", "继承父级")}
          </option>
          <option value="all">
            {text("One group for all alerts", "全部告警一个分组")}
          </option>
          <option value="labels">
            {text("All labels (each instance)", "全部标签（每个实例）")}
          </option>
          <option value="custom">{text("Selected labels", "指定标签")}</option>
        </select>
      </label>
      {grouping === "custom" && (
        <label>
          {text("Grouping labels", "标签列表")}
          <input
            aria-label={text("Grouping labels", "标签列表")}
            key={(value.group_by || []).join(",")}
            defaultValue={(value.group_by || []).join(", ")}
            disabled={busy}
            placeholder="alertname, team"
            onBlur={(e) =>
              onChange({
                ...value,
                group_by: e.target.value
                  .split(",")
                  .map((v) => v.trim())
                  .filter(Boolean),
              })
            }
          />
        </label>
      )}
      {(
        [
          ["group_wait", "Group wait", "首次等待"],
          ["group_interval", "Group interval", "分组间隔"],
          ["repeat_interval", "Repeat interval", "重复间隔"],
        ] as const
      ).map(([field, en, zh]) => (
        <label key={field}>
          {text(en, zh)}
          <input
            aria-label={text(en, zh)}
            disabled={busy}
            value={value[field] || ""}
            placeholder={inherited[field] || "0s"}
            maxLength={32}
            onChange={(e) => {
              const next = { ...value };
              if (e.target.value) next[field] = e.target.value;
              else delete next[field];
              onChange(next);
            }}
          />
        </label>
      ))}
      <p className="notification-effective">
        {text("Effective:", "实际生效：")} {text("wait", "等待")}{" "}
        {effective.group_wait || "0s"} · {text("interval", "间隔")}{" "}
        {effective.group_interval || "0s"} · {text("repeat", "重复")}{" "}
        {repeatInterval(effective.repeat_interval, effective.group_interval)}
        {root &&
        !value.group_by &&
        !value.group_wait &&
        !value.group_interval &&
        !value.repeat_interval
          ? text(" · Independent immediate notifications", " · 逐条立即发送")
          : ""}
      </p>
    </div>
  );
}

function newRoute(points: Point[]): NotificationRoute {
  return {
    receiver: points[0]?.uid || "",
    matchers: [{ name: "severity", operator: "=", value: "critical" }],
    continue: false,
  };
}

export function NotificationRoutingEditor({
  routes,
  points,
  timings = [],
  inherited,
  busy,
  onChange,
  depth = 0,
}: {
  routes: NotificationRoute[];
  points: Point[];
  timings?: string[];
  inherited: NotificationTiming;
  busy: boolean;
  onChange: (routes: NotificationRoute[]) => void;
  depth?: number;
}) {
  const patch = (i: number, v: NotificationRoute) =>
    onChange(routes.map((r, n) => (n === i ? v : r)));
  const move = (i: number, d: number) => {
    const next = [...routes];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    onChange(next);
  };
  return (
    <div className="notification-routing-tree">
      <div className="notification-route-heading">
        <h3>
          {depth
            ? text("Child routes", "子路由")
            : text("Label routes", "标签路由")}
        </h3>
        <button
          type="button"
          disabled={busy || routes.length >= 128 || depth >= 8}
          onClick={() => onChange([...routes, newRoute(points)])}
        >
          <Plus size={15} />
          {depth
            ? text("Add child route", "添加子路由")
            : text("Add route", "添加路由")}
        </button>
      </div>
      {routes.map((route, index) => (
        <div
          className="notification-policy-route"
          key={index}
          data-route-depth={depth}
        >
          <div className="form-row">
            <label>
              {text("Contact point", "联系点")}
              <select
                aria-label={text("Contact point", "联系点")}
                value={route.receiver || ""}
                disabled={busy}
                onChange={(e) =>
                  patch(index, { ...route, receiver: e.target.value })
                }
              >
                <option value="">
                  {text("Inherit parent contact point", "继承父级联系点")}
                </option>
                {points.map((p) => (
                  <option key={p.uid} value={p.uid}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={!!route.continue}
                disabled={busy}
                onChange={(e) =>
                  patch(index, { ...route, continue: e.target.checked })
                }
              />
              {text("Continue matching later routes", "继续匹配后续路由")}
            </label>
          </div>
          {(route.matchers || []).map((m, mi) => (
            <div className="form-row" key={mi}>
              <label>
                {text("Label", "标签名")}
                <input
                  aria-label={text("Label", "标签名")}
                  required
                  maxLength={128}
                  value={m.name}
                  disabled={busy}
                  onChange={(e) =>
                    patch(index, {
                      ...route,
                      matchers: (route.matchers || []).map((v, n) =>
                        n === mi ? { ...v, name: e.target.value } : v,
                      ),
                    })
                  }
                />
              </label>
              <label>
                {text("Operator", "运算符")}
                <select
                  aria-label={text("Operator", "运算符")}
                  value={m.operator}
                  disabled={busy}
                  onChange={(e) =>
                    patch(index, {
                      ...route,
                      matchers: (route.matchers || []).map((v, n) =>
                        n === mi
                          ? {
                              ...v,
                              operator: e.target
                                .value as NotificationMatcher["operator"],
                            }
                          : v,
                      ),
                    })
                  }
                >
                  {["=", "!=", "=~", "!~"].map((op) => (
                    <option key={op}>{op}</option>
                  ))}
                </select>
              </label>
              <label>
                {text("Value", "标签值")}
                <input
                  aria-label={text("Value", "标签值")}
                  maxLength={4096}
                  value={m.value}
                  disabled={busy}
                  onChange={(e) =>
                    patch(index, {
                      ...route,
                      matchers: (route.matchers || []).map((v, n) =>
                        n === mi ? { ...v, value: e.target.value } : v,
                      ),
                    })
                  }
                />
              </label>
              <button
                type="button"
                className="icon-button"
                aria-label={text("Remove matcher", "删除匹配条件")}
                disabled={busy}
                onClick={() =>
                  patch(index, {
                    ...route,
                    matchers: (route.matchers || []).filter((_, n) => n !== mi),
                  })
                }
              >
                <X size={16} />
              </button>
            </div>
          ))}
          <details className="notification-route-details">
            <summary>
              {text("Grouping and time intervals", "分组与时间段")}
            </summary>
            <NotificationTimingFields
              value={route}
              inherited={inherited}
              busy={busy}
              onChange={(v) =>
                patch(index, {
                  ...route,
                  ...v,
                  group_by: v.group_by,
                  group_wait: v.group_wait,
                  group_interval: v.group_interval,
                  repeat_interval: v.repeat_interval,
                })
              }
            />
            {(
              [
                ["mute_time_intervals", "Mute time intervals", "静默时间段"],
                [
                  "active_time_intervals",
                  "Active time intervals",
                  "有效时间段",
                ],
              ] as const
            ).map(([field, en, zh]) => (
              <label className="notification-interval-select" key={field}>
                {text(en, zh)}
                <select
                  multiple
                  aria-label={text(en, zh)}
                  value={route[field] || []}
                  disabled={busy}
                  onChange={(e) =>
                    patch(index, {
                      ...route,
                      [field]: Array.from(
                        e.target.selectedOptions,
                        (o) => o.value,
                      ),
                    })
                  }
                >
                  {timings.map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
                <small>
                  {text(
                    "Local to this route. Hold Ctrl to select multiple.",
                    "仅作用于本路由。按住 Ctrl 可选择多个。",
                  )}
                </small>
              </label>
            ))}
          </details>
          <div className="notification-route-actions">
            <button
              type="button"
              disabled={busy || (route.matchers?.length || 0) >= 32}
              onClick={() =>
                patch(index, {
                  ...route,
                  matchers: [
                    ...(route.matchers || []),
                    { name: "", operator: "=", value: "" },
                  ],
                })
              }
            >
              <Plus size={14} />
              {text("Add matcher", "添加匹配条件")}
            </button>
            <button
              type="button"
              disabled={busy || index === 0}
              aria-label={text("Move route up", "上移路由")}
              onClick={() => move(index, -1)}
            >
              <ArrowUp size={14} />
            </button>
            <button
              type="button"
              disabled={busy || index === routes.length - 1}
              aria-label={text("Move route down", "下移路由")}
              onClick={() => move(index, 1)}
            >
              <ArrowDown size={14} />
            </button>
            <button
              type="button"
              disabled={busy}
              className="danger-hover"
              onClick={() => onChange(routes.filter((_, n) => n !== index))}
            >
              <Trash2 size={14} />
              {text("Remove route", "删除路由")}
            </button>
          </div>
          <NotificationRoutingEditor
            routes={route.routes || []}
            points={points}
            timings={timings}
            inherited={{
              ...inherited,
              ...Object.fromEntries(
                Object.entries(route).filter(
                  ([k, v]) =>
                    [
                      "group_by",
                      "group_wait",
                      "group_interval",
                      "repeat_interval",
                    ].includes(k) &&
                    v !== undefined &&
                    v !== "",
                ),
              ),
            }}
            busy={busy}
            depth={depth + 1}
            onChange={(children) =>
              patch(index, {
                ...route,
                routes: children.length ? children : undefined,
              })
            }
          />
        </div>
      ))}
    </div>
  );
}
