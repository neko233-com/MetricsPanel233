import { useEffect, useMemo, useState } from "react";
import { arrayToDataFrame, type DataFrame } from "@grafana/data";
import {
  Observable,
  defer,
  of,
  combineLatest,
  map,
  switchMap,
  catchError,
  startWith,
  shareReplay,
  timeout,
} from "rxjs";
import {
  runDatasourceAnnotationQuery,
  type AnnotationQueryResult,
} from "./annotation-query";
import { t } from "../i18n";
import {
  annotationQueries,
  annotationSource,
  nativeAnnotationParams,
} from "./annotation-config";
import {
  api,
  dashboardUID,
  message,
  type Dashboard,
  type InterpolationValues,
  type Panel,
} from "../api";
import { resolveTimeRange, type TimeSelection } from "./time-range";
import { isTimeRegionQuery, timeRegionQuery } from "./time-regions";

export type Annotation = {
  id?: number | string;
  key?: string;
  readOnly?: boolean;
  dashboardUID?: string;
  panelId?: number;
  time: number;
  timeEnd: number;
  text: string;
  tags: string[];
  color?: string;
  queryName?: string;
  alertId?: number;
  alertUID?: string;
  alertName?: string;
  prevState?: string;
  newState?: string;
};

function alertColor(state?: string) {
  switch (state?.split(" (")[0]) {
    case "Alerting":
      return "#f2495c";
    case "Normal":
      return "#39d99c";
    case "Pending":
    case "Recovering":
      return "#ffb357";
    case "Error":
      return "#e02f44";
    case "NoData":
      return "#8e8e8e";
    default:
      return undefined;
  }
}

export function annotationStateLabel(
  state: string,
  translate: (text: string) => string,
) {
  const [name, reason] = state.split(" (");
  return (
    translate(name === "Pending" ? "Pending alert" : name) +
    (reason ? ` (${translate(reason.slice(0, -1))})` : "")
  );
}
type Result = { events: Annotation[]; error?: string };
const cache = new Map<string, Observable<Result>>();
function cached(key: string, create: () => Observable<Result>) {
  let stream = cache.get(key);
  if (!stream) {
    stream = defer(create).pipe(shareReplay({ bufferSize: 1, refCount: true }));
    cache.set(key, stream);
    if (cache.size > 200) cache.delete(cache.keys().next().value!);
  }
  return stream;
}
function nativeQuery(params: URLSearchParams) {
  return new Observable<Result>((subscriber) => {
    const controller = new AbortController();
    api<Annotation[]>("/api/annotations?" + params, {
      signal: controller.signal,
    })
      .then((events) => {
        subscriber.next({
          events: events.map((event) => ({
            ...event,
            key: `native:${event.id}`,
            readOnly: Boolean(event.alertId),
          })),
        });
        subscriber.complete();
      })
      .catch((error) => subscriber.error(error));
    return () => controller.abort();
  });
}
function externalEvents(result: AnnotationQueryResult, uid: string): Result {
  const events: Annotation[] = [];
  for (const raw of result.events) {
    const time = Number(raw.time),
      end = raw.timeEnd == null ? time : Number(raw.timeEnd);
    if (
      !Number.isSafeInteger(time) ||
      time <= 0 ||
      !Number.isSafeInteger(end) ||
      end < time
    )
      continue;
    const text = String(raw.text ?? raw.title ?? ""),
      tags = Array.isArray(raw.tags)
        ? raw.tags.filter((tag) => typeof tag === "string")
        : [];
    const key = JSON.stringify([
      uid,
      raw.id ?? [time, end, text, tags, raw.newState],
    ]);
    events.push({
      ...raw,
      time,
      timeEnd: end,
      text,
      tags,
      key: "plugin:" + key,
      readOnly: true,
    } as Annotation);
  }
  return { events, error: result.error };
}
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
    [problems, setProblems] = useState<{ query: string; message: string }[]>(
      [],
    ),
    [change, setChange] = useState(0);
  useEffect(() => {
    const update = () => setChange(revision);
    window.addEventListener("metricspanel-annotations-updated", update);
    return () =>
      window.removeEventListener("metricspanel-annotations-updated", update);
  }, []);
  const scope = JSON.stringify([dashboard, values, range]);
  useEffect(() => {
    if (!dashboard || disabled) {
      setEvents([]);
      setProblems([]);
      return;
    }
    const uid = dashboardUID(dashboard);
    let subscription: { unsubscribe(): void } | undefined;
    try {
      const resolved = resolveTimeRange(range),
        fixed = {
          from: resolved.start,
          to: resolved.end,
          timezone: resolved.timezone,
        };
      const panelID = Number(panel.config?.id || 0);
      const configured = annotationQueries(dashboard).filter(
        (q) =>
          q.enable !== false &&
          (!q.filter?.ids?.length ||
            (q.filter.exclude
              ? !q.filter.ids.includes(panelID)
              : q.filter.ids.includes(panelID))),
      );
      if (configured.length > 32)
        throw new Error("At most 32 annotation queries");
      const streams = configured.map((q) => {
        const source = defer(() => {
          const {
            ref,
            uid: sourceUID,
            native: builtin,
          } = annotationSource(q, values, fixed);
          let stream: Observable<Result>;
          if (!builtin) {
            const key = JSON.stringify([
              uid,
              dashboard.updated_at,
              ref,
              q,
              fixed,
              values,
              tick,
              change,
              sessionStorage.getItem("metricspanel-token") || "",
            ]);
            stream = cached(key, () =>
              defer(async () => {
                const runtime = await import("./plugin-runtime");
                runtime.setPluginVariables(values, fixed);
                return {
                  runtime,
                  datasource: await runtime.getPluginDatasource(
                    sourceUID,
                    Object.fromEntries(
                      Object.entries(values).map(([name, value]) => [
                        name,
                        { text: value, value },
                      ]),
                    ),
                  ),
                };
              }).pipe(
                switchMap(({ runtime, datasource }) => {
                  runtime.setPluginVariables(values, fixed);
                  const original = dashboard.grafana as
                    | Record<string, any>
                    | undefined;
                  return runDatasourceAnnotationQuery(datasource, q as any, {
                    range: resolved.sdk,
                    timezone: resolved.timezone,
                    variables: values,
                    dashboard: {
                      uid,
                      title: dashboard.name,
                      ...(original?.dashboard ||
                        original?.spec ||
                        original ||
                        {}),
                      timezone: resolved.timezone,
                    },
                    width: window.innerWidth,
                  }).pipe(
                    map((result) => externalEvents(result, datasource.uid)),
                  );
                }),
              ),
            );
          } else {
            if (isTimeRegionQuery(q))
              return cached(JSON.stringify(["time-regions", q, fixed]), () =>
                of<Result>(timeRegionQuery(q, fixed)),
              );
            const params = nativeAnnotationParams(q, dashboard, values, fixed);
            if (!params) return of({ events: [] } as Result);
            const key = `${params}:${tick}:${change}:${sessionStorage.getItem("metricspanel-token") || ""}`;
            stream = cached(key, () => nativeQuery(params));
          }
          return stream;
        });
        return source.pipe(
          timeout({ first: 60000, each: 60000 }),
          map((result) => ({
            ...result,
            events: result.events.map((event) => ({
              ...event,
              color:
                alertColor(event.newState) ||
                q.iconColor ||
                event.color ||
                "#5ac8de",
              queryName: q.name || "Annotations",
            })),
          })),
          catchError((error) =>
            of({
              events: [],
              error:
                error?.name === "TimeoutError"
                  ? "Annotation query timed out"
                  : message(error),
            } as Result),
          ),
          startWith({ events: [] } as Result),
          map((result) => ({ ...result, query: q.name || "Annotations" })),
        );
      });
      subscription = (
        streams.length ? combineLatest(streams) : of([])
      ).subscribe((results) => {
        const unique = new Map<string, Annotation>();
        for (const result of results)
          for (const event of result.events)
            if (
              (!event.panelId || Number(event.panelId) === panelID) &&
              event.time <= resolved.end &&
              event.timeEnd >= resolved.start
            )
              unique.set(event.key || `native:${event.id}`, event);
        if (unique.size > 1000) {
          setEvents([]);
          setProblems([
            { query: "", message: "At most 1000 visible annotations" },
          ]);
          return;
        }
        setEvents([...unique.values()]);
        setProblems(
          results
            .filter((result) => result.error)
            .map((result) => ({ query: result.query, message: result.error! })),
        );
      });
    } catch (e) {
      setEvents([]);
      setProblems([{ query: "", message: message(e) }]);
    }
    return () => {
      subscription?.unsubscribe();
    };
  }, [scope, tick, change, disabled, panel.config?.id]);
  const frames = useMemo<DataFrame[]>(
    () =>
      events.length
        ? [
            arrayToDataFrame(
              events.map(({ key: _key, readOnly: _readOnly, ...event }) => ({
                ...event,
                alertId: event.alertId || 0,
                alertUID: event.alertUID || "",
                alertName: event.alertName || "",
                prevState: event.prevState || "",
                newState: event.newState || "",
              })),
              [
                ...new Set([
                  "id",
                  "alertId",
                  "alertUID",
                  "alertName",
                  "prevState",
                  "newState",
                  ...events.flatMap((event) =>
                    Object.keys(event).filter(
                      (key) => key !== "key" && key !== "readOnly",
                    ),
                  ),
                ]),
              ],
            ),
          ]
        : [],
    [events],
  );
  return {
    events,
    frames,
    error: problems
      .map((problem) =>
        [problem.query, t(problem.message)].filter(Boolean).join(": "),
      )
      .join("; "),
  };
}
