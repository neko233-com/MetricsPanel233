import {
  type DataQuery,
  type DataQueryRequest,
  type ScopedVars,
  type DataSourceInstanceSettings,
} from "@grafana/data";
import {
  DataSourceWithBackend,
  getDataSourceSrv,
  getTemplateSrv,
} from "@grafana/runtime";
import { defer, switchMap, map } from "rxjs";
import { grafanaMeta } from "./grafana-meta";
import { expressionRef, isExpressionRef } from "./expression-ref";
import { restoreSQLDisplayNames } from "./sql-frames";
import { ExpressionQueryEditor } from "./ExpressionQueryEditor";

export type ExpressionQuery = DataQuery & {
  [key: string]: any;
  type?: string;
  expression?: string;
  window?: string;
  reducer?: string;
  downsampler?: string;
  upsampler?: string;
  format?: string;
  invert?: boolean;
  settings?: { [key: string]: any; mode?: string; replaceWithValue?: number };
  conditions?: Array<{
    [key: string]: any;
    evaluator?: { [key: string]: any; type?: string; params?: number[] };
    unloadEvaluator?: { [key: string]: any; type?: string; params?: number[] };
    query?: { [key: string]: any; params?: string[] };
    reducer?: { [key: string]: any; type?: string };
    operator?: { [key: string]: any; type?: string };
  }>;
};
export const expressionSettings: DataSourceInstanceSettings = {
  id: -100,
  uid: expressionRef.uid,
  name: expressionRef.name,
  type: expressionRef.type,
  access: "proxy",
  readOnly: true,
  jsonData: {},
  meta: {
    ...grafanaMeta,
    id: expressionRef.type,
    name: "Expression",
    metrics: false,
    annotations: false,
  },
};
export class ExpressionSource extends DataSourceWithBackend<ExpressionQuery> {
  constructor(public instanceSettings: DataSourceInstanceSettings) {
    super(instanceSettings);
    this.components = { QueryEditor: ExpressionQueryEditor };
  }
  getDefaultQuery() {
    return { type: "math", expression: "", datasource: expressionRef };
  }
  newQuery(query?: Partial<ExpressionQuery>): ExpressionQuery {
    return { refId: "--", datasource: expressionRef, type: "math", ...query };
  }
  getCollapsedText(query: ExpressionQuery) {
    return "Expression: " + query.type;
  }
  applyTemplateVariables(query: ExpressionQuery, scoped?: ScopedVars) {
    return {
      ...query,
      expression: getTemplateSrv().replace(query.expression || "", scoped),
      window: getTemplateSrv().replace(query.window || "", scoped),
    };
  }
  query(request: DataQueryRequest<ExpressionQuery>) {
    const protectedRefs: ScopedVars = { ...request.scopedVars };
    for (const query of request.targets)
      protectedRefs[query.refId] = { value: "$" + "{" + query.refId + "}" };
    return defer(async () =>
      Promise.all(
        request.targets.map(async (query) => {
          if (isExpressionRef(query.datasource)) {
            return {
              ...this.applyTemplateVariables(
                query,
                query.type === "sql" ? request.scopedVars : protectedRefs,
              ),
              datasource: expressionRef,
            };
          }
          const datasource = await getDataSourceSrv().get(
            query.datasource,
            request.scopedVars,
          );
          const prepared =
            datasource.interpolateVariablesInQueries?.(
              [query],
              request.scopedVars,
            )[0] || query;
          return {
            ...prepared,
            datasource: { uid: datasource.uid, type: datasource.type },
          };
        }),
      ),
    ).pipe(
      switchMap((targets) => super.query({ ...request, targets })),
      map((response) => ({
        ...response,
        data: restoreSQLDisplayNames(response, request.targets).data.filter(
          (frame) =>
            !request.targets.find((query) => query.refId === frame.refId)?.hide,
        ),
      })),
    );
  }
}
