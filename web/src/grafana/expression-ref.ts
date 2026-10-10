import type { DataSourceRef } from "@grafana/data";

export const expressionRef = Object.freeze({
  uid: "__expr__",
  type: "__expr__",
  name: "Expression",
});
export function isExpressionRef(ref?: DataSourceRef | string | null) {
  const valid = (value?: string | null) =>
    ["__expr__", "-100", "Expression"].includes(value || "");
  return typeof ref === "string"
    ? valid(ref)
    : valid(ref?.uid) || valid(ref?.type);
}
export function remapExpressionInput(
  expression: string,
  refs: Map<string, string>,
) {
  const bare = expression.startsWith("$") ? expression.slice(1) : expression;
  if (refs.has(bare))
    return expression.startsWith("$")
      ? "$" + "{" + refs.get(bare) + "}"
      : refs.get(bare)!;
  return expression.replace(
    /\$(?:\{([^}]+)\}|([A-Za-z_]\w*))/g,
    (original, braced, simple) => {
      const mapped = refs.get(braced || simple);
      return mapped ? "$" + "{" + mapped + "}" : original;
    },
  );
}
