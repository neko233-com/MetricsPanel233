import {
  FieldType,
  createTheme,
  fieldMatchers,
  getDisplayProcessor,
  getFieldDisplayName,
  dateTime,
  toDataFrame,
  standardTransformers,
  standardTransformersRegistry,
  transformDataFrame,
  type DataFrame,
  type DataTransformerConfig,
  type FieldConfigSource,
} from "@grafana/data";
import { firstValueFrom } from "rxjs";
import {
  api,
  interpolate,
  rangeMilliseconds,
  type Labels,
  type Panel,
  type QueryResult,
  type InterpolationValues,
} from "../api";

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
  datasource?: string | { type?: string; uid?: string };
};
export type GrafanaConfig = {
  type: string;
  description?: string;
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

export async function queryFrames(
  panel: Panel,
  values: InterpolationValues,
  range: string,
  signal: AbortSignal,
): Promise<DataFrame[]> {
  const config = panel.config;
  const targets: GrafanaTarget[] =
    config?.targets ||
    (panel.expressions || [panel.expr || ""]).map((expr, i) => ({
      expr,
      refId: String.fromCharCode(65 + i),
    }));
  const end = Date.now() / 1000,
    start = end - rangeMilliseconds(range) / 1000;
  const responses = await Promise.all(
    targets
      .filter((t) => !t.hide)
      .map(async (target, i) => {
        const source = target.datasource || config?.datasource;
        const uid = interpolate(
          typeof source === "object" ? source.uid || "" : source || "",
          values,
          range,
        );
        if (
          uid &&
          uid !== "metricspanel" &&
          uid !== "default" &&
          uid !== "prometheus" &&
          uid !== "-- Mixed --"
        ) {
          const { getPluginDatasource, setPluginVariables } = await import(
            "./plugin-runtime"
          );
          setPluginVariables(values, range);
          const datasource = await getPluginDatasource(uid);
          const { firstValueFrom, from, fromEvent, takeUntil, timeout } =
            await import("rxjs");
          if (signal.aborted) throw new DOMException("Aborted", "AbortError");
          const response = await firstValueFrom(
            from(
              datasource.query({
                requestId: `${panel.id}-${i}-${Date.now()}`,
                interval: `${Math.max(1, Math.ceil((end - start) / 240))}s`,
                intervalMs: Math.max(
                  1000,
                  Math.ceil((end - start) / 240) * 1000,
                ),
                targets: [
                  {
                    ...target,
                    refId: target.refId || String.fromCharCode(65 + i),
                    datasource: { uid: datasource.uid, type: datasource.type },
                  },
                ],
                range: {
                  from: dateTime(start * 1000),
                  to: dateTime(end * 1000),
                  raw: { from: "now-" + range, to: "now" },
                },
                scopedVars: Object.fromEntries(
                  Object.entries(values).map(([name, value]) => [
                    name,
                    { text: value, value },
                  ]),
                ),
                timezone: "browser",
                app: "dashboard",
                startTime: Date.now(),
                maxDataPoints: 240,
              }),
            ).pipe(takeUntil(fromEvent(signal, "abort")), timeout(20000)),
          );
          if (response.error)
            throw new Error(
              response.error.message || "Datasource query failed",
            );
          return response.data.map((frame) => toDataFrame(frame));
        }
        if (!target.expr)
          throw new Error(
            `Query ${target.refId || i + 1} has no PromQL expression`,
          );
        const datasource =
          typeof source === "object"
            ? source.type
            : source === "__expr__"
              ? source
              : undefined;
        if (
          datasource &&
          datasource !== "prometheus" &&
          !datasource.startsWith("$") &&
          datasource !== "default"
        ) {
          throw new Error(`Datasource ${datasource} needs an adapter`);
        }
        const expr = interpolate(target.expr, values, range),
          instant = target.instant === true && target.range !== true;
        const params = new URLSearchParams({ query: expr });
        if (instant) params.set("time", String(end));
        else {
          params.set("start", String(start));
          params.set("end", String(end));
          params.set(
            "step",
            String(Math.max(1, Math.ceil((end - start) / 240))),
          );
        }
        const result = await api<PromResponse>(
          `/prometheus/api/v1/${instant ? "query" : "query_range"}?${params}`,
          { signal },
        );
        return framesFromResponse(
          result,
          {
            ...target,
            refId: target.refId || String.fromCharCode(65 + i),
            legendFormat: target.legendFormat
              ? interpolate(target.legendFormat, values, range)
              : undefined,
          },
          instant,
        );
      }),
  );
  const transformations = config?.transformations || [];
  for (const transform of transformations)
    if (
      !transform.disabled &&
      !standardTransformersRegistry.getIfExists(transform.id)
    )
      throw new Error(`Unsupported transformation: ${transform.id}`);
  const replace = (text: string) => interpolate(text, values, range);
  const transformed = await firstValueFrom(
    transformDataFrame(transformations, responses.flat(), {
      interpolate: replace,
    }),
  );
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  const source = config?.fieldConfig || {
    defaults: { unit: panel.unit === "seconds" ? "s" : panel.unit },
    overrides: [],
  };
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
        display: getDisplayProcessor({ field: decorated, theme }),
      };
    }),
  }));
}

export function framesAsSeries(
  frames: DataFrame[],
  range: string,
): QueryResult {
  const end = Date.now(),
    start = end - rangeMilliseconds(range);
  const series = frames.flatMap((frame) => {
    const time = frame.fields.find((f) => f.type === FieldType.time);
    if (!time) return [];
    return frame.fields
      .filter((f) => f.type === FieldType.number)
      .map((field) => ({
        labels: { series: getFieldDisplayName(field, frame, frames) },
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
