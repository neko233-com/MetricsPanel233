import { t as tr } from "../i18n";
import { useEffect, useMemo, useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import {
  api,
  download,
  interpolate,
  message,
  ranges,
  type Dashboard,
} from "../api";
import { Chart } from "../components/Chart";

export function TemplateView({
  dashboard,
  range,
  onRange,
  tick,
  refresh,
}: {
  dashboard: Dashboard;
  range: string;
  onRange: (v: string) => void;
  tick: number;
  refresh: () => void;
}) {
  const variables = dashboard.variables || [];
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      variables.map((v) => [
        v.name,
        v.current || v.options[0] || (v.include_all ? ".*" : ""),
      ]),
    ),
  );
  const [options, setOptions] = useState<Record<string, string[]>>({}),
    [error, setError] = useState("");
  const variableKey = JSON.stringify(values);
  useEffect(() => {
    const controller = new AbortController();
    async function loadOptions() {
      const entries = await Promise.all(
        variables.map(async (variable) => {
          if (variable.type !== "query")
            return [variable.name, variable.options] as const;
          const query = interpolate(variable.query, values, range);
          const match =
            /^label_values\(\s*(?:(.*),\s*)?([a-zA-Z_][\w]*)\s*\)$/.exec(query);
          if (!match)
            throw new Error(
              `Variable ${variable.name}: supported query syntax is label_values(selector, label) or label_values(label)`,
            );
          const params = new URLSearchParams();
          if (match[1]) params.append("match[]", match[1].trim());
          const result = await api<{ data: string[] }>(
            `/prometheus/api/v1/label/${encodeURIComponent(match[2])}/values?${params}`,
            { signal: controller.signal },
          );
          return [variable.name, result.data] as const;
        }),
      );
      setOptions(Object.fromEntries(entries));
      setError("");
      setValues((old) => {
        const next = { ...old };
        let changed = false;
        for (const [name, choices] of entries)
          if (!next[name] && choices.length) {
            next[name] = choices[0];
            changed = true;
          }
        return changed ? next : old;
      });
    }
    void loadOptions().catch((e) => {
      if (!controller.signal.aborted) setError(message(e));
    });
    return () => controller.abort();
  }, [dashboard.id, variableKey, range]);
  const panels = useMemo(
    () =>
      dashboard.panels.map((p) => ({
        ...p,
        expr: interpolate(p.expr || "", values, range),
        expressions: p.expressions?.map((expr) =>
          interpolate(expr, values, range),
        ),
      })),
    [dashboard.panels, variableKey, range],
  );
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{dashboard.name}</h1>
          <p>{tr("Grafana template")} · PromQL</p>
        </div>
        <div className="toolbar">
          <select
            aria-label={tr("Time range")}
            value={range}
            onChange={(e) => onRange(e.target.value)}
          >
            {ranges.map((r) => (
              <option key={r.value} value={r.value}>
                {tr(r.label)}
              </option>
            ))}
          </select>
          <button
            className="icon-button outlined"
            aria-label={tr("Refresh metrics")}
            onClick={refresh}
          >
            <RefreshCw size={18} />
          </button>
          <button
            onClick={() =>
              download(`${dashboard.id}-grafana.json`, dashboard.grafana)
            }
          >
            <Download size={17} />
            {tr("Export JSON")}
          </button>
        </div>
      </div>
      {variables.length > 0 && (
        <div className="template-variables">
          {variables
            .filter((v) => v.type !== "constant")
            .map((v) => (
              <label key={v.name}>
                {v.name}
                {v.type === "textbox" ? (
                  <input
                    aria-label={v.name}
                    value={values[v.name] || ""}
                    onChange={(e) =>
                      setValues((old) => ({ ...old, [v.name]: e.target.value }))
                    }
                  />
                ) : (
                  <select
                    aria-label={v.name}
                    multiple={v.multi}
                    value={
                      v.multi
                        ? (values[v.name] || "").split("|")
                        : values[v.name] || ""
                    }
                    onChange={(e) =>
                      setValues((old) => ({
                        ...old,
                        [v.name]: v.multi
                          ? Array.from(e.target.selectedOptions)
                              .map((o) => o.value)
                              .join("|")
                          : e.target.value,
                      }))
                    }
                  >
                    {v.include_all && <option value=".*">All</option>}
                    {(options[v.name] || v.options).map((value) => (
                      <option key={value} value={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                )}
              </label>
            ))}
        </div>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="chart-grid template-grid">
        {panels.map((p) => (
          <Chart key={p.id} panel={p} range={range} tick={tick} />
        ))}
      </div>
    </>
  );
}
