const test = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const data = require("@grafana/data");
const rx = require("rxjs");
function load(file) {
  const exports = {};
  vm.runInNewContext(
    ts.transpileModule(readFileSync(file, "utf8"), {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    }).outputText,
    { exports, require, structuredClone, AbortController, Date },
  );
  return exports;
}
const { prepareSQLAnnotation, sqlAnnotationFrames } = load(
  "src/grafana/sql-annotations.ts",
);
const { annotationEventsFromFrames, runDatasourceAnnotationQuery } = load(
  "src/grafana/annotation-query.ts",
);
async function events(frame, mappings) {
  const prepared = sqlAnnotationFrames([frame], mappings);
  return rx.firstValueFrom(
    annotationEventsFromFrames(prepared.frames, prepared.mappings),
  );
}
const frame = (fields) => data.createDataFrame({ fields });

test("legacy SQL annotations migrate only their SQL model and preserve opaque settings", () => {
  const saved = {
    name: "Deploy",
    rawQuery: "SELECT $__unixEpochFrom() AS time",
    unknown: { wide: "9007199254740993" },
    target: { refId: "X", format: "time_series", opaque: true },
  };
  const converted = prepareSQLAnnotation(saved);
  assert.equal(saved.rawQuery, "SELECT $__unixEpochFrom() AS time");
  assert.equal(saved.target.format, "time_series");
  assert.equal(converted.rawQuery, undefined);
  assert.equal(converted.target.rawSql, saved.rawQuery);
  assert.equal(converted.target.format, "table");
  assert.equal(converted.target.opaque, true);
  assert.equal(converted.unknown.wide, "9007199254740993");
  assert.equal(
    prepareSQLAnnotation({
      target: { refId: "A", rawSql: "SELECT 1", opaque: 233 },
    }).target.opaque,
    233,
  );
});

test("SQL epoch seconds become milliseconds while typed datetimes retain their units", async () => {
  const epoch = await events(
    frame([
      {
        name: "time",
        type: data.FieldType.number,
        values: [1791801000.125, null],
      },
      {
        name: "TIMEEND",
        type: data.FieldType.number,
        values: [1791801060, null],
      },
      {
        name: "text",
        type: data.FieldType.string,
        values: ["deployment", "missing time"],
      },
      { name: "tags", type: data.FieldType.string, values: ["api,prod", null] },
    ]),
  );
  assert.equal(epoch[0].time, 1791801000125);
  assert.equal(epoch[0].timeEnd, 1791801060000);
  assert.equal(epoch[0].text, "deployment");
  assert.deepEqual(Array.from(epoch[0].tags), ["api", "prod"]);
  assert.equal(epoch[1].time, undefined);
  const datetime = await events(
    frame([
      { name: "time", type: data.FieldType.time, values: [1791801000125] },
      { name: "text", type: data.FieldType.string, values: ["datetime"] },
    ]),
  );
  assert.equal(datetime[0].time, 1791801000125);
});

test("SQL macros accept time_sec and missing optional text produces an empty description", async () => {
  const converted = await events(
    frame([
      { name: "time_sec", type: data.FieldType.number, values: [1791801000] },
      { name: "tags", type: data.FieldType.string, values: ["ops"] },
    ]),
  );
  assert.equal(converted[0].time, 1791801000000);
  assert.equal(converted[0].text, "");
  assert.deepEqual(Array.from(converted[0].tags), ["ops"]);
  await assert.rejects(
    events(
      frame([{ name: "value", type: data.FieldType.number, values: [233] }]),
    ),
    /time column/,
  );
  assert.deepEqual(
    Array.from(
      await events(
        frame([{ name: "value", type: data.FieldType.number, values: [] }]),
      ),
    ),
    [],
  );
});

test("SQL annotation mappings resolve custom time fields, constants and omitted descriptions", async () => {
  const converted = await events(
    frame([
      { name: "created", type: data.FieldType.number, values: [1791801000] },
      { name: "ended", type: data.FieldType.number, values: [1791801060] },
      { name: "details", type: data.FieldType.string, values: ["release"] },
    ]),
    {
      time: { value: "created" },
      timeEnd: { value: "ended" },
      text: { value: "details" },
      tags: { source: "text", value: "prod" },
    },
  );
  assert.equal(converted[0].time, 1791801000000);
  assert.equal(converted[0].timeEnd, 1791801060000);
  assert.equal(converted[0].text, "release");
  assert.deepEqual(Array.from(converted[0].tags), ["prod"]);
  const skipped = await events(
    frame([
      { name: "time", type: data.FieldType.number, values: [1791801000] },
      { name: "text", type: data.FieldType.string, values: ["hidden"] },
    ]),
    { text: { source: "skip" } },
  );
  assert.equal(skipped[0].text, "");
});

test("SQL annotations reuse the SDK request scope and unsubscribe query work", async () => {
  let request,
    canceled = 0;
  const datasource = {
    uid: "sql",
    type: "mysql",
    annotations: {
      prepareAnnotation: prepareSQLAnnotation,
      prepareQuery: (annotation) => annotation.target,
      processEvents: (annotation, frames) => {
        const prepared = sqlAnnotationFrames(frames, annotation.mappings);
        return annotationEventsFromFrames(prepared.frames, prepared.mappings);
      },
    },
    query: (input) => {
      request = input;
      return new rx.Observable((subscriber) => {
        subscriber.next({
          state: data.LoadingState.Done,
          data: [
            frame([
              {
                name: "time",
                type: data.FieldType.number,
                values: [1791801000],
              },
              {
                name: "text",
                type: data.FieldType.string,
                values: ["release"],
              },
            ]),
          ],
        });
        return () => {
          canceled++;
        };
      });
    },
  };
  const range = {
    from: data.dateTime(1791800000000),
    to: data.dateTime(1791802000000),
    raw: { from: "now-1h", to: "now" },
  };
  const result = await rx.firstValueFrom(
    runDatasourceAnnotationQuery(
      datasource,
      { rawQuery: "SELECT $__unixEpochFrom() AS time, '$service' AS text" },
      {
        range,
        timezone: "utc",
        dashboard: {},
        variables: { service: "orders" },
        width: 1536,
      },
    ),
  );
  assert.equal(request.targets[0].format, "table");
  assert.equal(
    request.targets[0].rawSql,
    "SELECT $__unixEpochFrom() AS time, '$service' AS text",
  );
  assert.equal(request.scopedVars.service.value, "orders");
  assert.equal(request.scopedVars.__from.value, range.from.valueOf());
  assert.equal(result.events[0].time, 1791801000000);
  assert.equal(canceled, 1);
});
