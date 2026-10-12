import {
  api,
  interpolate,
  type Variable,
  type Labels,
  type InterpolationValues,
} from "../api";
import type { VariableValues } from "./engine";
import { resolveTimeRange, type TimeSelection } from "./time-range";
import {
  parseCustomChoices,
  variableDefault,
  variableOrder,
  validSelection,
  type VariableChoice,
} from "./variable-model";
const sameSourceType = (actual: string, wanted: string) =>
  actual === wanted ||
  [actual, wanted].every((type) =>
    ["postgres", "grafana-postgresql-datasource"].includes(type),
  );

export function queryValues(
  values: VariableValues,
  variables: Variable[],
  choices?: Record<string, VariableChoice[]>,
): InterpolationValues {
  return Object.fromEntries(
    variables.map((v) => {
      const value = values[v.name];
      if (
        value === "$__all" ||
        (Array.isArray(value) && value.includes("$__all"))
      )
        return [
          v.name,
          v.config?.allValue
            ? { raw: v.config.allValue }
            : (
                choices?.[v.name] ||
                (v.options || []).map((value) => ({ text: value, value }))
              ).map((choice) => choice.value),
        ];
      return [v.name, value ?? ""];
    }),
  );
}
export async function variableChoices(
  variable: Variable,
  values: InterpolationValues,
  range: TimeSelection,
  signal: AbortSignal,
): Promise<VariableChoice[]> {
  signal.throwIfAborted();
  let result = variable.options || [];
  let labels = new Map(
    (variable.config?.options || []).map((option) => [
      String(option.value),
      String(option.text),
    ]),
  );
  if (variable.type === "custom" || variable.type === "interval") {
    const parsed = parseCustomChoices(
      interpolate(variable.query, values, range),
    );
    result = parsed.map((choice) => choice.value);
    labels = new Map(parsed.map((choice) => [choice.value, choice.text]));
    if (variable.type === "interval" && variable.config?.auto) {
      const { start, end } = resolveTimeRange(range);
      const count = Math.max(1, Number(variable.config.auto_count) || 30);
      const min = /^(\d+(?:\.\d+)?)(ms|s|m|h|d|w)$/.exec(
        variable.config.auto_min || "10s",
      );
      const scale = {
        ms: 1,
        s: 1000,
        m: 60000,
        h: 3600000,
        d: 86400000,
        w: 604800000,
      };
      const floor = min
        ? Number(min[1]) * scale[min[2] as keyof typeof scale]
        : 10000;
      const auto = `${Math.max(1, Math.ceil(Math.max(floor, (end - start) / count) / 1000))}s`;
      result.unshift(auto);
      labels.set(auto, `Auto (${auto})`);
    }
  }
  if (variable.type === "constant" || variable.type === "textbox") {
    const value = interpolate(
      variable.query || String(variableDefault(variable)),
      values,
      range,
    );
    result = [value];
    labels.set(value, value);
  }
  if (variable.type === "datasource") {
    const sources = await api<{ uid: string; name: string; type: string }[]>(
      "/api/datasources",
      { signal },
    );
    const type = variable.query;
    const pattern = variable.config?.regex
      ? interpolate(variable.config.regex, values, range)
      : undefined;
    const regex = pattern
      ? new RegExp(
          pattern.startsWith("/")
            ? pattern.slice(1, pattern.lastIndexOf("/"))
            : pattern,
        )
      : undefined;
    result = sources
      .filter(
        (source) =>
          (!type ||
            source.type === type ||
            [source.type, type].every((value) =>
              ["postgres", "grafana-postgresql-datasource"].includes(value),
            )) &&
          (!regex || regex.test(source.name)),
      )
      .map((source) => source.uid);
    labels = new Map(sources.map((source) => [source.uid, source.name]));
  }
  if (variable.type === "query") {
    let prefix = "/prometheus";
    let prometheusQuery = true;
    const ref = variable.config?.datasource;
    if (ref) {
      const raw = typeof ref === "string" ? ref : ref.uid || "";
      const uid = interpolate(raw, values, range);
      const sources = await api<
        { uid: string; name: string; type: string; isDefault?: boolean }[]
      >("/api/datasources", { signal });
      const source =
        uid && uid !== "default"
          ? sources.find((source) => source.uid === uid) ||
            sources.find((source) => source.name === uid) ||
            (uid === "prometheus"
              ? sources.find((source) => source.uid === "metricspanel")
              : undefined)
          : sources.find(
              (source) =>
                source.isDefault &&
                (typeof ref === "string" ||
                  !ref.type ||
                  sameSourceType(source.type, ref.type)),
            ) ||
            sources.find(
              (source) =>
                typeof ref === "object" &&
                ref.type &&
                sameSourceType(source.type, ref.type),
            );
      if (!source)
        throw new Error(
          `Variable ${variable.name}: datasource not found: ${uid}`,
        );
      if (source.type === "prometheus")
        prefix = `/api/datasources/proxy/uid/${encodeURIComponent(source.uid)}`;
      else {
        prometheusQuery = false;
        const runtime = await import("./plugin-runtime");
        runtime.setPluginVariables(values, range);
        const scopedVars = Object.fromEntries(
          Object.entries(values).map(([name, value]) => [
            name,
            { text: value, value },
          ]),
        );
        const datasource = await runtime.getPluginDatasource(
          source.uid,
          scopedVars,
        );
        if (!datasource.metricFindQuery)
          throw new Error(
            `Variable ${variable.name}: datasource does not support variable queries`,
          );
        signal.throwIfAborted();
        const queryOptions = {
          range: resolveTimeRange(range).sdk,
          scopedVars,
          variable,
          signal,
        };
        const options = await datasource.metricFindQuery(
          variable.config?.query ?? variable.query,
          queryOptions,
        );
        signal.throwIfAborted();
        result = options.map((option) => String(option.value ?? option.text));
        labels = new Map(
          options.map((option) => [
            String(option.value ?? option.text),
            String(option.text ?? option.value),
          ]),
        );
      }
    }
    if (prometheusQuery) {
      const { start, end } = resolveTimeRange(range);
      const model = variable.config?.query;
      const raw =
        typeof model === "string"
          ? model
          : model && typeof model === "object"
            ? String((model as Record<string, unknown>).query || variable.query)
            : variable.query;
      const query = interpolate(raw, values, range),
        params = new URLSearchParams({
          start: String(start / 1000),
          end: String(end / 1000),
        });
      const labels =
        /^label_values\(\s*(?:(.*),\s*)?([a-zA-Z_][\w]*)\s*\)$/.exec(query);
      const names = /^label_names\((.*)\)$/.exec(query),
        metrics = /^metrics\((.*)\)$/.exec(query),
        prom = /^query_result\((.*)\)$/s.exec(query);
      if (labels) {
        if (labels[1]) params.append("match[]", labels[1].trim());
        result = (
          await api<{ data: string[] }>(
            `${prefix}/api/v1/label/${encodeURIComponent(labels[2])}/values?${params}`,
            { signal },
          )
        ).data;
      } else if (names) {
        if (names[1]) params.append("match[]", names[1]);
        result = (
          await api<{ data: string[] }>(`${prefix}/api/v1/labels?${params}`, {
            signal,
          })
        ).data;
      } else if (metrics) {
        result = (
          await api<{ data: string[] }>(
            `${prefix}/api/v1/label/__name__/values?${params}`,
            { signal },
          )
        ).data.filter((name) => new RegExp(metrics[1]).test(name));
      } else if (prom) {
        const data = await api<{
          data: { result: { metric: Labels; value: [number, string] }[] };
        }>(
          `${prefix}/api/v1/query?${new URLSearchParams({ query: prom[1], time: String(end / 1000) })}`,
          { signal },
        );
        result = data.data.result.map(
          (s) =>
            `${s.metric.__name__ || ""}{${Object.entries(s.metric)
              .filter(([k]) => k !== "__name__")
              .map(([k, v]) => `${k}=${JSON.stringify(v)}`)
              .join(",")}} ${s.value[1]} ${s.value[0] * 1000}`,
        );
      } else
        throw new Error(
          `Variable ${variable.name}: unsupported query ${query}`,
        );
    }
  }
  let choices = result.map((value) => ({
    value: String(value),
    text: labels.get(String(value)) ?? String(value),
  }));
  if (variable.type !== "datasource" && variable.config?.regex) {
    const raw = interpolate(variable.config.regex, values, range);
    const pattern = raw.startsWith("/")
      ? raw.slice(1, raw.lastIndexOf("/"))
      : raw;
    const regex = new RegExp(pattern);
    choices = choices.flatMap((choice) => {
      regex.lastIndex = 0;
      const match = regex.exec(
        variable.config?.regexApplyTo === "text" ? choice.text : choice.value,
      );
      if (!match) return [];
      if (match.groups?.text || match.groups?.value)
        return [
          {
            text: match.groups.text || choice.text,
            value: match.groups.value || choice.value,
          },
        ];
      const captured = match[1];
      return [
        { value: captured || choice.value, text: captured || choice.text },
      ];
    });
  }
  const sort = variable.config?.sort;
  if (sort && sort !== "disabled") {
    const numeric = [
      3,
      4,
      7,
      8,
      "numericalAsc",
      "numericalDesc",
      "naturalAsc",
      "naturalDesc",
    ].includes(sort);
    choices = [...choices].sort((a, b) =>
      a.text.localeCompare(b.text, undefined, { numeric, sensitivity: "base" }),
    );
    if (
      [
        2,
        4,
        6,
        8,
        "alphabeticalDesc",
        "numericalDesc",
        "alphabeticalCaseInsensitiveDesc",
        "naturalDesc",
      ].includes(sort)
    )
      choices.reverse();
  }
  signal.throwIfAborted();
  return [...new Map(choices.map((choice) => [choice.value, choice])).values()];
}

export async function variableOptions(
  variable: Variable,
  values: InterpolationValues,
  range: TimeSelection,
  signal: AbortSignal,
): Promise<string[]> {
  return (await variableChoices(variable, values, range, signal)).map(
    (choice) => choice.value,
  );
}

export async function resolveVariables(
  variables: Variable[],
  requested: VariableValues,
  range: TimeSelection,
  signal: AbortSignal,
  load = variableChoices,
) {
  const values: VariableValues = {},
    choices: Record<string, VariableChoice[]> = {};
  for (const variable of variableOrder(variables)) {
    signal.throwIfAborted();
    choices[variable.name] = await load(
      variable,
      queryValues(values, variables, choices),
      range,
      signal,
    );
    signal.throwIfAborted();
    values[variable.name] = validSelection(
      variable,
      requested[variable.name] ?? variableDefault(variable),
      choices[variable.name],
    );
  }
  return {
    values,
    choices,
    effective: queryValues(values, variables, choices),
  };
}
