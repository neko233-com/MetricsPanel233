import { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { SlidersHorizontal } from "lucide-react";
import { message, type Dashboard } from "../api";
import { getLocale } from "../i18n";
import {
  queryValues,
  resolveVariables,
  variableChoices,
} from "../grafana/variables";
import {
  selectedVariables,
  variableDependencies,
  variableURL,
  type VariableChoice,
} from "../grafana/variable-model";
import type { VariableValues } from "../grafana/engine";
import type { TimeSelection } from "../grafana/time-range";
import {
  createVariableResolution,
  reduceVariableResolution,
  variableResolutionView,
} from "../grafana/variable-resolution";
import "./dashboard-variables.css";

const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);
export function useDashboardVariables(
  dashboard: Dashboard,
  range: TimeSelection,
  tick = 0,
) {
  const variables = dashboard.variables || [];
  const definitionKey = JSON.stringify(variables);
  const cache = useRef(new Map<string, VariableChoice[]>());
  const [requested, setRequested] = useState<VariableValues>(() =>
    selectedVariables(variables, new URLSearchParams(location.search)),
  );
  const requestedRef = useRef(requested);
  const requestNumber = useRef(0);
  const [resolved, dispatch] = useReducer(
    reduceVariableResolution,
    requested,
    createVariableResolution,
  );
  useEffect(() => {
    const restore = () => {
      const next = selectedVariables(
        variables,
        new URLSearchParams(location.search),
      );
      if (JSON.stringify(next) !== JSON.stringify(requestedRef.current)) {
        requestedRef.current = next;
        dispatch({ type: "invalidate", request: ++requestNumber.current });
        setRequested(next);
      }
    };
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [dashboard.id, definitionKey]);
  const requestedKey = JSON.stringify(requested);
  const context = JSON.stringify([dashboard.id, definitionKey, requestedKey]);
  useEffect(() => {
    cache.current.clear();
  }, [dashboard.id, definitionKey, tick]);
  useEffect(() => {
    const controller = new AbortController();
    const request = ++requestNumber.current;
    dispatch({ type: "start", context, request });
    void resolveVariables(
      variables,
      requested,
      range,
      controller.signal,
      async (variable, values, range, signal) => {
        const names = new Set(variables.map((variable) => variable.name));
        const deps = variableDependencies(variable, names).map((name) => [
          name,
          values[name],
        ]);
        const timed =
          variable.config?.refresh === 2 ||
          variable.config?.refresh === "onTimeRangeChanged" ||
          variable.type === "interval";
        const key = JSON.stringify([variable, deps, timed ? range : null]);
        if (cache.current.has(key)) return cache.current.get(key)!;
        const frozen =
          variable.type === "query" &&
          (variable.config?.refresh === 0 ||
            variable.config?.refresh === "never");
        const result = frozen
          ? variable.config?.options ||
            (variable.options || []).map((value) => ({ text: value, value }))
          : await variableChoices(variable, values, range, signal);
        signal.throwIfAborted();
        cache.current.set(key, result);
        return result;
      },
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          dispatch({ type: "resolved", context, request, result });
        }
      })
      .catch((reason) => {
        if (!controller.signal.aborted) {
          dispatch({
            type: "failed",
            context,
            request,
            error: message(reason),
          });
        }
      });
    return () => controller.abort();
  }, [dashboard.id, definitionKey, requestedKey, range, tick]);
  const { values, choices, loading, error, ready } = variableResolutionView(
    resolved,
    context,
    requested,
    variables.length > 0,
  );
  const effective = useMemo(
    () => queryValues(values, variables, choices),
    [JSON.stringify(values), definitionKey, choices],
  );
  useEffect(() => {
    if (loading || error) return;
    const params = variableURL(
      new URLSearchParams(location.search),
      variables,
      values,
    );
    history.replaceState(
      history.state,
      "",
      `${location.pathname}${params.size ? `?${params}` : ""}${location.hash}`,
    );
    if (!variables.length) return;
    let active = true;
    void import("../grafana/plugin-runtime").then((runtime) => {
      if (active)
        runtime.setPluginVariables(
          effective,
          range,
          variables.map((variable) => ({
            ...variable,
            config: {
              ...variable.config,
              options: choices[variable.name] || variable.config?.options,
            },
          })),
        );
    });
    return () => {
      active = false;
    };
  }, [
    dashboard.id,
    definitionKey,
    JSON.stringify(values),
    range,
    loading,
    error,
    effective,
  ]);
  return {
    variables,
    values,
    choices,
    effective,
    loading,
    error,
    ready,
    change: (name: string, value: string | string[]) => {
      const next = { ...values, [name]: value };
      if (JSON.stringify(next) === JSON.stringify(values)) return;
      requestedRef.current = next;
      dispatch({ type: "invalidate", request: ++requestNumber.current });
      setRequested(next);
    },
  };
}

export type DashboardVariableState = ReturnType<typeof useDashboardVariables>;
export function DashboardVariableBar({
  state,
}: {
  state: DashboardVariableState;
}) {
  return (
    <>
      {!!state.variables.length && (
        <div
          className="template-variables"
          aria-label={text("Dashboard variable values", "仪表盘变量值")}
        >
          {state.variables
            .filter(
              (variable) =>
                variable.type !== "constant" &&
                variable.config?.hide !== 2 &&
                variable.config?.hide !== "hideVariable",
            )
            .map((variable) => {
              const hideLabel =
                variable.config?.hide === 1 ||
                variable.config?.hide === "hideLabel";
              const choices =
                state.choices[variable.name] ||
                (variable.options || []).map((value) => ({
                  value,
                  text:
                    variable.config?.options?.find(
                      (option) => option.value === value,
                    )?.text || value,
                }));
              const value = state.values[variable.name];
              const control = (
                <label
                  key={variable.name}
                  title={
                    typeof variable.config?.description === "string"
                      ? variable.config.description
                      : undefined
                  }
                >
                  <span className={hideLabel ? "variable-hidden-label" : ""}>
                    {variable.config?.label || variable.name}
                  </span>
                  {variable.type === "textbox" ? (
                    <input
                      aria-label={variable.name}
                      value={String(value || "")}
                      onChange={(event) =>
                        state.change(variable.name, event.target.value)
                      }
                    />
                  ) : (
                    <select
                      aria-label={variable.name}
                      multiple={variable.multi}
                      value={
                        variable.multi
                          ? Array.isArray(value)
                            ? value
                            : [value || ""]
                          : Array.isArray(value)
                            ? value[0] || ""
                            : value || ""
                      }
                      onChange={(event) =>
                        state.change(
                          variable.name,
                          variable.multi
                            ? Array.from(event.target.selectedOptions).map(
                                (option) => option.value,
                              )
                            : event.target.value,
                        )
                      }
                    >
                      {variable.include_all && (
                        <option value="$__all">{text("All", "全部")}</option>
                      )}
                      {choices.map((choice) => (
                        <option value={choice.value} key={choice.value}>
                          {choice.text}
                        </option>
                      ))}
                    </select>
                  )}
                </label>
              );
              return variable.config?.hide === 3 ||
                variable.config?.hide === "inControlsMenu" ? (
                <details className="variable-controls-menu" key={variable.name}>
                  <summary>{variable.config?.label || variable.name}</summary>
                  {control}
                </details>
              ) : (
                control
              );
            })}
        </div>
      )}
      {state.loading && (
        <p role="status" className="subtle">
          {text("Loading variable values…", "正在读取变量值…")}
        </p>
      )}
      {state.error && (
        <p role="alert" className="form-error">
          {state.error}
        </p>
      )}
    </>
  );
}

export function DashboardVariablesButton({ onClick }: { onClick: () => void }) {
  return (
    <button onClick={onClick}>
      <SlidersHorizontal size={17} />
      {text("Variables", "变量")}
    </button>
  );
}
