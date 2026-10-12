import { useEffect, useRef, useState } from "react";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { api, jsonBody, message, type Dashboard, type Variable } from "../api";
import { getLocale } from "../i18n";
import { Dialog } from "./Dialog";
import { resolveVariables } from "../grafana/variables";
import {
  editableVariableTypes,
  variableDefault,
  parseCustomChoices,
  validateVariables,
  type VariableChoice,
} from "../grafana/variable-model";
import type { TimeSelection } from "../grafana/time-range";
import type { VariableValues } from "../grafana/engine";
import "./dashboard-variables.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
const hideModes = ["dontHide", "hideLabel", "hideVariable", "inControlsMenu"];
const refreshModes = ["never", "onDashboardLoad", "onTimeRangeChanged"];
const sortModes = [
  "disabled",
  "alphabeticalAsc",
  "alphabeticalDesc",
  "numericalAsc",
  "numericalDesc",
  "alphabeticalCaseInsensitiveAsc",
  "alphabeticalCaseInsensitiveDesc",
  "naturalAsc",
  "naturalDesc",
];
function enumNumber(value: unknown, modes: string[], fallback: number) {
  return typeof value === "number"
    ? value
    : modes.includes(String(value))
      ? modes.indexOf(String(value))
      : fallback;
}
type Draft = { originalName?: string; baseline?: Variable; variable: Variable };
type Source = { uid: string; name: string; type: string };
export function DashboardVariablesEditor({
  dashboard,
  range,
  values,
  onClose,
  reload,
}: {
  dashboard: Dashboard;
  range: TimeSelection;
  values: VariableValues;
  onClose: () => void;
  reload: () => Promise<void>;
}) {
  const [baseline] = useState(() => structuredClone(dashboard));
  const [drafts, setDrafts] = useState<Draft[]>(() =>
    (baseline.variables || []).map((variable) => ({
      originalName: variable.name,
      baseline: structuredClone(variable),
      variable: structuredClone(variable),
    })),
  );
  const [selected, setSelected] = useState(0),
    [sources, setSources] = useState<Source[]>([]);
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [previewBusy, setPreviewBusy] = useState(false);
  const [preview, setPreview] = useState<{
    choices: VariableChoice[];
    value: string | string[];
  } | null>(null);
  const [rawQuery, setRawQuery] = useState(""),
    [rawError, setRawError] = useState("");
  const controller = useRef<AbortController | null>(null);
  const variable = drafts[selected]?.variable;
  useEffect(() => {
    const abort = new AbortController();
    void api<Source[]>("/api/datasources", { signal: abort.signal })
      .then(setSources)
      .catch((reason) => {
        if (!abort.signal.aborted) setError(message(reason));
      });
    return () => {
      abort.abort();
      controller.current?.abort();
    };
  }, []);
  function stopPreview() {
    controller.current?.abort();
    controller.current = null;
    setPreviewBusy(false);
    setPreview(null);
  }
  function change(next: Variable) {
    stopPreview();
    setError("");
    setDrafts((current) =>
      current.map((draft, index) =>
        index === selected ? { ...draft, variable: next } : draft,
      ),
    );
  }
  function patch(
    update: Partial<Variable>,
    config: Record<string, unknown> = {},
  ) {
    if (!variable) return;
    change({
      ...variable,
      ...update,
      config: { ...variable.config, ...config },
    });
  }
  function pick(index: number) {
    stopPreview();
    setSelected(index);
    setRawQuery("");
    setRawError("");
    setError("");
  }
  function setQuery(query: string) {
    if (!variable) return;
    const current = variable.config?.query;
    const model =
      variable.type === "query" &&
      current &&
      typeof current === "object" &&
      !Array.isArray(current)
        ? {
            ...(current as Record<string, unknown>),
            [Object.hasOwn(current, "rawSql") ? "rawSql" : "query"]: query,
          }
        : query;
    const literal = ["constant", "textbox"].includes(variable.type);
    const choices = ["custom", "interval"].includes(variable.type)
      ? parseCustomChoices(query)
      : null;
    patch(
      {
        query,
        ...(literal ? { current: query } : {}),
        ...(choices ? { options: choices.map((choice) => choice.value) } : {}),
      },
      {
        query: model,
        ...(choices
          ? {
              options: choices.map((choice) => ({
                ...variable.config?.options?.find(
                  (option) => option.value === choice.value,
                ),
                ...choice,
              })),
            }
          : {}),
        ...(literal
          ? {
              current: {
                ...variable.config?.current,
                value: query,
                text: query,
              },
            }
          : {}),
      },
    );
    setRawQuery("");
    setRawError("");
  }
  function setDefault(value: string) {
    if (!variable) return;
    const selection = variable.multi
      ? value.split("\n").filter((value) => value !== "")
      : value;
    const options = preview?.choices || variable.config?.options || [];
    const label = (value: string) =>
      options.find((option) => String(option.value) === value)?.text || value;
    patch(
      { current: Array.isArray(selection) ? selection.join("|") : selection },
      {
        current: {
          ...variable.config?.current,
          value: selection,
          text: Array.isArray(selection)
            ? selection.map(label)
            : label(selection),
        },
      },
    );
  }
  async function runPreview() {
    stopPreview();
    setError("");
    const abort = new AbortController();
    controller.current = abort;
    setPreviewBusy(true);
    try {
      const variables = drafts.map((draft) => draft.variable);
      validateVariables(variables);
      const defaults = {
        ...values,
        ...Object.fromEntries(
          variables.map((variable) => [
            variable.name,
            variableDefault(variable),
          ]),
        ),
      };
      const result = await resolveVariables(
        variables,
        defaults,
        range,
        abort.signal,
      );
      if (!abort.signal.aborted && variable)
        setPreview({
          choices: result.choices[variable.name] || [],
          value: result.values[variable.name],
        });
    } catch (reason) {
      if (!abort.signal.aborted) setError(message(reason));
    } finally {
      if (!abort.signal.aborted) setPreviewBusy(false);
    }
  }
  const labels: Record<string, string> = {
    query: text("Query", "查询"),
    custom: text("Custom", "自定义"),
    constant: text("Constant", "常量"),
    textbox: text("Text box", "文本输入"),
    datasource: text("Datasource", "数据源"),
    interval: text("Interval", "时间间隔"),
  };
  const defaults = variable ? variableDefault(variable) : "";
  const sourceRef = variable?.config?.datasource;
  const uid = typeof sourceRef === "string" ? sourceRef : sourceRef?.uid || "";
  const displayedQuery =
    variable?.config?.query && typeof variable.config.query === "object"
      ? String(
          (variable.config.query as Record<string, unknown>).rawSql ??
            (variable.config.query as Record<string, unknown>).query ??
            variable.query,
        )
      : variable?.query || "";
  return (
    <Dialog
      title={text("Dashboard variables", "仪表盘变量")}
      style={{ width: 1100, maxWidth: "calc(100vw - 80px)" }}
      onClose={() => {
        if (!busy) {
          controller.current?.abort();
          onClose();
        }
      }}
    >
      <div className="variable-editor-body">
        <div className="variable-editor-layout">
          <aside className="variable-editor-sidebar">
            <button
              disabled={busy || drafts.length >= 128}
              onClick={() => {
                let index = 1;
                while (
                  drafts.some(
                    (draft) => draft.variable.name === `variable${index}`,
                  )
                )
                  index++;
                const variable: Variable = {
                  name: `variable${index}`,
                  type: "custom",
                  query: "east,west",
                  current: "east",
                  options: [],
                  multi: false,
                  include_all: false,
                  config: {
                    name: `variable${index}`,
                    type: "custom",
                    query: "east,west",
                    current: { value: "east", text: "east" },
                  },
                };
                setDrafts((current) => [...current, { variable }]);
                pick(drafts.length);
              }}
            >
              <Plus size={15} />
              {text("Add variable", "添加变量")}
            </button>
            <div className="variable-editor-list">
              {drafts.map((draft, index) => (
                <div
                  className="variable-editor-row"
                  key={draft.originalName || `new-${index}`}
                >
                  <button
                    aria-current={index === selected ? "true" : undefined}
                    onClick={() => pick(index)}
                  >
                    {draft.variable.name}
                    <small>
                      {" "}
                      · {labels[draft.variable.type] || draft.variable.type}
                    </small>
                  </button>
                  <button
                    className="icon-button"
                    aria-label={`${text("Move variable up", "上移变量")} ${draft.variable.name}`}
                    disabled={busy || index === 0}
                    onClick={() => {
                      const next = [...drafts];
                      [next[index - 1], next[index]] = [
                        next[index],
                        next[index - 1],
                      ];
                      setDrafts(next);
                      pick(index - 1);
                    }}
                  >
                    <ArrowUp size={14} />
                  </button>
                  <button
                    className="icon-button"
                    aria-label={`${text("Move variable down", "下移变量")} ${draft.variable.name}`}
                    disabled={busy || index === drafts.length - 1}
                    onClick={() => {
                      const next = [...drafts];
                      [next[index + 1], next[index]] = [
                        next[index],
                        next[index + 1],
                      ];
                      setDrafts(next);
                      pick(index + 1);
                    }}
                  >
                    <ArrowDown size={14} />
                  </button>
                </div>
              ))}
            </div>
          </aside>
          {variable ? (
            <div className="variable-editor-form">
              <div className="variable-editor-fields">
                <label>
                  {text("Name", "名称")}
                  <input
                    aria-label={text("Variable name", "变量名称")}
                    value={variable.name}
                    maxLength={100}
                    onChange={(event) =>
                      patch(
                        { name: event.target.value },
                        { name: event.target.value },
                      )
                    }
                  />
                </label>
                <label>
                  {text("Type", "类型")}
                  <select
                    aria-label={text("Variable type", "变量类型")}
                    value={variable.type}
                    onChange={(event) =>
                      patch(
                        { type: event.target.value },
                        { type: event.target.value },
                      )
                    }
                  >
                    {editableVariableTypes.map((type) => (
                      <option value={type} key={type}>
                        {labels[type]}
                      </option>
                    ))}
                    {!editableVariableTypes.includes(variable.type) && (
                      <option value={variable.type}>{variable.type}</option>
                    )}
                  </select>
                </label>
                <label>
                  {text("Display label", "显示名称")}
                  <input
                    aria-label={text("Variable label", "变量显示名称")}
                    value={variable.config?.label || ""}
                    maxLength={200}
                    onChange={(event) =>
                      patch({}, { label: event.target.value })
                    }
                  />
                </label>
                <label>
                  {text("Visibility", "显示方式")}
                  <select
                    aria-label={text("Variable visibility", "变量显示方式")}
                    value={String(
                      enumNumber(variable.config?.hide, hideModes, 0),
                    )}
                    onChange={(event) =>
                      patch({}, { hide: Number(event.target.value) })
                    }
                  >
                    <option value="0">
                      {text("Show label and value", "显示名称和值")}
                    </option>
                    <option value="1">{text("Hide label", "隐藏名称")}</option>
                    <option value="2">{text("Hidden", "隐藏")}</option>
                    <option value="3">
                      {text("Controls menu", "控制菜单")}
                    </option>
                    {["hideLabel", "hideVariable", "dontHide"].includes(
                      String(variable.config?.hide),
                    ) && (
                      <option value={String(variable.config?.hide)}>
                        {String(variable.config?.hide)}
                      </option>
                    )}
                  </select>
                </label>
              </div>
              {variable.type === "query" && (
                <label>
                  {text("Datasource", "数据源")}
                  <select
                    aria-label={text("Variable datasource", "变量数据源")}
                    value={uid}
                    onChange={(event) => {
                      const source = sources.find(
                        (source) => source.uid === event.target.value,
                      );
                      patch(
                        {},
                        {
                          datasource: {
                            ...(typeof sourceRef === "object" ? sourceRef : {}),
                            uid: event.target.value,
                            type: source?.type,
                          },
                        },
                      );
                    }}
                  >
                    <option value="">
                      {text("Local Prometheus", "本地 Prometheus")}
                    </option>
                    {sources.map((source) => (
                      <option value={source.uid} key={source.uid}>
                        {source.name}
                      </option>
                    ))}
                    {drafts
                      .filter((draft) => draft.variable.type === "datasource")
                      .map((draft) => (
                        <option
                          value={`$${draft.variable.name}`}
                          key={`$${draft.variable.name}`}
                        >
                          ${draft.variable.name}
                        </option>
                      ))}
                    {uid &&
                      !sources.some((source) => source.uid === uid) &&
                      !drafts.some(
                        (draft) => `$${draft.variable.name}` === uid,
                      ) && <option value={uid}>{uid}</option>}
                  </select>
                </label>
              )}
              <label>
                {variable.type === "datasource"
                  ? text("Datasource plugin type", "数据源插件类型")
                  : variable.type === "constant"
                    ? text("Constant value", "常量值")
                    : variable.type === "textbox"
                      ? text("Default text", "默认文本")
                      : variable.type === "query"
                        ? text("Variable query", "变量查询")
                        : text("Values (comma separated)", "值（用逗号分隔）")}
                <textarea
                  aria-label={text("Variable query", "变量查询")}
                  className="mono"
                  rows={variable.type === "query" ? 5 : 2}
                  maxLength={131072}
                  value={displayedQuery}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>
              <p className="variable-editor-note">
                {variable.type === "query"
                  ? text(
                      "Prometheus supports label_values(metric, label), label_names(), metrics(), and query_result(). SQL can return __text and __value columns. References such as $region update dependent variables.",
                      "Prometheus 支持 label_values(metric, label)、label_names()、metrics() 和 query_result()。SQL 可返回 __text、__value 列。$region 等引用会更新依赖变量。",
                    )
                  : variable.type === "custom"
                    ? text(
                        "Use East : east, West : west to give values display labels. Escape a comma as \\,.",
                        "可用 East : east, West : west 设置显示名称。值中的逗号可写为 \\,。",
                      )
                    : variable.type === "datasource"
                      ? text(
                          "Use the plugin ID, for example prometheus, mysql or grafana-postgresql-datasource. The selector displays datasource names and queries use their UIDs.",
                          "填写插件 ID，例如 prometheus、mysql 或 grafana-postgresql-datasource。选择栏显示数据源名称，查询使用 UID。",
                        )
                      : ""}
              </p>
              <div className="variable-editor-fields">
                <label>
                  {text("Default selection", "默认选择")}
                  <textarea
                    aria-label={text(
                      "Variable default selection",
                      "变量默认选择",
                    )}
                    rows={variable.multi ? 3 : 2}
                    value={
                      Array.isArray(defaults) ? defaults.join("\n") : defaults
                    }
                    readOnly={["constant", "textbox"].includes(variable.type)}
                    onChange={(event) => setDefault(event.target.value)}
                  />
                  <small>
                    {variable.multi
                      ? text(
                          "One value per line. $__all selects All.",
                          "每行一个值。$__all 表示全部。",
                        )
                      : text(
                          "Use an option value, not its display label.",
                          "填写选项值。",
                        )}
                  </small>
                </label>
                <label>
                  {text("Regex", "正则表达式")}
                  <input
                    aria-label={text("Variable regex", "变量正则表达式")}
                    className="mono"
                    value={variable.config?.regex || ""}
                    maxLength={4096}
                    onChange={(event) =>
                      patch({}, { regex: event.target.value })
                    }
                  />
                </label>
                <label>
                  {text("Sort", "排序")}
                  <select
                    aria-label={text("Variable sort", "变量排序")}
                    value={String(
                      enumNumber(variable.config?.sort, sortModes, 0),
                    )}
                    onChange={(event) =>
                      patch({}, { sort: Number(event.target.value) })
                    }
                  >
                    <option value="0">
                      {text("Returned order", "返回顺序")}
                    </option>
                    <option value="1">
                      {text("Alphabetical A–Z", "名称 A–Z")}
                    </option>
                    <option value="2">
                      {text("Alphabetical Z–A", "名称 Z–A")}
                    </option>
                    <option value="3">
                      {text("Numeric ascending", "数值升序")}
                    </option>
                    <option value="4">
                      {text("Numeric descending", "数值降序")}
                    </option>
                    <option value="5">
                      {text(
                        "Alphabetical A–Z (ignore case)",
                        "名称 A–Z（忽略大小写）",
                      )}
                    </option>
                    <option value="6">
                      {text(
                        "Alphabetical Z–A (ignore case)",
                        "名称 Z–A（忽略大小写）",
                      )}
                    </option>
                    <option value="7">
                      {text("Natural ascending", "自然升序")}
                    </option>
                    <option value="8">
                      {text("Natural descending", "自然降序")}
                    </option>
                    {variable.config?.sort &&
                      typeof variable.config.sort === "string" && (
                        <option value={variable.config.sort}>
                          {variable.config.sort}
                        </option>
                      )}
                  </select>
                </label>
                <label>
                  {text("Refresh", "刷新")}
                  <select
                    aria-label={text("Variable refresh", "变量刷新")}
                    value={String(
                      enumNumber(variable.config?.refresh, refreshModes, 1),
                    )}
                    onChange={(event) =>
                      patch({}, { refresh: Number(event.target.value) })
                    }
                  >
                    <option value="0">
                      {text("Use saved options", "使用保存的选项")}
                    </option>
                    <option value="1">
                      {text("On dashboard load", "加载仪表盘时")}
                    </option>
                    <option value="2">
                      {text("On time range change", "时间范围变化时")}
                    </option>
                  </select>
                </label>
              </div>
              <div className="variable-editor-options">
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={variable.multi}
                    disabled={["constant", "textbox"].includes(variable.type)}
                    onChange={(event) => {
                      const value = variableDefault({
                        ...variable,
                        multi: event.target.checked,
                      });
                      patch(
                        {
                          multi: event.target.checked,
                          current: Array.isArray(value)
                            ? value.join("|")
                            : value,
                        },
                        {
                          multi: event.target.checked,
                          current: { ...variable.config?.current, value },
                        },
                      );
                    }}
                  />
                  {text("Multi-value", "多选")}
                </label>
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={variable.include_all}
                    disabled={["constant", "textbox"].includes(variable.type)}
                    onChange={(event) =>
                      patch(
                        { include_all: event.target.checked },
                        { includeAll: event.target.checked },
                      )
                    }
                  />
                  {text("Include All", "包含全部")}
                </label>
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={variable.config?.skipUrlSync === true}
                    onChange={(event) =>
                      patch({}, { skipUrlSync: event.target.checked })
                    }
                  />
                  {text("Keep value out of URL", "不写入 URL")}
                </label>
              </div>
              {variable.include_all && (
                <label>
                  {text("Custom All value", "全部选项的自定义值")}
                  <input
                    aria-label={text("Variable All value", "变量全部值")}
                    className="mono"
                    value={variable.config?.allValue || ""}
                    onChange={(event) =>
                      patch({}, { allValue: event.target.value })
                    }
                  />
                  <small>
                    {text(
                      "Leave empty to include every option value.",
                      "留空时使用全部选项值。",
                    )}
                  </small>
                </label>
              )}
              {variable.type === "interval" && (
                <>
                  <label className="checkbox-label">
                    <input
                      type="checkbox"
                      checked={variable.config?.auto === true}
                      onChange={(event) =>
                        patch({}, { auto: event.target.checked })
                      }
                    />
                    {text("Auto interval", "自动间隔")}
                  </label>
                  {variable.config?.auto && (
                    <div className="variable-editor-fields">
                      <label>
                        {text("Target intervals", "目标间隔数")}
                        <input
                          type="number"
                          min={1}
                          max={1000}
                          value={variable.config.auto_count || 30}
                          onChange={(event) =>
                            patch(
                              {},
                              { auto_count: Number(event.target.value) },
                            )
                          }
                        />
                      </label>
                      <label>
                        {text("Minimum interval", "最小间隔")}
                        <input
                          value={variable.config.auto_min || "10s"}
                          onChange={(event) =>
                            patch({}, { auto_min: event.target.value })
                          }
                        />
                      </label>
                    </div>
                  )}
                </>
              )}
              {variable.type === "query" && (
                <details>
                  <summary>
                    {text("Query model (JSON)", "查询模型（JSON）")}
                  </summary>
                  <textarea
                    aria-label={text("Variable query model", "变量查询模型")}
                    rows={7}
                    className="mono"
                    value={
                      rawQuery ||
                      JSON.stringify(
                        variable.config?.query ?? variable.query,
                        null,
                        2,
                      )
                    }
                    onChange={(event) => {
                      setRawQuery(event.target.value);
                      stopPreview();
                      try {
                        const query = JSON.parse(event.target.value);
                        if (
                          typeof query !== "string" &&
                          (!query ||
                            typeof query !== "object" ||
                            Array.isArray(query))
                        )
                          throw new Error(
                            text(
                              "Query model must be a string or object",
                              "查询模型必须是字符串或对象",
                            ),
                          );
                        patch(
                          {
                            query:
                              typeof query === "string"
                                ? query
                                : String(query.rawSql ?? query.query ?? ""),
                          },
                          { query },
                        );
                        setRawError("");
                      } catch (reason) {
                        setRawError(message(reason));
                      }
                    }}
                  />
                  {rawError && (
                    <p role="alert" className="form-error">
                      {rawError}
                    </p>
                  )}
                </details>
              )}
              <div className="variable-preview">
                <div className="variable-preview-controls">
                  <button
                    disabled={previewBusy || !!rawError || busy}
                    onClick={() => void runPreview()}
                  >
                    {text(
                      previewBusy ? "Loading preview…" : "Preview values",
                      previewBusy ? "正在预览…" : "预览值",
                    )}
                  </button>
                  {previewBusy && (
                    <button onClick={stopPreview}>
                      {text("Cancel preview", "取消预览")}
                    </button>
                  )}
                  <button
                    aria-label={`${text("Delete variable", "删除变量")} ${variable.name}`}
                    className="danger"
                    disabled={busy}
                    onClick={() => {
                      setDrafts((current) =>
                        current.filter((_, index) => index !== selected),
                      );
                      pick(Math.max(0, selected - 1));
                    }}
                  >
                    <Trash2 size={15} />
                    {text("Delete variable", "删除变量")}
                  </button>
                </div>
                {preview && (
                  <>
                    <p>
                      {text("Selected value", "当前值")}:{" "}
                      <code>{JSON.stringify(preview.value)}</code>
                    </p>
                    <table>
                      <thead>
                        <tr>
                          <th>{text("Display text", "显示文本")}</th>
                          <th>{text("Value", "值")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {preview.choices.slice(0, 100).map((choice) => (
                          <tr key={choice.value}>
                            <td>{choice.text}</td>
                            <td className="mono">{choice.value}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    {preview.choices.length > 100 && (
                      <p>
                        {preview.choices.length}{" "}
                        {text(
                          "values; showing the first 100",
                          "个值，显示前 100 个",
                        )}
                      </p>
                    )}
                  </>
                )}
              </div>
            </div>
          ) : (
            <p className="subtle">
              {text(
                "No variables. Add a variable to reuse values in panel queries.",
                "暂无变量。添加变量后可在面板查询中复用值。",
              )}
            </p>
          )}
        </div>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {error && /changed|reload/i.test(error) && (
        <button
          disabled={busy}
          onClick={async () => {
            controller.current?.abort();
            await reload();
            onClose();
          }}
        >
          {text("Reload and discard edits", "重新加载并放弃编辑")}
        </button>
      )}
      <div className="variable-editor-footer">
        <button
          disabled={busy}
          onClick={() => {
            controller.current?.abort();
            onClose();
          }}
        >
          {text("Cancel", "取消")}
        </button>
        <button
          className="primary"
          disabled={busy || !!rawError}
          onClick={async () => {
            stopPreview();
            setError("");
            setBusy(true);
            try {
              validateVariables(drafts.map((draft) => draft.variable));
              await api(
                `/dashboards/${encodeURIComponent(baseline.id)}/variables`,
                {
                  method: "PATCH",
                  headers: { "If-Match": String(baseline.updated_at) },
                  body: jsonBody({ variables: drafts }),
                },
              );
              await reload();
              onClose();
            } catch (reason) {
              setError(message(reason));
            } finally {
              setBusy(false);
            }
          }}
        >
          {text(
            busy ? "Saving…" : "Save variables",
            busy ? "正在保存…" : "保存变量",
          )}
        </button>
      </div>
    </Dialog>
  );
}
