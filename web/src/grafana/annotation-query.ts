import {
  CoreApp,
  FieldType,
  LoadingState,
  DataTransformerID,
  getFieldDisplayName,
  rangeUtil,
  standardTransformers,
  toDataFrame,
  type AnnotationEvent,
  type AnnotationEventMappings,
  type AnnotationQuery,
  type DataFrame,
  type Field,
  type DataSourceApi,
  type ScopedVars,
  type TimeRange,
} from "@grafana/data";
import {
  defer,
  from,
  of,
  filter,
  map,
  switchMap,
  timeout,
  Observable,
} from "rxjs";

export type AnnotationQueryResult = {
  events: AnnotationEvent[];
  error?: string;
};
export type AnnotationQueryContext = {
  range: TimeRange;
  timezone: string;
  dashboard: Record<string, unknown>;
  variables: Record<string, string | string[] | { raw: string }>;
  width: number;
  deadlineMs?: number;
};
let requestNumber = 0;

// Resolve the SDK field/text/skip mappings independently of the datasource's query language.
export function annotationEventsFromFrames(
  frames: DataFrame[],
  mappings: AnnotationEventMappings = {},
) {
  if (frames.reduce((count, frame) => count + frame.length, 0) > 10000)
    throw new Error("At most 10000 annotation frame rows");
  const source =
    frames.length > 1
      ? of(frames).pipe(
          Object.values(standardTransformers)
            .find((t) => t.id === DataTransformerID.merge)!
            .operator({}, { interpolate: (value) => value }),
        )
      : of(frames);
  return source.pipe(
    map((data) => {
      const frame = data[0];
      if (!frame?.length) return [];
      const names = new Map(
        frame.fields.map((field) => [
          getFieldDisplayName(field, frame).toLowerCase(),
          field,
        ]),
      );
      const keys = [
        "time",
        "timeEnd",
        "title",
        "text",
        "tags",
        "id",
        "userId",
        "login",
        "email",
        "prevState",
        "newState",
        "data",
        "panelId",
        "alertId",
        "dashboardId",
        "dashboardUID",
      ];
      const fields = keys.flatMap<{
        key: string;
        constant?: string;
        field?: Field;
      }>((key) => {
        const mapping =
          (mappings as Record<string, { source?: string; value?: string }>)[
            key
          ] || {};
        if (mapping.source === "skip") return [];
        if (mapping.source === "text")
          return mapping.value ? [{ key, constant: mapping.value }] : [];
        const field =
          names.get((mapping.value || key).toLowerCase()) ||
          (key === "time"
            ? frame.fields.find((field) => field.type === FieldType.time)
            : key === "text"
              ? frame.fields.find((field) => field.type === FieldType.string)
              : undefined);
        return field ? [{ key, field }] : [];
      });
      if (
        !fields.some((field) => field.key === "time") ||
        !fields.some((field) => field.key === "text")
      )
        throw new Error("Annotation results require time and text fields");
      if (frame.length > 1000)
        throw new Error("At most 1000 annotation events per query");
      return Array.from({ length: frame.length }, (_, row) => {
        const event: Record<string, unknown> = {};
        for (const entry of fields) {
          const value =
            entry.constant !== undefined
              ? entry.constant
              : entry.field?.values[row];
          if (value != null)
            event[entry.key] =
              entry.key === "tags" && typeof value === "string"
                ? value.split(",")
                : value;
        }
        return event as AnnotationEvent;
      });
    }),
  );
}

// Both SDK generations are executed as observables so a panel lifecycle owns subscriptions.
export function runDatasourceAnnotationQuery(
  datasource: DataSourceApi,
  saved: AnnotationQuery,
  context: AnnotationQueryContext,
): Observable<AnnotationQueryResult> {
  return defer(() => {
    const annotation = structuredClone(saved);
    const interval = rangeUtil.calculateInterval(
      context.range,
      Math.max(1, Math.min(context.width, 10000)),
      datasource.interval,
    );
    const scopedVars: ScopedVars = Object.fromEntries(
      Object.entries({
        ...context.variables,
        __from: context.range.from.valueOf(),
        __to: context.range.to.valueOf(),
        __range_ms: context.range.to.valueOf() - context.range.from.valueOf(),
        __range_s:
          (context.range.to.valueOf() - context.range.from.valueOf()) / 1000,
        __interval: interval.interval,
        __interval_ms: interval.intervalMs,
        __annotation: annotation,
      }).map(([name, value]) => [name, { value, text: value }]),
    );
    if (
      datasource.annotationQuery &&
      (!datasource.annotations ||
        ["loki", "elasticsearch", "grafana-opensearch-datasource"].includes(
          datasource.type,
        ))
    ) {
      return new Observable<AnnotationQueryResult>((subscriber) => {
        const controller = new AbortController();
        const options = {
          range: context.range,
          rangeRaw: context.range.raw,
          dashboard: context.dashboard,
          annotation,
          scopedVars,
          signal: controller.signal,
        };
        Promise.resolve()
          .then(() => datasource.annotationQuery!(options))
          .then((events) => {
            if (subscriber.closed) return;
            if (events.length > 1000)
              throw new Error("At most 1000 annotation events per query");
            subscriber.next({ events });
            subscriber.complete();
          })
          .catch((error) => subscriber.error(error));
        return () => controller.abort();
      });
    }
    const processor = {
      prepareAnnotation: (stored: AnnotationQuery) =>
        typeof stored.query === "string"
          ? {
              ...stored,
              target: { refId: "annotation_query", query: stored.query },
              mappings: {},
            }
          : stored,
      prepareQuery: (stored: AnnotationQuery) => stored.target,
      processEvents: (stored: AnnotationQuery, frames: DataFrame[]) =>
        annotationEventsFromFrames(frames, stored.mappings),
      ...datasource.annotations,
    };
    const prepared = processor.prepareAnnotation({
      ...processor.getDefaultQuery?.(),
      ...annotation,
    });
    if (!prepared) return of({ events: [] });
    const target = processor.prepareQuery(prepared);
    if (!target) return of({ events: [] });
    scopedVars.__annotation = { value: prepared, text: prepared.name };
    const request = {
      startTime: Date.now(),
      requestId: `AQ${++requestNumber}`,
      app: CoreApp.Dashboard,
      range: context.range,
      rangeRaw: context.range.raw,
      timezone: context.timezone,
      maxDataPoints: Math.max(1, Math.min(context.width, 10000)),
      scopedVars,
      ...interval,
      targets: [
        {
          ...target,
          refId: "Anno",
          datasource: { uid: datasource.uid, type: datasource.type },
        },
      ],
    };
    return from(datasource.query(request)).pipe(
      filter((response) => response.state !== LoadingState.Loading),
      switchMap((response) => {
        const frames = (response.data || []).map((frame) => toDataFrame(frame));
        const error =
          response.error?.message ||
          response.errors?.map((error) => error.message).join("; ");
        if (!frames.length) return of({ events: [], error });
        if (frames.reduce((count, frame) => count + frame.length, 0) > 10000)
          throw new Error("At most 10000 annotation frame rows");
        return processor.processEvents(prepared, frames).pipe(
          map((events) => {
            if ((events?.length || 0) > 1000)
              throw new Error("At most 1000 annotation events per query");
            return { events: events || [], error };
          }),
        );
      }),
    );
  }).pipe(
    timeout({
      first: context.deadlineMs || 60000,
      each: context.deadlineMs || 60000,
    }),
  );
}
