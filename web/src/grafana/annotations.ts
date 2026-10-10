import { useEffect, useMemo, useState } from "react";
import { toDataFrame, type DataFrame } from "@grafana/data";
import {
  api,
  dashboardUID,
  interpolate,
  message,
  type Dashboard,
  type InterpolationValues,
  type Panel,
} from "../api";
import { resolveTimeRange, type TimeSelection } from "./time-range";

export type Annotation = {
  id: number;
  dashboardUID?: string;
  panelId?: number;
  time: number;
  timeEnd: number;
  text: string;
  tags: string[];
  color?: string;
  queryName?: string;
};
type Query = {
  enable?: boolean;
  builtIn?: number;
  name?: string;
  iconColor?: string;
  datasource?: string | { type?: string; uid?: string };
  type?: string;
  tags?: string[];
  matchAny?: boolean;
  limit?: number;
  target?: {
    type?: string;
    tags?: string[];
    matchAny?: boolean;
    limit?: number;
  };
  query?: {
    group?: string;
    datasource?: { name?: string };
    spec?: Record<string, unknown>;
  };
  filter?: { ids?: number[]; exclude?: boolean };
};
function queries(dashboard?: Dashboard): Query[] {
  const original = dashboard?.grafana as Record<string, any> | undefined;
  const spec = original?.dashboard || original?.spec || original;
  const annotations = spec?.annotations;
  if (Array.isArray(annotations))
    return annotations.map((item) => ({
      ...item.spec?.legacyOptions,
      ...item.spec,
      target: { ...item.spec?.legacyOptions, ...item.spec?.query?.spec },
      datasource: {
        type: item.spec?.query?.group,
        uid: item.spec?.query?.datasource?.name,
      },
    }));
  if (Array.isArray(annotations?.list)) return annotations.list;
  return [
    {
      builtIn: 1,
      name: "Annotations & Alerts",
      enable: true,
      type: "dashboard",
    },
  ];
}
const cache = new Map<string, Promise<Annotation[]>>();
let revision = 0;
export function annotationsChanged() {
  revision++;
  cache.clear();
  window.dispatchEvent(new Event("metricspanel-annotations-updated"));
}
export function useAnnotations(
  dashboard: Dashboard | undefined,
  panel: Panel,
  range: TimeSelection,
  tick: number,
  values: InterpolationValues = {},
  disabled = false,
) {
  const [events, setEvents] = useState<Annotation[]>([]),
    [error, setError] = useState(""),
    [change, setChange] = useState(0);
  useEffect(() => {
    const update = () => setChange(revision);
    window.addEventListener("metricspanel-annotations-updated", update);
    return () =>
      window.removeEventListener("metricspanel-annotations-updated", update);
  }, []);
  const scope = JSON.stringify([dashboard, values, range]);
  useEffect(() => {
    let active = true;
    if (!dashboard || disabled) {
      setEvents([]);
      setError("");
      return;
    }
    const uid = dashboardUID(dashboard);
    const load = async () => {
      const resolved = resolveTimeRange(range),
        fixed = {
          from: resolved.start,
          to: resolved.end,
          timezone: resolved.timezone,
        };
      const panelID = Number(panel.config?.id || 0);
      const configured = queries(dashboard).filter(
        (q) =>
          q.enable !== false &&
          (!q.filter?.ids?.length ||
            (q.filter.exclude
              ? !q.filter.ids.includes(panelID)
              : q.filter.ids.includes(panelID))),
      );
      if (configured.length > 32)
        throw new Error("At most 32 annotation queries");
      const results = await Promise.all(
        configured.map(async (q) => {
          const ref = q.datasource;
          if (
            ref &&
            (typeof ref === "string"
              ? !["-- Grafana --", "grafana"].includes(ref)
              : ref.type && ref.type !== "grafana")
          )
            throw new Error(
              "Annotation datasource requires a plugin annotation adapter",
            );
          const target = q.target || q;
          const params = new URLSearchParams({
            from: String(resolved.start),
            to: String(resolved.end),
            limit: String(target.limit || 100),
          });
          if (q.builtIn || target.type === "dashboard" || !target.type)
            params.set("dashboardUID", uid);
          else {
            const tags = (target.tags || []).flatMap((tag) => {
              const exact = tag.match(/^\$\{?(\w+)\}?$/);
              const selected = exact && values[exact[1]];
              return Array.isArray(selected)
                ? selected
                : [interpolate(tag, values, fixed)];
            });
            if (!tags.length) return [];
            for (const tag of tags) params.append("tags", tag);
            params.set("matchAny", String(Boolean(target.matchAny)));
          }
          const key = `${params}:${tick}:${change}:${sessionStorage.getItem("metricspanel-token") || ""}`;
          let response = cache.get(key);
          if (!response) {
            response = api<Annotation[]>("/api/annotations?" + params);
            cache.set(key, response);
            if (cache.size > 200) cache.delete(cache.keys().next().value!);
            response.catch(() => cache.delete(key));
          }
          return (await response).map((event) => ({
            ...event,
            color: q.iconColor || "#5ac8de",
            queryName: q.name || "Annotations",
          }));
        }),
      );
      const unique = new Map<number, Annotation>();
      for (const event of results.flat())
        if (!event.panelId || event.panelId === panelID)
          unique.set(event.id, event);
      if (unique.size > 1000)
        throw new Error("At most 1000 visible annotations");
      if (active) {
        setEvents([...unique.values()]);
        setError("");
      }
    };
    load().catch((e) => {
      if (active) {
        setEvents([]);
        setError(message(e));
      }
    });
    return () => {
      active = false;
    };
  }, [scope, tick, change, disabled, panel.config?.id]);
  const frames = useMemo<DataFrame[]>(
    () => (events.length ? [toDataFrame(events)] : []),
    [events],
  );
  return { events, frames, error };
}
