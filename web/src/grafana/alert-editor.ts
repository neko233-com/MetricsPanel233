import type { GrafanaTarget } from "./engine";
import { isExpressionRef, remapExpressionQuery } from "./expression-ref";

export type AlertQueryNode = {
  [key: string]: any;
  refId: string;
  datasourceUid?: string;
  queryType?: string;
  relativeTimeRange?: { [key: string]: any; from?: number; to?: number };
  model: Record<string, any>;
};
export type AlertQueryGraph = {
  [key: string]: any;
  condition: string;
  data: AlertQueryNode[];
};
export function alertQueryUID(node: AlertQueryNode): string {
  const uid =
    node.datasourceUid ||
    (typeof node.model.datasource === "string"
      ? node.model.datasource
      : node.model.datasource?.uid) ||
    "";
  if (isExpressionRef(uid) || (!uid && isExpressionRef(node.model.datasource)))
    return "__expr__";
  if (uid === "prometheus") return "metricspanel";
  if (["-1", "-- Grafana --"].includes(uid)) return "grafana";
  return uid;
}
export function alertEditorTarget(node: AlertQueryNode): GrafanaTarget {
  return {
    ...node.model,
    refId: node.refId,
    datasource: {
      ...(typeof node.model.datasource === "object"
        ? node.model.datasource
        : {}),
      uid: alertQueryUID(node),
      ...(alertQueryUID(node) === "__expr__" ? { type: "__expr__" } : {}),
    },
  };
}
export function withAlertTarget(
  graph: AlertQueryGraph,
  index: number,
  target: GrafanaTarget,
): AlertQueryGraph {
  const previous = graph.data[index],
    canonical = alertEditorTarget(previous);
  const model = { ...previous.model, ...target };
  const refId = target.refId || previous.refId;
  const ref =
    typeof target.datasource === "string"
      ? { uid: target.datasource }
      : target.datasource;
  const changedSource =
    JSON.stringify(target.datasource) !== JSON.stringify(canonical.datasource);
  if (!Object.hasOwn(previous.model, "refId")) delete model.refId;
  else model.refId = refId;
  if (!Object.hasOwn(previous.model, "datasource")) delete model.datasource;
  else if (!changedSource) model.datasource = previous.model.datasource;
  const next = {
    ...previous,
    refId,
    model,
    ...(changedSource ? { datasourceUid: ref?.uid || "metricspanel" } : {}),
  };
  return {
    ...graph,
    condition: graph.condition === previous.refId ? refId : graph.condition,
    ...(graph.record?.from === previous.refId
      ? { record: { ...graph.record, from: refId } }
      : {}),
    data: graph.data.map((node, i) => {
      if (i === index) return next;
      if (
        refId === previous.refId ||
        alertQueryUID(node) !== "__expr__" ||
        node.model.type === "sql"
      )
        return node;
      return {
        ...node,
        model: remapExpressionQuery(
          node.model,
          new Map([[previous.refId, refId]]),
        ),
      };
    }),
  };
}
export function validateAlertEditorGraph(graph: AlertQueryGraph) {
  if (
    !graph ||
    !Array.isArray(graph.data) ||
    graph.data.length < 1 ||
    graph.data.length > 32
  )
    throw new Error("Alert graph needs 1 to 32 queries");
  const refs = new Set<string>();
  for (const node of graph.data) {
    if (
      typeof node.refId !== "string" ||
      !node.refId ||
      new TextEncoder().encode(node.refId).length > 100 ||
      refs.has(node.refId)
    )
      throw new Error(
        "Alert query references must be unique and contain 1 to 100 bytes",
      );
    refs.add(node.refId);
    if (
      !node.model ||
      typeof node.model !== "object" ||
      Array.isArray(node.model) ||
      !alertQueryUID(node)
    )
      throw new Error("Each alert query needs a datasource and model");
    const from = node.relativeTimeRange?.from ?? 0,
      to = node.relativeTimeRange?.to ?? 0;
    if (
      !Number.isSafeInteger(from) ||
      !Number.isSafeInteger(to) ||
      from < to ||
      to < 0 ||
      from > 31 * 86400
    )
      throw new Error(
        "Alert query range must be 0 to 31 days with from greater than or equal to to",
      );
  }
  if (!refs.has(graph.record?.from || graph.condition))
    throw new Error("Choose an existing query as the alert condition");
}
export function nextAlertReference(graph: AlertQueryGraph) {
  const refs = new Set(graph.data.map((node) => node.refId));
  for (let i = 0; ; i++) {
    const ref = i < 26 ? String.fromCharCode(65 + i) : "Q" + (i + 1);
    if (!refs.has(ref)) return ref;
  }
}
