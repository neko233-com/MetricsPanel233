const test = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const ts = require("typescript");
const data = require("@grafana/data");
const rx = require("rxjs");
const source = readFileSync(
  path.join(__dirname, "../src/grafana/annotation-query.ts"),
  "utf8",
);
const exported = {};
vm.runInNewContext(
  ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText,
  { exports: exported, require, structuredClone, AbortController, Date },
);
const { annotationEventsFromFrames, runDatasourceAnnotationQuery } = exported;
const range = {
  from: data.dateTime(1000),
  to: data.dateTime(10000),
  raw: { from: "1000", to: "10000" },
};
const context = {
  range,
  timezone: "utc",
  dashboard: { uid: "proof" },
  variables: { service: "api" },
  width: 1000,
};
const frame = () =>
  data.createDataFrame({
    fields: [
      { name: "When", type: data.FieldType.time, values: [5000] },
      { name: "Detail", type: data.FieldType.string, values: ["deployment"] },
      { name: "EventKey", type: data.FieldType.string, values: ["1"] },
    ],
  });
const mappings = {
  time: { value: "WHEN" },
  text: { value: "DETAIL" },
  id: { value: "EventKey" },
  tags: { source: "text", value: "api,prod" },
  title: { source: "skip" },
};

test("SDK annotation mappings merge frames, preserve string IDs, constants and skip fields", async () => {
  const extra = data.createDataFrame({
    fields: [
      { name: "When", type: data.FieldType.time, values: [5000] },
      { name: "timeEnd", type: data.FieldType.time, values: [7000] },
    ],
  });
  const events = await rx.firstValueFrom(
    annotationEventsFromFrames([frame(), extra], mappings),
  );
  assert.equal(events.length, 1);
  assert.equal(events[0].time, 5000);
  assert.equal(events[0].timeEnd, 7000);
  assert.equal(events[0].text, "deployment");
  assert.equal(events[0].id, "1");
  assert.deepEqual(Array.from(events[0].tags), ["api", "prod"]);
  assert.equal(events[0].title, undefined);
  await assert.rejects(
    rx.firstValueFrom(
      annotationEventsFromFrames([frame()], {
        ...mappings,
        time: { source: "skip" },
      }),
    ),
    /time and text/,
  );
});

test("modern AnnotationSupport prepares a clone, uses public request scopes and custom events", async () => {
  let request;
  const saved = { name: "Release", target: { value: 1 } };
  const datasource = {
    uid: "external",
    type: "fixture",
    interval: "1s",
    annotations: {
      getDefaultQuery: () => ({ defaulted: true }),
      prepareAnnotation: (value) => {
        assert.equal(value.defaulted, true);
        value.target.value = 9;
        return { ...value, name: "Prepared release" };
      },
      prepareQuery: (value) => value.target,
      processEvents: (_query, frames) =>
        rx.of([
          {
            id: "custom",
            time: frames[0].fields[0].values[0],
            text: "custom event",
          },
        ]),
    },
    query: (input) => {
      request = input;
      return rx.of(
        { state: data.LoadingState.Loading, data: [] },
        {
          state: data.LoadingState.Done,
          data: [frame()],
          error: { message: "partial query failure" },
        },
      );
    },
  };
  const result = await rx.firstValueFrom(
    runDatasourceAnnotationQuery(datasource, saved, context),
  );
  assert.equal(saved.target.value, 1);
  assert.equal(request.targets[0].value, 9);
  assert.equal(request.targets[0].refId, "Anno");
  assert.equal(request.app, data.CoreApp.Dashboard);
  assert.equal(request.timezone, "utc");
  assert.equal(request.scopedVars.service.value, "api");
  assert.equal(request.scopedVars.__from.value, 1000);
  assert.equal(request.scopedVars.__range_ms.value, 9000);
  assert.equal(request.scopedVars.__annotation.value.name, "Prepared release");
  assert.ok(request.intervalMs > 0);
  assert.equal(result.events[0].text, "custom event");
  assert.equal(result.error, "partial query failure");
});

test("standard support migrates string queries and honors prepareQuery skip", async () => {
  let executed = 0;
  const datasource = {
    uid: "external",
    type: "fixture",
    query: (request) => {
      executed++;
      assert.equal(request.targets[0].query, "old query");
      return Promise.resolve({ data: [frame()] });
    },
  };
  const result = await rx.firstValueFrom(
    runDatasourceAnnotationQuery(
      datasource,
      { name: "Legacy model", query: "old query" },
      context,
    ),
  );
  assert.equal(result.events[0].text, "deployment");
  datasource.annotations = { prepareQuery: () => undefined };
  const empty = await rx.firstValueFrom(
    runDatasourceAnnotationQuery(
      datasource,
      { target: { refId: "A" } },
      context,
    ),
  );
  assert.equal(empty.events.length, 0);
  assert.equal(executed, 1);
});

test("legacy annotationQuery receives dashboard, raw range and cancellation without calling modern query", async () => {
  let options;
  const saved = { name: "Legacy", query: "events" };
  const datasource = {
    uid: "external",
    type: "legacy",
    query: () => assert.fail("modern query invoked"),
    annotationQuery: async (input) => {
      options = input;
      input.annotation.name = "changed";
      return [{ id: "legacy", time: 5000, text: "legacy event" }];
    },
  };
  const result = await rx.firstValueFrom(
    runDatasourceAnnotationQuery(datasource, saved, context),
  );
  assert.equal(options.rangeRaw, range.raw);
  assert.equal(options.dashboard.uid, "proof");
  assert.equal(options.scopedVars.service.value, "api");
  assert.equal(options.signal.aborted, true);
  assert.equal(saved.name, "Legacy");
  assert.equal(result.events[0].id, "legacy");
});

test("annotation streaming updates stay observable and release the datasource subscription", async () => {
  let released = 0;
  const datasource = {
    uid: "external",
    type: "fixture",
    query: () =>
      new rx.Observable((observer) => {
        observer.next({ state: data.LoadingState.Streaming, data: [frame()] });
        const timer = setTimeout(
          () =>
            observer.next({
              state: data.LoadingState.Streaming,
              data: [frame()],
            }),
          5,
        );
        return () => {
          clearTimeout(timer);
          released++;
        };
      }),
  };
  const updates = await rx.lastValueFrom(
    runDatasourceAnnotationQuery(
      datasource,
      { mappings, target: { refId: "A" } },
      context,
    ).pipe(rx.take(2), rx.toArray()),
  );
  assert.equal(updates.length, 2);
  assert.equal(updates[0].events[0].id, "1");
  assert.equal(released, 1);
  const stalled = {
    ...datasource,
    query: () =>
      new rx.Observable(() => () => {
        released++;
      }),
  };
  await assert.rejects(
    rx.firstValueFrom(
      runDatasourceAnnotationQuery(
        stalled,
        { target: { refId: "A" } },
        { ...context, deadlineMs: 20 },
      ),
    ),
    (error) => error.name === "TimeoutError",
  );
  assert.equal(released, 2);
});

test("oversized annotation frames and custom results fail before reaching chart rendering", async () => {
  const oversized = data.createDataFrame({
    fields: [
      {
        name: "time",
        type: data.FieldType.time,
        values: Array(10001).fill(5000),
      },
      {
        name: "text",
        type: data.FieldType.string,
        values: Array(10001).fill("event"),
      },
    ],
  });
  assert.throws(() => annotationEventsFromFrames([oversized]), /10000/);
  const datasource = {
    uid: "external",
    type: "fixture",
    query: () => rx.of({ data: [frame()] }),
    annotations: {
      processEvents: () =>
        rx.of(Array(1001).fill({ time: 5000, text: "event" })),
    },
  };
  await assert.rejects(
    rx.firstValueFrom(
      runDatasourceAnnotationQuery(
        datasource,
        { target: { refId: "A" } },
        context,
      ),
    ),
    /1000 annotation events/,
  );
});
