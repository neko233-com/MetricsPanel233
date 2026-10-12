export type SQLVariable = {
  value: unknown;
  multi?: boolean;
  includeAll?: boolean;
};

function stringValue(value: unknown): string {
  if (value == null) return "";
  if (["string", "number", "boolean"].includes(typeof value))
    return String(value);
  throw new Error("SQL variables must contain strings, numbers or booleans");
}

function escapeSQL(value: string, dialect: string) {
  const escaped = value.replace(/'/g, "''");
  return dialect === "mysql" ? escaped.replace(/\\/g, "\\\\") : escaped;
}

// Time macros remain intact: the backend expands them using this request's
// exact range and interval. Only dashboard/scoped variables are interpolated.
export function interpolateSQLVariables(
  sql: string,
  variables: Record<string, SQLVariable>,
  dialect: string,
): string {
  return sql.replace(
    /\$\{([a-zA-Z_][\w]*)(?::([\w]+))?\}|\$([a-zA-Z_][\w]*)|\[\[([a-zA-Z_][\w]*)(?::([\w]+))?\]\]/g,
    (
      match,
      braced: string,
      mode: string,
      plain: string,
      legacy: string,
      legacyMode: string,
    ) => {
      const name = braced || plain || legacy;
      if (name.startsWith("__") || !Object.hasOwn(variables, name))
        return match;
      const variable = variables[name];
      if (variable.value === undefined) return match;
      if (
        variable.value &&
        typeof variable.value === "object" &&
        !Array.isArray(variable.value) &&
        typeof (variable.value as { raw?: unknown }).raw === "string"
      )
        return (variable.value as { raw: string }).raw;
      const parts = (
        Array.isArray(variable.value) ? variable.value : [variable.value]
      ).map(stringValue);
      switch (mode || legacyMode) {
        case "raw":
        case "csv":
          return parts.join(",");
        case "pipe":
          return parts.join("|");
        case "json":
          return JSON.stringify(variable.value);
        case "doublequote":
          return parts.map((value) => JSON.stringify(value)).join(",");
        case "singlequote":
          return parts
            .map((value) => `'${value.replace(/['\\]/g, "\\$&")}'`)
            .join(",");
        case "sqlstring":
          return parts
            .map((value) => `'${escapeSQL(value, dialect)}'`)
            .join(",");
        case "percentencode":
          return encodeURIComponent(parts.join(","));
        case "regex":
          return parts
            .map((value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
            .join("|");
        case undefined:
        case "":
          break;
        default:
          throw new Error(
            `Unsupported SQL variable format: ${mode || legacyMode}`,
          );
      }
      return Array.isArray(variable.value) ||
        variable.multi ||
        variable.includeAll
        ? parts.map((value) => `'${escapeSQL(value, dialect)}'`).join(",")
        : escapeSQL(parts[0] || "", dialect);
    },
  );
}
