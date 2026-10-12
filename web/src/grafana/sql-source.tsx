import {
  dateTime,
  AnnotationEventFieldSource,
  type AnnotationQuery,
  type DataQuery,
  type DataSourceApi,
  type DataSourceInstanceSettings,
  type QueryEditorProps,
  type ScopedVars,
  type TimeRange,
} from "@grafana/data";
import { DataSourceWithBackend, getTemplateSrv } from "@grafana/runtime";
import { getLocale } from "../i18n";
import { interpolateSQLVariables, type SQLVariable } from "./sql-variables";
import { grafanaMeta } from "./grafana-meta";
import { sqlVariableResults } from "./sql-variable-results";
import { annotationEventsFromFrames } from "./annotation-query";
import { prepareSQLAnnotation, sqlAnnotationFrames } from "./sql-annotations";
import { firstValueFrom, fromEvent, map, takeUntil } from "rxjs";

export type SQLQuery = DataQuery & {
  rawSql?: string;
  format?: "table" | "time_series";
};
const text = (en: string, zh: string) => (getLocale() === "zh" ? zh : en);

export function sqlSourceMeta(type: string) {
  return {
    ...grafanaMeta,
    id: type,
    name: type === "mysql" ? "MySQL" : "PostgreSQL",
    builtIn: true,
    backend: true,
    metrics: true,
    alerting: true,
    annotations: true,
    info: { ...grafanaMeta.info, description: "SQL database queries" },
  };
}

export class LocalSQL extends DataSourceWithBackend<SQLQuery> {
  constructor(settings: DataSourceInstanceSettings) {
    super(settings);
    this.components = { QueryEditor: SQLQueryEditor };
    this.annotations = {
      QueryEditor: SQLAnnotationQueryEditor,
      getDefaultQuery: () => ({
        refId: "Anno",
        rawSql: "SELECT $__unixEpochFrom() AS time, 'Event' AS text",
        format: "table",
      }),
      prepareAnnotation: prepareSQLAnnotation,
      prepareQuery: (annotation) => ({
        ...annotation.target,
        refId: annotation.target?.refId || "Anno",
        format: "table",
      }),
      processEvents: (annotation, frames) => {
        const prepared = sqlAnnotationFrames(frames, annotation.mappings);
        return annotationEventsFromFrames(
          prepared.frames,
          prepared.mappings,
        ).pipe(
          map((events) =>
            events
              .filter(
                (event) =>
                  Number.isSafeInteger(event.time) &&
                  event.time! > 0 &&
                  (event.timeEnd == null ||
                    (Number.isSafeInteger(event.timeEnd) &&
                      event.timeEnd >= event.time!)),
              )
              .map((event) => ({
                ...event,
                text: String(event.text ?? ""),
                tags: Array.isArray(event.tags)
                  ? event.tags
                      .filter((tag) => typeof tag === "string")
                      .map((tag) => tag.trim())
                      .filter(Boolean)
                  : [],
              })),
          ),
        );
      },
    };
  }
  getDefaultQuery(): Partial<SQLQuery> {
    return { rawSql: "SELECT 1 AS value", format: "table" as const };
  }
  applyTemplateVariables(query: SQLQuery, scoped?: ScopedVars): SQLQuery {
    const variables: Record<string, SQLVariable> = {};
    for (const variable of getTemplateSrv().getVariables()) {
      const model = variable as typeof variable & {
        multi?: boolean;
        includeAll?: boolean;
        current?: { value?: unknown };
      };
      if (model.current?.value !== undefined)
        variables[model.name] = {
          value: model.current.value,
          multi: model.multi,
          includeAll: model.includeAll,
        };
    }
    for (const [name, value] of Object.entries(scoped || {})) {
      if (value) variables[name] = { ...variables[name], value: value.value };
    }
    return {
      ...query,
      rawSql: interpolateSQLVariables(query.rawSql || "", variables, this.type),
    };
  }
  interpolateVariablesInQueries(queries: SQLQuery[], scoped?: ScopedVars) {
    return queries.map((query) => this.applyTemplateVariables(query, scoped));
  }
  async metricFindQuery(
    query: string | Partial<SQLQuery>,
    options?: {
      range?: TimeRange;
      scopedVars?: ScopedVars;
      signal?: AbortSignal;
    },
  ) {
    options?.signal?.throwIfAborted();
    const range = options?.range || {
      from: dateTime(Date.now() - 21600000),
      to: dateTime(),
      raw: { from: "now-6h", to: "now" },
    };
    const request = this.query({
      requestId: `sql-variable-${this.uid}-${Date.now()}`,
      timezone: "browser",
      app: "dashboard",
      startTime: Date.now(),
      range,
      rangeRaw: range.raw,
      interval: "1s",
      intervalMs: 1000,
      maxDataPoints: 1000,
      scopedVars: options?.scopedVars || {},
      targets: [
        {
          ...(typeof query === "string" ? { rawSql: query } : query),
          refId: "VariableQuery",
          datasource: { uid: this.uid, type: this.type },
          format: "table",
        },
      ],
    });
    const response = await firstValueFrom(
      options?.signal
        ? request.pipe(takeUntil(fromEvent(options.signal, "abort")))
        : request,
    ).catch((error) => {
      options?.signal?.throwIfAborted();
      throw error;
    });
    options?.signal?.throwIfAborted();
    if (response.error)
      throw new Error(response.error.message || "SQL variable query failed");
    return sqlVariableResults(response.data);
  }
}

export function SQLQueryEditor({
  query,
  onChange,
  onRunQuery,
  datasource,
  annotationMode = false,
}: QueryEditorProps<DataSourceApi<SQLQuery>, SQLQuery> & {
  annotationMode?: boolean;
}) {
  const mysql = datasource.type === "mysql";
  return (
    <div className="sql-query-editor">
      <label>
        {text("SQL query", "SQL 查询")}
        <textarea
          aria-label={text("SQL query", "SQL 查询")}
          className="mono"
          rows={7}
          maxLength={131072}
          value={query.rawSql || ""}
          placeholder="SELECT 1 AS value"
          onChange={(event) =>
            onChange({ ...query, rawSql: event.target.value })
          }
          onKeyDown={(event) => {
            if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
              event.preventDefault();
              onRunQuery();
            }
          }}
        />
      </label>
      {!annotationMode && (
        <label>
          {text("Result format", "结果格式")}
          <select
            aria-label={text("Result format", "结果格式")}
            value={query.format || "time_series"}
            onChange={(event) =>
              onChange({
                ...query,
                format: event.target.value as SQLQuery["format"],
              })
            }
          >
            <option value="table">{text("Table", "表格")}</option>
            <option value="time_series">{text("Time series", "时序")}</option>
          </select>
        </label>
      )}
      {annotationMode && (
        <p className="subtle">
          {text(
            "Return time (SQL datetime or UNIX epoch seconds). Optional columns: timeend, text, tags (comma-separated). Queries run as tables using the dashboard time range and variables.",
            "返回 time 列（SQL 日期时间或 UNIX 秒时间戳）；可选 timeend、text、tags（逗号分隔）。注释查询使用表格结果，并支持仪表盘时间范围与变量。",
          )}
        </p>
      )}
      <details className="sql-query-help">
        <summary>{text("SQL macros and variables", "SQL 宏与变量")}</summary>
        <p>
          {text(
            "Time series requires a time column and numeric values, ordered by time. Ctrl + Enter runs the query.",
            "时序结果需要 time 列和数值列，并按时间排序。Ctrl + Enter 执行查询。",
          )}
        </p>
        <pre className="mono">{`SELECT $__time(created_at), value\nFROM measurements\nWHERE $__timeFilter(created_at)\nORDER BY created_at`}</pre>
        <p>
          <code>$__timeFrom()</code> / <code>$__timeTo()</code> ·{" "}
          <code>$__timeGroupAlias(created_at, '$__interval')</code> ·{" "}
          <code>$__unixEpochFilter(epoch)</code>
        </p>
        <p>
          {text(
            "Dashboard variables support ${name:sqlstring} for quoted SQL values and ${name:csv} for unquoted lists. Multi-value variables are quoted by default.",
            "仪表盘变量可使用 ${name:sqlstring} 生成带引号的 SQL 值，${name:csv} 生成不带引号的列表。多值变量默认添加引号。",
          )}
        </p>
        <a
          href={`https://grafana.com/docs/grafana/latest/datasources/${mysql ? "mysql" : "postgres"}/${annotationMode ? "annotations" : "query-editor"}/`}
          target="_blank"
          rel="noreferrer"
        >
          {text("Grafana SQL query reference", "Grafana SQL 查询参考")}
        </a>
      </details>
    </div>
  );
}

export function SQLAnnotationQueryEditor(
  props: QueryEditorProps<DataSourceApi<SQLQuery>, SQLQuery> & {
    annotation?: AnnotationQuery;
    onAnnotationChange?: (annotation: AnnotationQuery) => void;
  },
) {
  return (
    <>
      <SQLQueryEditor
        {...props}
        annotationMode
        query={{ ...props.query, format: "table" }}
        onChange={(query) => props.onChange({ ...query, format: "table" })}
      />
      {props.annotation && props.onAnnotationChange && (
        <details className="annotation-mappings">
          <summary>{text("Event field mappings", "事件字段映射")}</summary>
          {(["time", "timeEnd", "text", "tags", "id"] as const).map((key) => {
            const mapping = props.annotation!.mappings?.[key] || {};
            const change = (patch: typeof mapping) =>
              props.onAnnotationChange!({
                ...props.annotation!,
                mappings: {
                  ...props.annotation!.mappings,
                  [key]: { ...mapping, ...patch },
                },
              });
            return (
              <div className="annotation-mapping-row" key={key}>
                <label>
                  {key}
                  <select
                    aria-label={`${text("Mapping source", "映射来源")} ${key}`}
                    value={mapping.source || "field"}
                    onChange={(event) =>
                      change({
                        source: event.target
                          .value as AnnotationEventFieldSource,
                      })
                    }
                  >
                    <option value="field">{text("Field", "字段")}</option>
                    {key !== "time" && key !== "timeEnd" && (
                      <option value="text">
                        {text("Constant text", "固定文字")}
                      </option>
                    )}
                    {key !== "time" && (
                      <option value="skip">{text("Skip", "跳过")}</option>
                    )}
                  </select>
                </label>
                <label>
                  {text("Mapping value", "映射值")}
                  <input
                    aria-label={`${text("Mapping value", "映射值")} ${key}`}
                    value={mapping.value || ""}
                    disabled={mapping.source === "skip"}
                    placeholder={key}
                    onChange={(event) => change({ value: event.target.value })}
                  />
                </label>
              </div>
            );
          })}
        </details>
      )}
    </>
  );
}
