import type {
  AnnotationQuery,
  AnnotationEventMappings,
  DataQuery,
  DataFrame,
  Field,
} from "@grafana/data";
type SQLAnnotationTarget = DataQuery & {
  rawSql?: string;
  format?: "table" | "time_series";
};

// SQL result timestamps use epoch seconds unless the backend marks a datetime field.
function annotationTime(value: unknown, field: Field) {
  if (value == null || value === "") return null;
  if (value instanceof Date) return value.valueOf();
  const number = typeof value === "number" ? value : Number(value);
  if (Number.isFinite(number)) {
    const time = field.type === "time" ? number : Math.round(number * 1000);
    return Number.isSafeInteger(time) ? time : null;
  }
  if (typeof value === "string") {
    const time = Date.parse(value);
    return Number.isFinite(time) ? time : null;
  }
  return null;
}

export function prepareSQLAnnotation(annotation: AnnotationQuery) {
  const target: { refId: string; [key: string]: unknown } = {
    refId: "Anno",
    ...annotation.target,
  };
  if (typeof annotation.rawQuery === "string")
    target.rawSql = annotation.rawQuery;
  else if (!target.rawSql && typeof annotation.rawSql === "string")
    target.rawSql = annotation.rawSql;
  const prepared: AnnotationQuery<SQLAnnotationTarget> = {
    ...annotation,
    target: { ...target, format: "table" },
  };
  if (typeof annotation.rawQuery === "string") delete prepared.rawQuery;
  return prepared;
}

export function sqlAnnotationFrames(
  frames: DataFrame[],
  mappings: AnnotationEventMappings = {},
) {
  const textName = "__sql_annotation_empty_text";
  const textMapping = mappings.text;
  const preparedMappings: AnnotationEventMappings = { ...mappings };
  const prepared = frames.map((frame) => {
    const names = (field: Field) =>
      String(field.config.displayName || field.name).toLowerCase();
    const start = String(mappings.time?.value || "time").toLowerCase();
    const end = String(mappings.timeEnd?.value || "timeend").toLowerCase();
    const timeField =
      frame.fields.find((field) => names(field) === start) ||
      (!mappings.time?.value
        ? frame.fields.find(
            (field) => names(field) === "time_sec" || field.type === "time",
          )
        : undefined);
    if (frame.length && !timeField && mappings.time?.source !== "text")
      throw new Error("SQL annotations require a time column");
    const hasText =
      (textMapping?.source === "text" && Boolean(textMapping.value)) ||
      (textMapping?.source !== "skip" &&
        frame.fields.some(
          (field) =>
            names(field) === String(textMapping?.value || "text").toLowerCase(),
        ));
    const fields = frame.fields.map((field) => {
      if (field !== timeField && names(field) !== end) return field;
      return {
        ...field,
        type: "time" as Field["type"],
        name:
          field === timeField && !mappings.time?.value ? "time" : field.name,
        values: Array.from(field.values, (value) =>
          annotationTime(value, field),
        ),
      };
    });
    if (!hasText) {
      fields.push({
        name: textName,
        type: "string" as Field["type"],
        config: {},
        values: Array(frame.length).fill(""),
      });
      preparedMappings.text = { value: textName };
    }
    return { ...frame, fields };
  });
  return { frames: prepared, mappings: preparedMappings };
}
