import type { Dashboard, InterpolationValues } from "../api";
import { dashboardUID, interpolate } from "../api";
import type { TimeSelection } from "./time-range";

export type AnnotationConfig = Record<string, any>;
export type AnnotationDraft = {
  config: AnnotationConfig;
  resource?: Record<string, any>;
  original?: AnnotationConfig;
};
const builtin = () => ({
  builtIn: 1,
  name: "Annotations & Alerts",
  enable: true,
  datasource: { type: "grafana", uid: "-- Grafana --" },
  type: "dashboard",
  iconColor: "#5ac8de",
});

export function validateAnnotationConfig(config: AnnotationConfig) {
  const object = (value: any) =>
    value && typeof value === "object" && !Array.isArray(value);
  if (
    !object(config) ||
    ["name", "iconColor"].some(
      (key) => config[key] != null && typeof config[key] !== "string",
    ) ||
    ["target", "filter", "mappings"].some(
      (key) => config[key] != null && !object(config[key]),
    )
  )
    throw new Error("Invalid annotation query configuration");
  const ref = config.datasource;
  if (
    ref != null &&
    typeof ref !== "string" &&
    (!object(ref) ||
      ["uid", "type"].some(
        (key) => ref[key] != null && typeof ref[key] !== "string",
      ))
  )
    throw new Error("Invalid annotation query configuration");
  if (
    config.filter?.ids != null &&
    (!Array.isArray(config.filter.ids) ||
      config.filter.ids.some((id: any) => !Number.isSafeInteger(id) || id < 1))
  )
    throw new Error("Invalid annotation query configuration");
  if (
    config.builtIn ||
    !ref ||
    ref === "grafana" ||
    ref === "-- Grafana --" ||
    ref.type === "grafana"
  )
    for (const target of [config, config.target || {}])
      if (
        target.tags != null &&
        (!Array.isArray(target.tags) ||
          target.tags.some((tag: any) => typeof tag !== "string"))
      )
        throw new Error("Invalid annotation query configuration");
}

export function readAnnotationDrafts(dashboard?: Dashboard): AnnotationDraft[] {
  const raw = dashboard?.grafana as Record<string, any> | undefined;
  const model = raw?.dashboard || raw?.spec || raw;
  const saved = model?.annotations ?? dashboard?.annotations;
  if (Array.isArray(saved)) {
    const drafts = saved.map((item) =>
      item?.kind === "AnnotationQuery" && item.spec
        ? {
            resource: structuredClone(item),
            config: structuredClone({
              ...item.spec.legacyOptions,
              ...item.spec,
              target: {
                ...item.spec.legacyOptions?.target,
                ...item.spec.query?.spec,
              },
              datasource: {
                type: item.spec.query?.group,
                uid: item.spec.query?.datasource?.name,
              },
            }),
          }
        : { config: structuredClone(item) },
    );
    return drafts.map((draft) =>
      draft.resource
        ? { ...draft, original: structuredClone(draft.config) }
        : draft,
    );
  }
  if (Array.isArray(saved?.list))
    return saved.list.map((config: AnnotationConfig) => ({
      config: structuredClone(config),
    }));
  return [{ config: builtin() }];
}

export function annotationQueries(dashboard?: Dashboard): AnnotationConfig[] {
  return readAnnotationDrafts(dashboard).map((draft) => draft.config);
}

// Edit only the annotation contract; unknown resource, envelope and plugin fields survive.
export function withAnnotationQueries(
  dashboard: Dashboard,
  drafts: AnnotationDraft[],
): Dashboard {
  if (drafts.length > 32) throw new Error("At most 32 annotation queries");
  drafts.forEach((draft) => validateAnnotationConfig(draft.config));
  if (!dashboard.grafana)
    return {
      ...dashboard,
      annotations: drafts.map((draft) => structuredClone(draft.config)),
    };
  const raw = structuredClone(dashboard.grafana) as Record<string, any>;
  const model = raw.dashboard || raw.spec || raw;
  if (
    Array.isArray(model.annotations) ||
    String(raw.apiVersion).includes("/v2")
  ) {
    model.annotations = drafts.map(
      ({ config, resource, original: baseline }) => {
        if (
          resource &&
          baseline &&
          JSON.stringify(config) === JSON.stringify(baseline)
        )
          return structuredClone(resource);
        const original = resource?.spec || {};
        const { datasource, target, query, legacyOptions, ...options } =
          structuredClone(config);
        const ref =
          typeof datasource === "string"
            ? { uid: datasource }
            : datasource || {};
        const spec = { ...original };
        const legacy = { ...original.legacyOptions, ...legacyOptions };
        const common = new Set([
          "name",
          "enable",
          "hide",
          "iconColor",
          "filter",
          "builtIn",
          "placement",
        ]);
        for (const [key, value] of Object.entries(options)) {
          if (common.has(key) || Object.hasOwn(original, key))
            spec[key] = key === "builtIn" ? Boolean(value) : value;
          else legacy[key] = value;
        }
        spec.legacyOptions = legacy;
        if (typeof query === "string") legacy.query = query;
        if (
          !resource ||
          !baseline ||
          JSON.stringify(target) !== JSON.stringify(baseline.target) ||
          JSON.stringify(datasource) !== JSON.stringify(baseline.datasource)
        )
          spec.query = {
            ...original.query,
            ...(query && typeof query === "object" ? query : {}),
            kind: original.query?.kind || "DataQuery",
            version: original.query?.version || "v0",
            group: ref.type || original.query?.group || "grafana",
            datasource: { ...original.query?.datasource, name: ref.uid || "" },
            spec: { ...(target || {}), refId: target?.refId || "Anno" },
          };
        delete spec.target;
        delete spec.datasource;
        return { ...resource, kind: resource?.kind || "AnnotationQuery", spec };
      },
    );
  } else {
    model.annotations = {
      ...model.annotations,
      list: drafts.map((draft) => structuredClone(draft.config)),
    };
  }
  return { ...dashboard, grafana: raw };
}

export function annotationSource(
  config: AnnotationConfig,
  values: InterpolationValues,
  range: TimeSelection,
) {
  const saved = config.datasource;
  const ref =
    typeof saved === "string"
      ? interpolate(saved, values, range)
      : saved
        ? { ...saved, uid: interpolate(saved.uid || "", values, range) }
        : undefined;
  let uid = typeof ref === "string" ? ref : ref?.uid || "";
  if (uid === "prometheus") uid = "metricspanel";
  if (uid === "default") uid = "";
  const native =
    Boolean(config.builtIn) ||
    !ref ||
    ["-- Grafana --", "grafana"].includes(uid) ||
    (!uid &&
      typeof ref === "object" &&
      ["grafana", "datasource"].includes(ref.type || ""));
  return { uid, ref, native };
}

export function nativeAnnotationParams(
  config: AnnotationConfig,
  dashboard: Dashboard,
  values: InterpolationValues,
  range: { from: number; to: number; timezone: string },
) {
  const target = { ...config, ...config.target };
  const params = new URLSearchParams({
    from: String(range.from),
    to: String(range.to),
    limit: String(target.limit || 100),
  });
  if (config.builtIn || target.type === "dashboard" || !target.type)
    params.set("dashboardUID", dashboardUID(dashboard));
  else {
    const tags = (target.tags || []).flatMap((tag: string) => {
      const exact = tag.match(/^\$\{?(\w+)\}?$/);
      const selected = exact && values[exact[1]];
      return Array.isArray(selected)
        ? selected
        : [interpolate(tag, values, range)];
    });
    if (!tags.length) return null;
    for (const tag of tags) params.append("tags", tag);
    params.set("matchAny", String(Boolean(target.matchAny)));
  }
  return params;
}
