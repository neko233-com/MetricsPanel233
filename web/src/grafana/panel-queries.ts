import type { Dashboard, Panel } from "../api";
import type { GrafanaTarget } from "./engine";

export function validatePanelQueries(queries: GrafanaTarget[]) {
  if (queries.length > 32) throw new Error("At most 32 panel queries");
  const refs = new Set<string>();
  for (const query of queries) {
    if (
      !query ||
      typeof query !== "object" ||
      Array.isArray(query) ||
      typeof query.refId !== "string" ||
      !query.refId.trim() ||
      query.refId.length > 100
    )
      throw new Error("Each query needs a reference of 1 to 100 characters");
    if (refs.has(query.refId))
      throw new Error("Query references must be unique");
    refs.add(query.refId);
  }
}

export function withPanelQueries(
  dashboard: Dashboard,
  panelID: string,
  queries: GrafanaTarget[],
  originalRefs = queries.map((query) => query.refId),
): Dashboard {
  validatePanelQueries(queries);
  const source = dashboard.panels.find((panel) => panel.id === panelID);
  if (!source) throw new Error("Panel no longer exists");
  const config = source.config;
  if (JSON.stringify(queries) === JSON.stringify(config?.targets || []))
    return dashboard;
  const targets = structuredClone(queries);
  const result = {
    ...dashboard,
    panels: dashboard.panels.map((panel) =>
      panel.id === panelID
        ? {
            ...panel,
            config: {
              ...panel.config,
              type: panel.config?.type || panel.visualization || "timeseries",
              targets,
            },
            expressions: targets
              .filter((query) => !query.hide && query.expr)
              .map((query) => query.expr!),
            expr: targets.find((query) => !query.hide && query.expr)?.expr,
          }
        : panel,
    ),
  };
  if (!dashboard.grafana) return result;
  const raw = structuredClone(dashboard.grafana) as Record<string, any>;
  const model = raw.dashboard || raw.spec || raw;
  let edited = 0;
  if (model.elements) {
    for (const element of Object.values(model.elements) as any[]) {
      if (element.kind !== "Panel" || element.spec?.id !== config?.id) continue;
      const data = (element.spec.data ||= { kind: "QueryGroup", spec: {} });
      const spec = (data.spec ||= {});
      const originals = new Map<string, any>(
        (spec.queries || []).map((item: any) => [item.spec?.refId, item]),
      );
      const originalTargets = new Map(
        (config?.targets || []).map((query) => [query.refId, query]),
      );
      spec.queries = targets.map((target, index) => {
        const original = originals.get(originalRefs[index]!);
        if (
          original &&
          JSON.stringify(target) ===
            JSON.stringify(originalTargets.get(originalRefs[index]))
        )
          return original;
        const { refId, hide, datasource, ...query } = target;
        const ref =
          typeof datasource === "string"
            ? { uid: datasource }
            : datasource || {};
        return {
          ...original,
          kind: original?.kind || "PanelQuery",
          spec: {
            ...original?.spec,
            refId,
            hidden: Boolean(hide),
            query: {
              ...original?.spec?.query,
              kind: original?.spec?.query?.kind || "DataQuery",
              group: ref.type || original?.spec?.query?.group || "prometheus",
              datasource: {
                ...original?.spec?.query?.datasource,
                name: ref.uid || "",
              },
              spec: query,
            },
          },
        };
      });
      edited++;
    }
  } else {
    const walk = (panels: any[]) =>
      panels?.forEach((panel) => {
        const matches =
          config?.id != null
            ? panel.id === config.id
            : JSON.stringify(panel) === JSON.stringify(config);
        if (matches) {
          panel.targets = structuredClone(targets);
          edited++;
        }
        if (Array.isArray(panel.panels)) walk(panel.panels);
      });
    walk(model.panels);
  }
  if (edited !== 1) throw new Error("Cannot identify a unique source panel");
  return { ...result, grafana: raw };
}

export function panelWithQueries(
  panel: Panel,
  queries: GrafanaTarget[],
): Panel {
  return {
    ...panel,
    config: {
      ...panel.config,
      type: panel.config?.type || panel.visualization || "table",
      targets: queries,
    },
  };
}
