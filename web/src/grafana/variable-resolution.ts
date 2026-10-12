import type { VariableValues } from "./engine";
import type { VariableChoice } from "./variable-model";

type Result = {
  values: VariableValues;
  choices: Record<string, VariableChoice[]>;
};
export type VariableResolution = Result & {
  context?: string;
  pendingContext?: string;
  request: number;
  loading: boolean;
  error: string;
};
export type VariableResolutionAction =
  | { type: "invalidate"; request: number }
  | { type: "start"; request: number; context: string }
  | { type: "resolved"; request: number; context: string; result: Result }
  | { type: "failed"; request: number; context: string; error: string };

export function createVariableResolution(
  values: VariableValues,
): VariableResolution {
  return { values, choices: {}, request: 0, loading: false, error: "" };
}

export function reduceVariableResolution(
  state: VariableResolution,
  action: VariableResolutionAction,
): VariableResolution {
  if (action.type === "invalidate")
    return {
      ...state,
      context: undefined,
      pendingContext: undefined,
      request: action.request,
      loading: true,
      error: "",
    };
  if (action.type === "start")
    return {
      ...state,
      pendingContext: action.context,
      request: action.request,
      loading: true,
      error: "",
    };
  if (
    action.request !== state.request ||
    action.context !== state.pendingContext
  )
    return state;
  if (action.type === "failed")
    return { ...state, loading: false, error: action.error };
  return {
    ...state,
    ...action.result,
    context: action.context,
    loading: false,
    error: "",
  };
}

// Loading an already resolved context is a background refresh, so panel state survives.
export function variableResolutionView(
  state: VariableResolution,
  context: string,
  requested: VariableValues,
  hasVariables: boolean,
) {
  if (!hasVariables)
    return {
      ready: true,
      loading: false,
      error: "",
      values: requested,
      choices: {},
    };
  const ready = state.context === context;
  return {
    ready,
    loading: state.loading || (!ready && state.pendingContext !== context),
    error: state.pendingContext === context ? state.error : "",
    values: ready ? state.values : requested,
    choices: state.choices,
  };
}
