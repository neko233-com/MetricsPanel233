import { FieldType, isDataFrame, type DataQueryResponse } from "@grafana/data";
import { isExpressionRef } from "./expression-ref";

export function restoreSQLDisplayNames(
  response: DataQueryResponse,
  targets: Array<{
    refId: string;
    type?: string;
    datasource?: string | { uid?: string; type?: string } | null;
  }>,
): DataQueryResponse {
  const sql = new Set(
    targets
      .filter(
        (query) => query.type === "sql" && isExpressionRef(query.datasource),
      )
      .map((query) => query.refId),
  );
  if (!sql.size) return response;
  const refs = new Set(targets.map((query) => query.refId));
  return {
    ...response,
    data: response.data.map((frame) => {
      if (!isDataFrame(frame) || !frame.refId) return frame;
      const fullLong = ["numeric-full-long", "timeseries-full-long"].includes(
        frame.meta?.type || "",
      );
      if (!sql.has(frame.refId) && !(fullLong && refs.has(frame.refId)))
        return frame;
      const names = frame.fields.filter(
        (field) => field.name === "__display_name__",
      );
      const values = frame.fields.filter(
        (field) =>
          field.name === "__value__" && field.type === FieldType.number,
      );
      if (names.length !== 1 || values.length !== 1) return frame;
      const alias = names[0].values[0];
      // A field alias describes one series. Interleaved series with different
      // display names must retain their per-row names until a transformation
      // partitions the data, matching the pinned Grafana SQL contract.
      if (
        typeof alias !== "string" ||
        !alias ||
        !names[0].values.every((value) => value === alias)
      )
        return frame;
      return {
        ...frame,
        fields: frame.fields.map((field) =>
          field === values[0]
            ? {
                ...field,
                config: { ...field.config, displayNameFromDS: alias },
                state: null,
              }
            : field,
        ),
      };
    }),
  };
}
