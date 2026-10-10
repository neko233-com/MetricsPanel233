import {
  FieldType,
  createTheme,
  fieldMatchers,
  getDisplayProcessor,
  getFieldDisplayName,
  toDataFrame,
  LoadingState,
  type DataQueryResponse,
  standardTransformers,
  standardTransformersRegistry,
  transformDataFrame,
  type DataFrame,
  type DataTransformerConfig,
  type FieldConfigSource,
  alignTimeRangeCompareData,
} from "@grafana/data";
import {
  firstValueFrom,
  Observable,
  EMPTY,
  defer,
  from,
  of,
  throwError,
  combineLatest,
  switchMap,
  map,
  catchError,
  repeat,
  startWith,
  take,
  takeUntil,
  filter,
  fromEvent,
  timeout,
  auditTime,
  scan,
  merge,
  ReplaySubject,
  finalize,
  tap,
  exhaustMap,
  share,
} from "rxjs";
import {
  message,
  interpolate,
  type Labels,
  type Panel,
  type QueryResult,
  type InterpolationValues,
} from "../api";
import {
  resolveTimeRange,
  resolvePanelTimeRange,
  panelComparison,
  resolveComparisonRange,
  comparisonRefId,
  type PanelTimeRange,
  type TimeSelection,
} from "./time-range";

import { t } from "../i18n";
import { isExpressionRef, comparisonQueries } from "./expression-ref";
export type VariableValues = Record<string, string | string[]>;
export type GrafanaTarget = {
  [key: string]: unknown;
  expr?: string;
  refId?: string;
  legendFormat?: string;
  hide?: boolean;
  instant?: boolean;
  range?: boolean;
  format?: string;
  timeRangeCompare?: boolean;
  datasource?: string | { type?: string; uid?: string };
};
export type GrafanaConfig = {
  snapshotData?: unknown;
  snapshotTimeRange?: { from: number; to: number; timezone?: string };
  id?: number;
  type: string;
  description?: string;
  timeFrom?: string;
  timeShift?: string;
  hideTimeOverride?: boolean;
  timeCompare?: string;
  compareWith?: string;
  targets?: GrafanaTarget[];
  datasource?: GrafanaTarget["datasource"];
  transformations?: DataTransformerConfig[];
  fieldConfig?: FieldConfigSource;
  gridPos?: { x: number; y: number; w: number; h: number };
  repeat?: string;
  repeatDirection?: string;
  maxPerRow?: number;
  options?: {
    content?: string;
    mode?: string;
    orientation?: string;
    reduceOptions?: {
      calcs?: string[];
      fields?: string;
      values?: boolean;
      limit?: number;
    };
    [key: string]: unknown;
  };
};

// Grafana's public data package exposes the same transformation operators used
// by its panel runtime. Register those operators, keeping unsupported IDs visible.
for (const info of Object.values(standardTransformers)) {
  if (!standardTransformersRegistry.getIfExists(info.id))
    standardTransformersRegistry.register({
      id: info.id,
      name: info.name,
      description: info.description,
      transformation: async () => info,
      defaultOptions: info.defaultOptions,
      editor: () => null,
      imageDark: "",
      imageLight: "",
    });
}
const theme = createTheme({ colors: { mode: "dark" } });
type PromSeries = {
  metric: Labels;
  values?: [number, string][];
  value?: [number, string];
};
type PromResponse = {
  data: { resultType: string; result: PromSeries[] | [number, string] };
};

export function framesFromResponse(
  response: PromResponse,
  target: GrafanaTarget,
  instant: boolean,
): DataFrame[] {
  const refId = target.refId || "A";
  const raw = response.data.result;
  const series: PromSeries[] =
    response.data.resultType === "scalar"
      ? [{ metric: {}, value: raw as [number, string] }]
      : (raw as PromSeries[]);
  if (!Array.isArray(series))
    throw new Error(
      `Unsupported Prometheus result: ${response.data.resultType}`,
    );
  if (target.format === "table") {
    const rows = series.flatMap((s) =>
      (s.values || (s.value ? [s.value] : [])).map((p) => ({
        time: p[0] * 1000,
        value: Number(p[1]),
        labels: s.metric,
      })),
    );
    const labels = [
      ...new Set(rows.flatMap((r) => Object.keys(r.labels))),
    ].sort();
    return [
      {
        refId,
        length: rows.length,
        fields: [
          {
            name: "Time",
            type: FieldType.time,
            config: {},
            values: rows.map((r) => r.time),
          },
          ...labels.map((label) => ({
            name: label,
            type: FieldType.string,
            config: {},
            values: rows.map((r) => r.labels[label] ?? ""),
          })),
          {
            name: `Value #${refId}`,
            type: FieldType.number,
            config: {},
            values: rows.map((r) =>
              Number.isFinite(r.value) ? r.value : null,
            ),
          },
        ],
      },
    ];
  }
  return series.map((s) => {
    const points = s.values || (s.value ? [s.value] : []);
    const name =
      target.legendFormat && target.legendFormat !== "__auto"
        ? target.legendFormat.replace(
            /\{\{\s*(\w+)\s*\}\}/g,
            (_, label: string) => s.metric[label] ?? "",
          )
        : undefined;
    return {
      name,
      refId,
      length: points.length,
      fields: [
        {
          name: "Time",
          type: FieldType.time,
          config: {},
          values: points.map((p) => p[0] * 1000),
        },
        {
          name: "Value",
          type: FieldType.number,
          labels: s.metric,
          config: name ? { displayNameFromDS: name } : {},
          values: points.map((p) =>
            Number.isFinite(Number(p[1])) ? Number(p[1]) : null,
          ),
        },
      ],
      meta: { preferredVisualisationType: instant ? "table" : "graph" },
    };
  });
}

export type FrameUpdate = {
  frames: DataFrame[];
  loading: boolean;
  streaming: boolean;
  error: string;
  timeRange?: PanelTimeRange;
};
type RangedResponse = DataQueryResponse & { panelRange?: PanelTimeRange };
export function watchFrames(
  panel: Panel,
  values: InterpolationValues,
  range: TimeSelection,
  refresh: Observable<unknown>,
  context: { dashboardUID?: string; sampledAt?: number } = {},
): Observable<FrameUpdate> {
  let sampledAt = context.sampledAt ?? Date.now();
  const trigger = refresh.pipe(
    tap(() => {
      sampledAt = Date.now();
    }),
    share(),
  );
  const panelRange = () => {
    const base = resolveTimeRange(range, sampledAt);
    const fixed = { from: base.start, to: base.end, timezone: base.timezone };
    const comparison = panelComparison(panel.config || {});
    return resolvePanelTimeRange(
      range,
      {
        timeFrom: panel.config?.timeFrom
          ? interpolate(panel.config.timeFrom, values, fixed)
          : undefined,
        timeShift: panel.config?.timeShift
          ? interpolate(panel.config.timeShift, values, fixed)
          : undefined,
        hideTimeOverride: panel.config?.hideTimeOverride,
        timeCompare: comparison
          ? interpolate(comparison, values, fixed)
          : undefined,
      },
      sampledAt,
    );
  };
  const targets: GrafanaTarget[] =
    panel.config?.targets ||
    (panel.expressions || [panel.expr || ""]).map((expr, i) => ({
      expr,
      refId: String.fromCharCode(65 + i),
    }));
  if (targets.length > 32)
    return throwError(() => new Error("A panel supports at most 32 queries"));
  const groups = new Map<string, GrafanaTarget[]>();
  const hasExpressions = targets.some((target) =>
    isExpressionRef(target.datasource || panel.config?.datasource),
  );
  targets
    .filter((target) => hasExpressions || !target.hide)
    .forEach((target, i) => {
      const ref = target.datasource || panel.config?.datasource;
      let uid = interpolate(
        typeof ref === "object" ? ref.uid || "" : ref || "",
        values,
        range,
      );
      if (uid === "prometheus") uid = "metricspanel";
      if (!uid && typeof ref === "object" && ref.type === "grafana")
        uid = "grafana";
      if (isExpressionRef(ref)) uid = "__expr__";
      if (uid === "default") uid = "";
      const existing = groups.get(uid) || [];
      existing.push({
        ...target,
        refId: target.refId || String.fromCharCode(65 + i),
        ...(hasExpressions
          ? {
              datasource: {
                uid,
                type: typeof ref === "object" ? ref.type : undefined,
              },
            }
          : {}),
      });
      groups.set(uid, existing);
    });
  const requestGroups = hasExpressions
    ? new Map([["__expr__", Array.from(groups.values()).flat()]])
    : groups;
  const requests = Array.from(requestGroups, ([uid, queries]) => ({
    uid,
    queries,
    compare: false,
  }));
  if (panelComparison(panel.config || {}))
    for (const [uid, queries] of requestGroups) {
      const included = queries.filter(
        (query) => query.timeRangeCompare !== false,
      );
      const references = new Map(
        included.map((query) => [query.refId!, comparisonRefId(query.refId!)]),
      );
      const compared = comparisonQueries(included, references);
      if (compared.length)
        requests.push({ uid, queries: compared, compare: true });
    }
  const streams = requests.map(({ uid, queries, compare }) =>
    defer(async () => {
      const runtime = await import("./plugin-runtime");
      runtime.setPluginVariables(values, range);
      return { datasource: await runtime.getPluginDatasource(uid), runtime };
    }).pipe(
      switchMap(({ datasource, runtime }) => {
        const query = (targets = queries) => {
          const primary = panelRange();
          const resolved = compare
              ? resolveComparisonRange(primary, primary.comparison!)
              : primary,
            { start, end } = resolved;
          // SDK macros must use the exact timestamps of this query, including relative refreshes.
          runtime.setPluginVariables(values, {
            from: start,
            to: end,
            timezone: resolved.timezone,
          });
          return from(
            datasource.query({
              dashboardUID: context.dashboardUID,
              panelId: panel.config?.id,
              requestId: `${panel.id}${compare ? "-compare" : ""}-${Date.now()}`,
              interval: `${Math.max(1, Math.ceil((end - start) / 240000))}s`,
              intervalMs: Math.max(
                1000,
                Math.ceil((end - start) / 240000) * 1000,
              ),
              targets: targets.map((query) => ({
                ...query,
                refId: query.refId!,
                datasource: hasExpressions
                  ? typeof query.datasource === "string"
                    ? { uid: query.datasource }
                    : query.datasource
                  : { uid: datasource.uid, type: datasource.type },
              })),
              range: resolved.sdk,
              rangeRaw: resolved.sdk.raw,
              scopedVars: Object.fromEntries(
                Object.entries({
                  ...values,
                  __from: String(start),
                  __to: String(end),
                }).map(([name, value]) => [name, { text: value, value }]),
              ),
              timezone: resolved.timezone,
              app: "dashboard",
              startTime: Date.now(),
              maxDataPoints: 240,
            }),
          ).pipe(
            map(
              (response) =>
                ({
                  ...response,
                  data: compare
                    ? response.data.map((input) => {
                        const frame = toDataFrame(input);
                        return {
                          ...frame,
                          refId: comparisonRefId(
                            frame.refId ||
                              (targets.length === 1 ? targets[0].refId! : ""),
                          ),
                          meta: {
                            ...frame.meta,
                            timeCompare: {
                              diffMs: resolved.start - primary.start,
                              isTimeShiftQuery: true,
                            },
                          },
                        };
                      })
                    : response.data.map((input) => {
                        const frame = toDataFrame(input);
                        // A direct SDK Live stream can omit RefID; a single target
                        // identifies it unambiguously for query-editor data and refreshes.
                        return !frame.refId && targets.length === 1
                          ? { ...frame, refId: targets[0].refId! }
                          : frame;
                      }),
                  panelRange: primary,
                }) as RangedResponse,
            ),
          );
        };
        const finished = new ReplaySubject<void>(1),
          liveRefs = new Set<string>();
        let live = false;
        return merge(
          defer(() => query()).pipe(finalize(() => finished.next())),
          trigger.pipe(
            filter(
              () =>
                live && queries.some((target) => !liveRefs.has(target.refId!)),
            ),
            exhaustMap(() =>
              defer(() =>
                query(queries.filter((target) => !liveRefs.has(target.refId!))),
              ).pipe(
                catchError((error) =>
                  of({
                    data: [],
                    error: { message: message(error) },
                    state: LoadingState.Error,
                  } as DataQueryResponse),
                ),
              ),
            ),
            takeUntil(finished),
          ),
        ).pipe(
          timeout({ first: 20000 }),
          tap((response) => {
            if (response.state === LoadingState.Streaming) {
              for (const frame of response.data) {
                if (
                  !frame.meta?.channel &&
                  !/^(ds|plugin)\//.test(response.key || "")
                )
                  continue;
                live = true;
                const refId =
                  frame.refId ||
                  (queries.length === 1 ? queries[0].refId : undefined);
                if (refId) liveRefs.add(refId);
              }
            }
          }),
          // DataSourceWithBackend merges separate Live frame observables with
          // its static frames. Keep each response key until this query ends.
          scan((chunks, response) => {
            const key = response.key ? `key:${response.key}` : "__static__";
            if (key.length > 2048 || (!chunks.has(key) && chunks.size >= 128))
              throw new Error("Query response stream key limit exceeded");
            chunks.set(key, response);
            return chunks;
          }, new Map<string, RangedResponse>()),
          map((chunks) => {
            const responses = Array.from(chunks.values());
            return {
              data: responses.flatMap((response) => response.data),
              state: responses.some(
                (response) => response.state === LoadingState.Streaming,
              )
                ? LoadingState.Streaming
                : responses.some(
                      (response) => response.state === LoadingState.Loading,
                    )
                  ? LoadingState.Loading
                  : LoadingState.Done,
              error: responses.find((response) => response.error)?.error,
              panelRange: responses.find((response) => response.panelRange)
                ?.panelRange,
            } as RangedResponse;
          }),
        );
      }),
      catchError((error) =>
        of({
          data: [],
          state: LoadingState.Error,
          error: { message: message(error) },
        } as DataQueryResponse),
      ),
      repeat({ delay: () => trigger.pipe(take(1)) }),
      startWith({ data: [], state: LoadingState.Loading } as DataQueryResponse),
    ),
  );
  if (streams.length === 0)
    return merge(of(undefined), trigger).pipe(
      map(() => ({
        frames: [],
        loading: false,
        streaming: false,
        error: "",
        timeRange: panelRange(),
      })),
      catchError((error) =>
        of({
          frames: [],
          loading: false,
          streaming: false,
          error: message(error),
        }),
      ),
    );
  return combineLatest(streams).pipe(
    auditTime(50),
    switchMap((responses) => {
      const resolved = responses.reduce<PanelTimeRange | undefined>(
        (latest, response) => {
          const candidate = (response as RangedResponse).panelRange;
          return candidate &&
            (!latest || candidate.sampledAt > latest.sampledAt)
            ? candidate
            : latest;
        },
        undefined,
      );
      const effective = resolved
        ? {
            from: resolved.start,
            to: resolved.end,
            timezone: resolved.timezone,
          }
        : range;
      return from(
        decorateFrames(
          panel,
          values,
          effective,
          responses.flatMap((response) =>
            response.data.map((frame) => toDataFrame(frame)),
          ),
        ),
      ).pipe(
        map((frames) => ({
          frames,
          timeRange: resolved,
          loading: responses.some(
            (response) => response.state === LoadingState.Loading,
          ),
          streaming: responses.some(
            (response) => response.state === LoadingState.Streaming,
          ),
          error: responses
            .map((response) => response.error?.message || "")
            .filter(Boolean)
            .join("; "),
        })),
      );
    }),
  );
}
export async function queryFrames(
  panel: Panel,
  values: InterpolationValues,
  range: TimeSelection,
  signal: AbortSignal,
): Promise<DataFrame[]> {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  return firstValueFrom(
    watchFrames(panel, values, range, EMPTY).pipe(
      takeUntil(fromEvent(signal, "abort")),
      filter((update) => !update.loading),
      map((update) => {
        if (update.error) throw new Error(update.error);
        return update.frames;
      }),
    ),
  );
}
async function decorateFrames(
  panel: Panel,
  values: InterpolationValues,
  range: TimeSelection,
  input: DataFrame[],
): Promise<DataFrame[]> {
  const config = panel.config;
  const transformations = config?.transformations || [];
  for (const transform of transformations)
    if (
      !transform.disabled &&
      !standardTransformersRegistry.getIfExists(transform.id)
    )
      throw new Error(`Unsupported transformation: ${transform.id}`);
  const replace = (text: string) => interpolate(text, values, range);
  const transformed = await firstValueFrom(
    transformDataFrame(transformations, input, {
      interpolate: replace,
    }),
  );

  const source = config?.fieldConfig || {
    defaults: { unit: panel.unit === "seconds" ? "s" : panel.unit },
    overrides: [],
  };
  const timeZone = resolveTimeRange(range).timezone;
  return transformed.map((frame) => ({
    ...frame,
    fields: frame.fields.map((field) => {
      const resolved = { ...source.defaults, ...field.config };
      for (const override of source.overrides || []) {
        const matcher = fieldMatchers.getIfExists(override.matcher.id);
        if (!matcher)
          throw new Error(`Unsupported field matcher: ${override.matcher.id}`);
        if (matcher.get(override.matcher.options)(field, frame, transformed)) {
          for (const property of override.properties) {
            if (property.id.startsWith("custom."))
              resolved.custom = {
                ...resolved.custom,
                [property.id.slice(7)]: property.value,
              };
            else Object.assign(resolved, { [property.id]: property.value });
          }
        }
      }
      if (resolved.displayName)
        resolved.displayName = replace(resolved.displayName);
      const decorated = { ...field, config: resolved };
      return {
        ...decorated,
        display: getDisplayProcessor({
          field: decorated,
          theme,
          timeZone,
        }),
      };
    }),
  }));
}

export function framesAsSeries(
  frames: DataFrame[],
  range: TimeSelection,
): QueryResult {
  const { start, end } = resolveTimeRange(range);
  const series = frames.flatMap((frame) => {
    const compared = frame.meta?.timeCompare?.isTimeShiftQuery;
    const aligned = compared
      ? alignTimeRangeCompareData(frame, frame.meta!.timeCompare!.diffMs, theme)
      : frame;
    const time = aligned.fields.find((f) => f.type === FieldType.time);
    if (!time) return [];
    return aligned.fields
      .filter((f) => f.type === FieldType.number)
      .map((field) => ({
        labels: {
          series: compared
            ? getFieldDisplayName(field, frame, frames).replace(
                / \(comparison\)$/,
                ` (${t("Comparison")})`,
              )
            : getFieldDisplayName(field, frame, frames),
        },
        comparison: Boolean(compared),
        points: field.values.flatMap((value, i) =>
          typeof value === "number" &&
          Number.isFinite(value) &&
          Number.isFinite(Number(time.values[i]))
            ? [{ timestamp: Number(time.values[i]), value }]
            : [],
        ),
      }));
  });
  return {
    metric: "PromQL",
    aggregation: "PromQL",
    start,
    end,
    step: 0,
    series,
  };
}
