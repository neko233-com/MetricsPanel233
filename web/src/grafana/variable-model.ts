import type { Variable } from "../api";
import type { VariableValues } from "./engine";

export type VariableChoice = { text: string; value: string };
export const editableVariableTypes = [
  "query",
  "custom",
  "constant",
  "textbox",
  "datasource",
  "interval",
];

export function parseCustomChoices(query: string): VariableChoice[] {
  const parts: string[] = [];
  let part = "",
    escaped = false;
  for (const char of query) {
    if (escaped) {
      part += char;
      escaped = false;
    } else if (char === "\\") escaped = true;
    else if (char === ",") {
      parts.push(part);
      part = "";
    } else part += char;
  }
  if (escaped) part += "\\";
  parts.push(part);
  return parts
    .map((part) => {
      const [text, ...values] = part.trim().split(/\s+:\s+/);
      return { text, value: values.length ? values.join(" : ") : text };
    })
    .filter((choice) => choice.value !== "");
}

export function variableDefault(variable: Variable): string | string[] {
  const raw = variable.config?.current?.value;
  const current = Array.isArray(raw)
    ? raw.map(String)
    : raw != null
      ? String(raw)
      : variable.current;
  if (variable.type === "constant") return variable.query;
  if (variable.multi)
    return Array.isArray(current) ? current : current ? current.split("|") : [];
  return Array.isArray(current)
    ? current[0] || ""
    : current ||
        variable.options?.[0] ||
        (variable.type === "textbox" ? variable.query : "");
}

export function selectedVariables(
  variables: Variable[],
  params: URLSearchParams,
): VariableValues {
  return Object.fromEntries(
    variables.map((variable) => {
      const selected =
        variable.type === "constant" || variable.config?.skipUrlSync
          ? []
          : params.getAll(`var-${variable.name}`);
      return [
        variable.name,
        selected.length
          ? variable.multi
            ? selected
            : selected[0]
          : variableDefault(variable),
      ];
    }),
  );
}

export function variableDependencies(
  variable: Variable,
  names: Set<string>,
): string[] {
  const value = JSON.stringify([
    variable.query,
    variable.config?.query,
    variable.config?.datasource,
    variable.config?.regex,
  ]);
  const result = new Set<string>();
  for (const match of value.matchAll(
    /\$\{([a-zA-Z_][\w]*)(?::[\w]+)?\}|\$([a-zA-Z_][\w]*)|\[\[([a-zA-Z_][\w]*)(?::[\w]+)?\]\]/g,
  )) {
    const name = match[1] || match[2] || match[3];
    if (names.has(name)) result.add(name);
  }
  return [...result];
}

export function variableOrder(variables: Variable[]): Variable[] {
  const byName = new Map(
    variables.map((variable) => [variable.name, variable]),
  );
  if (byName.size !== variables.length)
    throw new Error("Variable names must be unique");
  const names = new Set(byName.keys()),
    active = new Set<string>(),
    done = new Set<string>(),
    result: Variable[] = [];
  function visit(variable: Variable) {
    if (done.has(variable.name)) return;
    if (active.has(variable.name))
      throw new Error(
        `Variable dependency cycle: ${[...active, variable.name].join(" → ")}`,
      );
    active.add(variable.name);
    for (const name of variableDependencies(variable, names))
      visit(byName.get(name)!);
    active.delete(variable.name);
    done.add(variable.name);
    result.push(variable);
  }
  variables.forEach(visit);
  return result;
}

export function validSelection(
  variable: Variable,
  selected: string | string[] | undefined,
  choices: VariableChoice[],
): string | string[] {
  if (variable.type === "constant") return choices[0]?.value ?? variable.query;
  if (variable.type === "textbox")
    return Array.isArray(selected) ? selected[0] || "" : (selected ?? "");
  const desired = Array.isArray(selected)
    ? selected
    : selected != null && selected !== ""
      ? [selected]
      : [];
  if (variable.include_all && desired.includes("$__all"))
    return variable.multi ? ["$__all"] : "$__all";
  const allowed = new Set(choices.map((choice) => choice.value));
  const values = desired.filter(
    (value) => allowed.has(value) || variable.config?.allowCustomValue === true,
  );
  const fallback = choices[0]?.value || (variable.include_all ? "$__all" : "");
  return variable.multi
    ? values.length
      ? [...new Set(values)]
      : fallback
        ? [fallback]
        : []
    : (values[0] ?? fallback);
}

export function variableURL(
  params: URLSearchParams,
  variables: Variable[],
  values: VariableValues,
): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const key of [...next.keys()])
    if (key.startsWith("var-")) next.delete(key);
  for (const variable of variables) {
    if (variable.type === "constant" || variable.config?.skipUrlSync) continue;
    const value = values[variable.name];
    for (const choice of Array.isArray(value) ? value : [value ?? ""])
      next.append(`var-${variable.name}`, choice);
  }
  return next;
}

export function validateVariables(variables: Variable[]) {
  if (variables.length > 128)
    throw new Error("A dashboard supports at most 128 variables");
  for (const variable of variables) {
    if (
      !/^[a-zA-Z_][\w]*$/.test(variable.name) ||
      variable.name.startsWith("__") ||
      variable.name.length > 100
    )
      throw new Error(
        "Variable names must start with a letter or underscore, use letters, numbers or underscores, and cannot start with __",
      );
  }
  variableOrder(variables);
}
