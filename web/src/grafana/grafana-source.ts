import {
  DataFrameType,
  DataFrameView,
  FieldType,
  LoadingState,
  dataFrameFromJSON,
  isValidLiveChannelAddress,
  parseLiveChannelAddress,
  type AnnotationQuery,
  type AnnotationQueryRequest,
  type DataFrame,
  type DataFrameJSON,
  type DataQuery,
  type DataQueryRequest,
  type DataQueryResponse,
  type DataSourceInstanceSettings,
} from "@grafana/data";
import {
  DataSourceWithBackend,
  getGrafanaLiveSrv,
  getTemplateSrv,
} from "@grafana/runtime";
import { Observable, defer, merge, of, catchError, map } from "rxjs";
import { api } from "../api";
import { calculateTimeRegions, type TimeRegionConfig } from "./time-regions";

export type GrafanaQuery = DataQuery & {
  queryType?: string;
  seriesCount?: number;
  startValue?: number;
  min?: number;
  max?: number;
  spread?: number;
  noise?: number;
  dropPercent?: number;
  snapshot?: DataFrameJSON[];
  timeRegion?: TimeRegionConfig;
  channel?: string;
  filter?: { fields?: string[] };
  buffer?: number;
  path?: string;
  target?: Record<string, any>;
  type?: string;
  tags?: string[];
  matchAny?: boolean;
  limit?: number;
};

export { grafanaMeta } from "./grafana-meta";

export function randomWalkFrames(
  query: GrafanaQuery,
  request: DataQueryRequest<GrafanaQuery>,
  random = Math.random,
): DataFrame[] {
  const count = query.seriesCount ?? 1,
    interval = request.intervalMs;
  const from = request.range.from.valueOf(),
    to = request.range.to.valueOf();
  if (
    !Number.isInteger(count) ||
    count < 0 ||
    count > 128 ||
    !Number.isFinite(interval) ||
    interval <= 0 ||
    !Number.isSafeInteger(from) ||
    !Number.isSafeInteger(to) ||
    to < from ||
    [
      query.startValue,
      query.min,
      query.max,
      query.spread,
      query.noise,
      query.dropPercent,
    ].some((v) => v != null && !Number.isFinite(v)) ||
    (query.min != null && query.max != null && query.min > query.max)
  )
    throw new Error("Invalid random walk configuration");
  const points = Math.min(10000, Math.ceil((to - from) / interval));
  if (count * points > 1000000)
    throw new Error("Random walk exceeds 1000000 generated rows");
  const start = query.startValue ?? random() * 100,
    spread = query.spread ?? 1,
    noise = query.noise ?? 0;
  const frames: DataFrame[] = [];
  for (let series = 0; series < count; series++) {
    let value = start;
    const times: number[] = [],
      values: number[] = [];
    for (let index = 0; index < points; index++) {
      let sample = value + random() * noise;
      if (query.min != null && sample < query.min) sample = value = query.min;
      if (query.max != null && sample > query.max) sample = value = query.max;
      if (!Number.isFinite(sample))
        throw new Error("Random walk produced a non-finite value");
      if (
        !((query.dropPercent ?? 0) > 0 && random() < query.dropPercent! / 100)
      ) {
        times.push(from + index * interval);
        values.push(sample);
      }
      value += (random() - 0.5) * spread;
    }
    frames.push({
      refId: query.refId,
      length: times.length,
      meta: { type: DataFrameType.TimeSeriesMulti },
      fields: [
        {
          name: "time",
          type: FieldType.time,
          config: { interval },
          values: times,
        },
        {
          name: `${query.refId}-series${series || ""}`,
          type: FieldType.number,
          config: {},
          values,
        },
      ],
    });
  }
  return frames;
}

export function regionFrame(
  name: string,
  config: TimeRegionConfig,
  range: { from: { valueOf(): number }; to: { valueOf(): number } },
): DataFrame[] {
  const times = calculateTimeRegions(config, {
    from: range.from.valueOf(),
    to: range.to.valueOf(),
  });
  return times.length
    ? [
        {
          length: times.length,
          fields: [
            {
              name: "time",
              type: FieldType.time,
              config: {},
              values: times.map((r) => r.from),
            },
            {
              name: "timeEnd",
              type: FieldType.time,
              config: {},
              values: times.map((r) => r.to),
            },
            {
              name: "text",
              type: FieldType.string,
              config: {},
              values: times.map(() => name),
            },
          ],
        },
      ]
    : [];
}

let streamID = 0;
export class LocalGrafana extends DataSourceWithBackend<GrafanaQuery> {
  constructor(settings: DataSourceInstanceSettings) {
    super(settings);
    this.annotations = {
      prepareAnnotation: (annotation) => ({
        ...annotation,
        target: annotation.target ?? {
          type: annotation.type ?? "dashboard",
          limit: annotation.limit ?? 100,
          tags: annotation.tags ?? [],
          matchAny: annotation.matchAny ?? false,
        },
      }),
      prepareQuery: (annotation) => {
        const { filter: _panelFilter, ...rest } = annotation;
        return { ...rest, refId: annotation.name, queryType: "annotations" };
      },
    };
  }
  getDefaultQuery() {
    return { queryType: "randomWalk" };
  }
  query(
    request: DataQueryRequest<GrafanaQuery>,
  ): Observable<DataQueryResponse> {
    const streams = request.targets
      .filter((target) => !target.hide)
      .map((target) =>
        defer(() => {
          const key = target.refId;
          switch (target.queryType || "randomWalk") {
            case "randomWalk":
              return of({
                key,
                data: randomWalkFrames(target, request),
                state: LoadingState.Done,
              });
            case "snapshot": {
              if (
                !Array.isArray(target.snapshot ?? []) ||
                (target.snapshot?.length ?? 0) > 128
              )
                throw new Error("Invalid snapshot frames");
              const frames = (target.snapshot || []).map((frame) =>
                dataFrameFromJSON(structuredClone(frame)),
              );
              if (
                frames.some(
                  (frame) => frame.length > 10000 || frame.fields.length > 1024,
                )
              )
                throw new Error("Snapshot exceeds frame limits");
              // Snapshot refIds belong to the original frames, not this query.
              return of({ key, data: frames, state: LoadingState.Done });
            }
            case "timeRegions":
              return of({
                key,
                data: regionFrame("", target.timeRegion || {}, request.range),
                state: LoadingState.Done,
              });
            case "annotations":
              return new Observable<DataQueryResponse>((subscriber) => {
                const controller = new AbortController();
                void this.getAnnotations(
                  {
                    annotation:
                      target as unknown as AnnotationQuery<GrafanaQuery>,
                    range: request.range,
                    rangeRaw: request.range.raw,
                    dashboard: { uid: request.dashboardUID } as any,
                  },
                  request.scopes,
                  controller.signal,
                )
                  .then((result) => {
                    subscriber.next({
                      ...result,
                      key,
                      state: LoadingState.Done,
                    });
                    subscriber.complete();
                  })
                  .catch((error) => subscriber.error(error));
                return () => controller.abort();
              });
            case "measurements": {
              const channel = getTemplateSrv().replace(
                target.channel || "",
                request.scopedVars,
              );
              const addr = parseLiveChannelAddress(channel);
              if (!isValidLiveChannelAddress(addr))
                throw new Error("Invalid Live channel");
              if (
                target.buffer != null &&
                (!Number.isFinite(target.buffer) || target.buffer < 0)
              )
                throw new Error("Invalid Live buffer");
              const buffer = {
                maxLength: target.buffer
                  ? (request.maxDataPoints || 500) * 2
                  : request.maxDataPoints || 500,
                ...(target.buffer
                  ? {
                      maxDelta: target.buffer,
                    }
                  : request.rangeRaw?.to === "now"
                    ? {
                        maxDelta:
                          request.range.to.valueOf() -
                          request.range.from.valueOf(),
                      }
                    : {}),
              };
              return getGrafanaLiveSrv()
                .getDataStream({
                  key: `${request.requestId}.${++streamID}`,
                  addr: addr!,
                  filter: target.filter,
                  buffer,
                })
                .pipe(
                  map((result) => ({
                    ...result,
                    data: result.data.map((frame) => ({
                      ...frame,
                      refId: target.refId,
                      meta: { ...frame.meta, channel },
                    })),
                  })),
                );
            }
            case "list":
              return super
                .query({ ...request, targets: [target] })
                .pipe(map((result) => ({ ...result, key })));
            default:
              throw new Error(
                `Unknown Grafana query type: ${target.queryType}`,
              );
          }
        }).pipe(
          catchError((error) =>
            of({
              key: target.refId,
              data: [],
              state: LoadingState.Error,
              error: {
                message: error instanceof Error ? error.message : String(error),
                refId: target.refId,
              },
            } as DataQueryResponse),
          ),
        ),
      );
    return streams.length
      ? merge(...streams)
      : of({ data: [], state: LoadingState.Done });
  }
  async getAnnotations(
    options: AnnotationQueryRequest<GrafanaQuery>,
    scopes?: unknown[],
    signal?: AbortSignal,
  ): Promise<DataQueryResponse> {
    const annotation = options.annotation,
      target: GrafanaQuery = annotation.target || {
        refId: annotation.name || "Anno",
        type: annotation.type ?? "dashboard",
        limit: annotation.limit ?? 100,
        tags: annotation.tags ?? [],
        matchAny: annotation.matchAny ?? false,
      };
    if (scopes?.length)
      throw new Error("Grafana annotation scopes are not implemented");
    if (target.queryType === "timeRegions")
      return {
        data: regionFrame(
          annotation.name,
          target.timeRegion || {},
          options.range,
        ),
      };
    const params = new URLSearchParams({
      from: String(options.range.from.valueOf()),
      to: String(options.range.to.valueOf()),
      limit: String(target.limit || 100),
    });
    if (!target.type || target.type === "dashboard") {
      if (!options.dashboard?.uid) return { data: [] };
      params.set("dashboardUID", options.dashboard.uid);
    } else {
      if (!Array.isArray(target.tags) || !target.tags.length)
        return { data: [] };
      for (const tag of target.tags) {
        const value = getTemplateSrv().replace(
          tag,
          {},
          (input: string | string[]) =>
            Array.isArray(input) ? input.join("\u0000") : String(input),
        );
        for (const part of value.split("\u0000")) params.append("tags", part);
      }
      params.set("matchAny", String(Boolean(target.matchAny)));
    }
    const events = await api<any[]>("/api/annotations?" + params, { signal });
    if (!events.length) return { data: [] };
    const fields = [
      ...new Set([
        "time",
        "timeEnd",
        "text",
        "tags",
        "id",
        "dashboardUID",
        "panelId",
        "alertId",
        "newState",
        "prevState",
        "data",
        ...events.flatMap((event) => Object.keys(event)),
      ]),
    ];
    return {
      data: [
        {
          length: events.length,
          fields: fields.map((name) => ({
            name,
            type:
              name === "time" || name === "timeEnd"
                ? FieldType.time
                : [
                      "id",
                      "dashboardId",
                      "panelId",
                      "alertId",
                      "userId",
                      "orgId",
                      "created",
                      "updated",
                    ].includes(name)
                  ? FieldType.number
                  : name === "tags" || name === "data"
                    ? FieldType.other
                    : FieldType.string,
            config: {},
            values: events.map((event) => event[name]),
          })),
        },
      ],
    };
  }
  listFiles(
    path: string,
    maxDataPoints = 500,
  ): Observable<DataFrameView<{ name: string; [key: string]: any }>> {
    return super
      .query({
        targets: [{ refId: "A", queryType: "list", path }],
        maxDataPoints,
      } as DataQueryRequest<GrafanaQuery>)
      .pipe(
        map((result) => {
          if (result.error) throw new Error(result.error.message);
          return new DataFrameView(
            (result.data[0] as DataFrame) || { fields: [], length: 0 },
          );
        }),
      );
  }
  metricFindQuery() {
    return Promise.resolve([]);
  }
  testDatasource() {
    return Promise.resolve({
      status: "success",
      message: "Data source is working",
    });
  }
}
