import type {
  QueryEditorProps,
  DataSourceJsonData,
  DataSourceApi,
} from "@grafana/data";
import type { ExpressionQuery } from "./expression-source";
import { t } from "../i18n";

const operations = [
  "math",
  "reduce",
  "resample",
  "threshold",
  "classic_conditions",
  "sql",
];
const reducers = ["sum", "mean", "min", "max", "count", "last", "median"];
const classicReducers = [
  "avg",
  "sum",
  "min",
  "max",
  "count",
  "last",
  "median",
  "diff",
  "diff_abs",
  "percent_diff",
  "percent_diff_abs",
  "count_non_null",
];
const comparisons = [
  "gt",
  "lt",
  "eq",
  "ne",
  "gte",
  "lte",
  "within_range",
  "outside_range",
  "within_range_included",
  "outside_range_included",
];
const names: Record<string, string> = {
  math: "Math",
  reduce: "Reduce",
  resample: "Resample",
  threshold: "Threshold",
  classic_conditions: "Classic conditions",
  sql: "SQL",
  gt: "Is above",
  lt: "Is below",
  eq: "Is equal to",
  ne: "Is not equal to",
  gte: "Is above or equal to",
  lte: "Is below or equal to",
  within_range: "Is within range",
  outside_range: "Is outside range",
  within_range_included: "Is within inclusive range",
  outside_range_included: "Is outside inclusive range",
  no_value: "Has no value",
  strict: "Strict",
  dropNN: "Drop non-numeric",
  replaceNN: "Replace non-numeric",
  pad: "Previous value",
  backfilling: "Next value",
  fillna: "NaN",
  and: "AND",
  or: "OR",
  "logic-or": "Short-circuit OR",
};
type Condition = NonNullable<ExpressionQuery["conditions"]>[number];
type Evaluator = NonNullable<Condition["evaluator"]>;

function Choice({
  label,
  value,
  values,
  change,
}: {
  label: string;
  value: string;
  values: string[];
  change: (value: string) => void;
}) {
  const choices =
    values.includes(value) || !value ? values : [...values, value];
  return (
    <label>
      {t(label)}
      <select
        aria-label={t(label)}
        value={value}
        onChange={(event) => change(event.target.value)}
      >
        {choices.map((item) => (
          <option key={item} value={item}>
            {t(names[item] || item)}
          </option>
        ))}
      </select>
    </label>
  );
}

function EvaluatorFields({
  value,
  onChange,
  label,
  classic = false,
}: {
  value: Evaluator;
  onChange: (value: Evaluator) => void;
  label: string;
  classic?: boolean;
}) {
  const count =
    value.type === "no_value" ? 0 : value.type?.includes("range") ? 2 : 1;
  return (
    <div className="expression-evaluator">
      <Choice
        label={label}
        value={value.type || "gt"}
        values={classic ? [...comparisons, "no_value"] : comparisons}
        change={(type) =>
          onChange({
            ...value,
            type,
            params: Array.from(
              {
                length:
                  type === "no_value" ? 0 : type.includes("range") ? 2 : 1,
              },
              (_, i) => value.params?.[i] ?? 0,
            ),
          })
        }
      />
      {Array.from({ length: count }, (_, index) => (
        <label key={index}>
          {t(index === 0 ? "Value" : "Upper value")}
          <input
            type="number"
            step="any"
            aria-label={
              t(label) + " " + t(index === 0 ? "Value" : "Upper value")
            }
            value={value.params?.[index] ?? 0}
            onChange={(event) => {
              const n = Number(event.target.value);
              if (!Number.isFinite(n)) return;
              const params = [...(value.params || [])];
              params[index] = n;
              onChange({ ...value, params });
            }}
          />
        </label>
      ))}
    </div>
  );
}

export function ExpressionQueryEditor({
  query,
  queries = [],
  onChange,
  onRunQuery,
}: QueryEditorProps<
  DataSourceApi<ExpressionQuery>,
  ExpressionQuery,
  DataSourceJsonData
>) {
  const type = query.type || "math";
  const patch = (update: Partial<ExpressionQuery>) =>
    onChange({ ...query, ...update });
  const refs = queries
    .filter((item) => item.refId !== query.refId)
    .map((item) => item.refId);
  const input = (
    label: string,
    value: string,
    change: (value: string) => void,
  ) => (
    <label>
      {t(label)}
      <input
        aria-label={t(label)}
        value={value}
        list={`expression-inputs-${query.refId}`}
        onChange={(event) => change(event.target.value)}
      />
    </label>
  );
  const condition = query.conditions?.[0] || {
    evaluator: { type: "gt", params: [0] },
  };
  const updateCondition = (update: Partial<Condition>) =>
    patch({
      conditions: [
        { ...condition, ...update },
        ...(query.conditions || []).slice(1),
      ],
    });
  return (
    <div
      className="expression-query-editor"
      onKeyDown={(event) => {
        if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
          event.preventDefault();
          onRunQuery();
        }
      }}
    >
      <datalist id={`expression-inputs-${query.refId}`}>
        {refs.map((ref) => (
          <option key={ref} value={ref} />
        ))}
      </datalist>
      <Choice
        label="Operation"
        value={type}
        values={operations}
        change={(next) => {
          const ref = refs[0] || "A";
          const common = { ...query, type: next };
          switch (next) {
            case "math":
              onChange({ ...common, expression: "$" + ref });
              break;
            case "reduce":
              onChange({
                ...common,
                expression: ref,
                reducer: "mean",
                settings: { mode: "strict" },
              });
              break;
            case "resample":
              onChange({
                ...common,
                expression: ref,
                window: "10s",
                downsampler: "mean",
                upsampler: "pad",
              });
              break;
            case "threshold":
              onChange({
                ...common,
                expression: ref,
                invert: false,
                conditions: [{ evaluator: { type: "gt", params: [0] } }],
              });
              break;
            case "classic_conditions":
              onChange({
                ...common,
                conditions: [
                  {
                    query: { params: [ref] },
                    reducer: { type: "avg" },
                    evaluator: { type: "gt", params: [0] },
                    operator: { type: "and" },
                  },
                ],
              });
              break;
            case "sql":
              onChange({
                ...common,
                expression: "SELECT * FROM `" + ref.replaceAll("`", "``") + "`",
                format: "table",
              });
              break;
          }
        }}
      />
      {type === "math" || type === "sql" ? (
        <label>
          {t(type === "sql" ? "SQL query" : "Math expression")}
          <textarea
            className="mono"
            rows={type === "sql" ? 6 : 3}
            aria-label={t(type === "sql" ? "SQL query" : "Math expression")}
            value={query.expression || ""}
            onChange={(event) => patch({ expression: event.target.value })}
          />
        </label>
      ) : ["reduce", "resample", "threshold"].includes(type) ? (
        input("Input query", query.expression || "", (expression) =>
          patch({ expression }),
        )
      ) : null}
      {type === "math" ? (
        <p className="subtle">
          {t(
            "Reference queries with $A or ${query name}. Ctrl+Enter runs the query.",
          )}
        </p>
      ) : null}
      {type === "reduce" ? (
        <>
          <div className="form-row">
            <Choice
              label="Function"
              value={query.reducer || "mean"}
              values={reducers}
              change={(reducer) => patch({ reducer })}
            />
            <Choice
              label="Mode"
              value={query.settings?.mode || "strict"}
              values={["strict", "dropNN", "replaceNN"]}
              change={(mode) =>
                patch({
                  settings: {
                    ...query.settings,
                    mode,
                    ...(mode === "replaceNN"
                      ? {
                          replaceWithValue:
                            query.settings?.replaceWithValue ?? 0,
                        }
                      : {}),
                  },
                })
              }
            />
          </div>
          {query.settings?.mode === "replaceNN" ? (
            <label>
              {t("Replacement value")}
              <input
                type="number"
                step="any"
                aria-label={t("Replacement value")}
                value={query.settings.replaceWithValue ?? 0}
                onChange={(event) => {
                  const value = Number(event.target.value);
                  if (Number.isFinite(value))
                    patch({
                      settings: { ...query.settings, replaceWithValue: value },
                    });
                }}
              />
            </label>
          ) : null}
        </>
      ) : null}
      {type === "resample" ? (
        <>
          {input("Resample interval", query.window || "", (window) =>
            patch({ window }),
          )}
          <div className="form-row">
            <Choice
              label="Downsample"
              value={query.downsampler || "mean"}
              values={["sum", "mean", "min", "max", "last"]}
              change={(downsampler) => patch({ downsampler })}
            />
            <Choice
              label="Upsample"
              value={query.upsampler || "pad"}
              values={["pad", "backfilling", "fillna"]}
              change={(upsampler) => patch({ upsampler })}
            />
          </div>
        </>
      ) : null}
      {type === "threshold" ? (
        <>
          <EvaluatorFields
            label="Threshold comparison"
            value={condition.evaluator || { type: "gt", params: [0] }}
            onChange={(evaluator) => updateCondition({ evaluator })}
          />
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={Boolean(query.invert)}
              onChange={(event) => patch({ invert: event.target.checked })}
            />
            {t("Invert result")}
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={Boolean(condition.unloadEvaluator)}
              onChange={(event) =>
                updateCondition({
                  unloadEvaluator: event.target.checked
                    ? { type: "lt", params: [0] }
                    : undefined,
                })
              }
            />
            {t("Custom recovery threshold")}
          </label>
          {condition.unloadEvaluator ? (
            <EvaluatorFields
              label="Recovery comparison"
              value={condition.unloadEvaluator}
              onChange={(unloadEvaluator) =>
                updateCondition({ unloadEvaluator })
              }
            />
          ) : null}
        </>
      ) : null}
      {type === "classic_conditions" ? (
        <>
          {(query.conditions || []).map((item, index) => {
            const change = (update: Partial<Condition>) =>
              patch({
                conditions: query.conditions!.map((current, i) =>
                  i === index ? { ...current, ...update } : current,
                ),
              });
            const move = (offset: number) => {
              const conditions = [...query.conditions!];
              [conditions[index], conditions[index + offset]] = [
                conditions[index + offset],
                conditions[index],
              ];
              patch({ conditions });
            };
            return (
              <fieldset key={index} className="expression-condition">
                <legend>
                  {t("Condition")} {index + 1}
                </legend>
                {index > 0 ? (
                  <Choice
                    label="Join conditions"
                    value={item.operator?.type || "and"}
                    values={["and", "or", "logic-or"]}
                    change={(type) =>
                      change({ operator: { ...item.operator, type } })
                    }
                  />
                ) : null}
                <div className="form-row">
                  {input("Input query", item.query?.params?.[0] || "", (ref) =>
                    change({
                      query: {
                        ...item.query,
                        params: [ref, ...(item.query?.params || []).slice(1)],
                      },
                    }),
                  )}
                  <Choice
                    label="Function"
                    value={item.reducer?.type || "avg"}
                    values={classicReducers}
                    change={(type) =>
                      change({ reducer: { ...item.reducer, type } })
                    }
                  />
                </div>
                <EvaluatorFields
                  label="Condition comparison"
                  classic
                  value={item.evaluator || { type: "gt", params: [0] }}
                  onChange={(evaluator) => change({ evaluator })}
                />
                <div className="query-actions">
                  <button
                    type="button"
                    disabled={index === 0}
                    onClick={() => move(-1)}
                  >
                    {t("Move condition up")}
                  </button>
                  <button
                    type="button"
                    disabled={index === query.conditions!.length - 1}
                    onClick={() => move(1)}
                  >
                    {t("Move condition down")}
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      patch({
                        conditions: query.conditions!.filter(
                          (_, i) => i !== index,
                        ),
                      })
                    }
                  >
                    {t("Remove condition")}
                  </button>
                </div>
              </fieldset>
            );
          })}
          <button
            type="button"
            onClick={() =>
              patch({
                conditions: [
                  ...(query.conditions || []),
                  {
                    query: { params: [refs[0] || "A"] },
                    reducer: { type: "avg" },
                    evaluator: { type: "gt", params: [0] },
                    operator: { type: "and" },
                  },
                ],
              })
            }
          >
            {t("Add condition")}
          </button>
        </>
      ) : null}
      {type === "sql" ? (
        <>
          <Choice
            label="Result format"
            value={query.format || "table"}
            values={["table", "alerting"]}
            change={(format) => patch({ format })}
          />
          <p className="subtle">
            {t(
              "SQL reads backend query references as tables. One SQL expression per query group.",
            )}
          </p>
        </>
      ) : null}
      {!operations.includes(type) ? (
        <p role="alert">
          {t(
            "Unsupported expression operation. Use advanced JSON to inspect this query.",
          )}
        </p>
      ) : null}
    </div>
  );
}
