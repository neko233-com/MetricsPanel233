import {
  matchPluginId,
  type DataSourceInstanceSettings,
  type DataSourceRef,
} from "@grafana/data";
import type { GetDataSourceListFilters } from "@grafana/runtime";

export function resolveSourceSettings(
  sources: DataSourceInstanceSettings[],
  ref?: DataSourceRef | string | null,
  replace = (value: string) => value,
) {
  const id = replace(typeof ref === "string" ? ref : ref?.uid || "");
  if (!id || id === "default") {
    const type = typeof ref === "object" && ref?.type;
    const candidates = type
      ? sources.filter(
          (s) => s.type === type || s.meta.aliasIDs?.includes(type),
        )
      : sources;
    return candidates.find((s) => s.isDefault) || candidates[0];
  }
  return sources.find(
    (s) => s.uid === id || s.name === id || String(s.id) === id,
  );
}

export function filterSourceSettings(
  sources: DataSourceInstanceSettings[],
  filters: GetDataSourceListFilters = {},
) {
  const result = sources
    .filter((source) => {
      const meta = source.meta;
      if (["grafana", "mixed", "dashboard"].includes(meta.id)) return false;
      if (
        (filters.metrics && !meta.metrics) ||
        (filters.annotations && !meta.annotations) ||
        (filters.alerting && !meta.alerting) ||
        (filters.tracing && !meta.tracing) ||
        (filters.logs && !meta.logs && meta.category !== "logging")
      )
        return false;
      if (
        (filters.pluginId && !matchPluginId(filters.pluginId, meta)) ||
        (filters.filter && !filters.filter(source))
      )
        return false;
      if (
        filters.type &&
        !(Array.isArray(filters.type)
          ? filters.type.includes(source.type)
          : source.type === filters.type ||
            meta.aliasIDs?.includes(filters.type))
      )
        return false;
      return (
        filters.all ||
        meta.metrics ||
        meta.annotations ||
        meta.alerting ||
        meta.logs ||
        meta.tracing
      );
    })
    .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  if (!filters.pluginId && !filters.alerting) {
    for (const type of [
      filters.mixed && "mixed",
      filters.dashboard && "dashboard",
      !filters.tracing && "grafana",
    ]) {
      const builtin = type && sources.find((s) => s.meta.id === type);
      if (builtin && (!filters.filter || filters.filter(builtin)))
        result.push(builtin);
    }
  }
  return result;
}
