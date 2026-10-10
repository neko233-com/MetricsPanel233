import {
  api,
  interpolate,
  type Variable,
  type Labels,
  type InterpolationValues,
} from "../api";
import type { VariableValues } from "./engine";
import { resolveTimeRange, type TimeSelection } from "./time-range";

export function queryValues(
  values: VariableValues,
  variables: Variable[],
): InterpolationValues {
  return Object.fromEntries(
    variables.map((v) => {
      const value = values[v.name];
      if (
        value === "$__all" ||
        (Array.isArray(value) && value.includes("$__all"))
      )
        return [v.name, { raw: v.config?.allValue || ".*" }];
      return [v.name, value ?? ""];
    }),
  );
}
export async function variableOptions(
  variable: Variable,
  values: InterpolationValues,
  range: TimeSelection,
  signal: AbortSignal,
): Promise<string[]> {
  if (variable.type === "datasource") return ["prometheus"];
  let result = variable.options;
  if (variable.type === "query") {
    const { start, end } = resolveTimeRange(range);
    const query = interpolate(variable.query, values, range),
      params = new URLSearchParams({
        start: String(start / 1000),
        end: String(end / 1000),
      });
    const labels = /^label_values\(\s*(?:(.*),\s*)?([a-zA-Z_][\w]*)\s*\)$/.exec(
      query,
    );
    const names = /^label_names\((.*)\)$/.exec(query),
      metrics = /^metrics\((.*)\)$/.exec(query),
      prom = /^query_result\((.*)\)$/s.exec(query);
    if (labels) {
      if (labels[1]) params.append("match[]", labels[1].trim());
      result = (
        await api<{ data: string[] }>(
          `/prometheus/api/v1/label/${encodeURIComponent(labels[2])}/values?${params}`,
          { signal },
        )
      ).data;
    } else if (names) {
      if (names[1]) params.append("match[]", names[1]);
      result = (
        await api<{ data: string[] }>(`/prometheus/api/v1/labels?${params}`, {
          signal,
        })
      ).data;
    } else if (metrics) {
      result = (
        await api<{ data: string[] }>(
          `/prometheus/api/v1/label/__name__/values?${params}`,
          { signal },
        )
      ).data.filter((name) => new RegExp(metrics[1]).test(name));
    } else if (prom) {
      const data = await api<{
        data: { result: { metric: Labels; value: [number, string] }[] };
      }>(
        `/prometheus/api/v1/query?${new URLSearchParams({ query: prom[1], time: String(end / 1000) })}`,
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
      throw new Error(`Variable ${variable.name}: unsupported query ${query}`);
  }
  if (variable.config?.regex) {
    const raw = interpolate(variable.config.regex, values, range);
    const pattern = raw.startsWith("/")
      ? raw.slice(1, raw.lastIndexOf("/"))
      : raw;
    const regex = new RegExp(pattern);
    result = result.flatMap((value) => {
      const match = regex.exec(value);
      return match ? [match.groups?.value || match[1] || match[0]] : [];
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
    result = [...result].sort((a, b) =>
      a.localeCompare(b, undefined, { numeric, sensitivity: "base" }),
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
      result.reverse();
  }
  return [...new Set(result)];
}
