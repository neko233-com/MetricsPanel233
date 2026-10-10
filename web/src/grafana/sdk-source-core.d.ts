declare module "metricspanel/sdk-source-settings" {
  import type { DataSourceInstanceSettings } from "@grafana/data";
  export function syncDataSourceInstanceSettings(settings: {
    datasources: Record<string, DataSourceInstanceSettings>;
    defaultDatasource: string;
  }): void;
}
declare module "metricspanel/sdk-source-loader" {
  import type {
    DataSourceApi,
    DataSourcePlugin,
    PluginMeta,
  } from "@grafana/data";
  export function setDataSourcePluginImporter(
    load: (meta: PluginMeta) => Promise<DataSourcePlugin<DataSourceApi>>,
  ): void;
}
declare module "metricspanel/sdk-expression-source" {
  import type { DataSourceApi, DataQuery } from "@grafana/data";
  export function setExpressionDataSourceInstance<TQuery extends DataQuery>(
    source: DataSourceApi<TQuery>,
  ): void;
}
