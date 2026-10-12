export const POSTGRES_DATASOURCE = "grafana-postgresql-datasource";

export function isSQLDatasource(type: string) {
  return (
    type === "mysql" || type === "postgres" || type === POSTGRES_DATASOURCE
  );
}
