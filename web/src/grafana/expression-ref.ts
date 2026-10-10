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

// A classic query holds input RefIDs in condition.query.params[0]. Keep its
// legacy range parameters and unknown fields intact when building comparisons.
export function remapExpressionQuery<
  T extends {
    refId?: string;
    type?: string;
    expression?: string;
    conditions?: Array<{
      query?: { params?: string[]; [key: string]: unknown };
      [key: string]: unknown;
    }>;
  },
>(query: T, refs: Map<string, string>): T {
  return {
    ...query,
    ...(typeof query.expression === "string"
      ? { expression: remapExpressionInput(query.expression, refs) }
      : {}),
    ...(query.type === "classic_conditions" && Array.isArray(query.conditions)
      ? {
          conditions: query.conditions.map((condition) => {
            if (!condition || typeof condition !== "object") return condition;
            const params = condition.query?.params;
            if (
              !Array.isArray(params) ||
              !params.length ||
              !refs.has(params[0])
            )
              return condition;
            return {
              ...condition,
              query: {
                ...condition.query,
                params: [refs.get(params[0])!, ...params.slice(1)],
              },
            };
          }),
        }
      : {}),
  };
}
