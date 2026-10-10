const test = require("node:test"),
  assert = require("node:assert/strict");
const fs = require("node:fs"),
  path = require("node:path"),
  vm = require("node:vm"),
  ts = require("typescript");
const data = require("@grafana/data"),
  rx = require("rxjs");
const outputs = {},
  apiCalls = [],
  backendCalls = [],
  liveCalls = [];
let queryAPI = async () => [],
  liveStream = rx.EMPTY;
// Inject only transport. SDK frame conversion, dates, RxJS and production core
// query code run unchanged; browser tests exercise real DataSourceWithBackend.
class Backend extends data.DataSourceApi {
  query(request) {
    backendCalls.push(request);
    return rx.of({
      data: [
        data.toDataFrame({
          fields: [
            {
              name: "name",
              type: data.FieldType.string,
              values: ["index.html"],
            },
          ],
        }),
      ],
    });
  }
}
const runtime = {
  DataSourceWithBackend: Backend,
  getTemplateSrv: () => ({
    replace: (text, scoped, format) => {
      const value =
        text === "$tag"
          ? ["prod", "go"]
          : text === "$channel"
            ? "ds/live/counter"
            : text;
      return typeof format === "function"
        ? format(value)
        : Array.isArray(value)
          ? value.join(",")
          : value;
    },
  }),
  getGrafanaLiveSrv: () => ({
    getDataStream: (options) => {
      liveCalls.push(options);
      return liveStream;
    },
  }),
};
function load(name) {
  if (outputs[name]) return outputs[name];
  const exported = {};
  vm.runInNewContext(
    ts.transpileModule(
      fs.readFileSync(
        path.join(__dirname, "../src/grafana/" + name + ".ts"),
        "utf8",
      ),
      {
        compilerOptions: {
          module: ts.ModuleKind.CommonJS,
          target: ts.ScriptTarget.ES2022,
        },
      },
    ).outputText,
    {
      exports: exported,
      Date,
      Intl,
      structuredClone,
      URLSearchParams,
      AbortController,
      require: (module) =>
        module === "@grafana/runtime"
          ? runtime
          : module === "../api"
            ? {
                api: (url, options) => {
                  apiCalls.push({ url, options });
                  return queryAPI(url, options);
                },
              }
            : module.startsWith("./")
              ? load(module.slice(2))
              : require(module),
    },
  );
  outputs[name] = exported;
  return exported;
}
const { LocalGrafana, grafanaMeta, randomWalkFrames } = load("grafana-source");
const settings = {
  id: -1,
  uid: "grafana",
  name: "-- Grafana --",
  type: "grafana",
  meta: grafanaMeta,
  jsonData: {},
};
const request = (targets, extra = {}) => ({
  targets,
  requestId: "proof",
  intervalMs: 1000,
  maxDataPoints: 50,
  range: {
    from: data.dateTime(1000),
    to: data.dateTime(10000),
    raw: { from: "1000", to: "10000" },
  },
  ...extra,
});
const plain = (value) => JSON.parse(JSON.stringify(value));

test("core random walk follows interval, exclusive upper bound, series names, clamps and generated row bounds", () => {
  const frames = randomWalkFrames(
    { refId: "R", seriesCount: 2, startValue: 233, spread: 0, noise: 0 },
    request([]),
    () => 0.5,
  );
  assert.equal(frames.length, 2);
  assert.equal(frames[0].length, 9);
  assert.deepEqual(
    plain(frames[0].fields[0].values),
    [1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000],
  );
  assert.equal(frames[0].fields[1].name, "R-series");
  assert.equal(frames[1].fields[1].name, "R-series1");
  assert.ok(
    frames.every((frame) =>
      frame.fields[1].values.every((value) => value === 233),
    ),
  );
  const clamped = randomWalkFrames(
    { refId: "A", startValue: 10, min: 0, max: 1 },
    request([]),
  );
  assert.ok(
    clamped[0].fields[1].values.every((value) => value >= 0 && value <= 1),
  );
  assert.equal(
    randomWalkFrames({ refId: "A", dropPercent: 100 }, request([]))[0].length,
    0,
  );
  assert.throws(
    () => randomWalkFrames({ refId: "A" }, request([], { intervalMs: 0 })),
    /Invalid random walk/,
  );
  assert.throws(
    () => randomWalkFrames({ refId: "A", seriesCount: 129 }, request([])),
    /Invalid random walk/,
  );
  assert.throws(
    () =>
      randomWalkFrames(
        { refId: "A", startValue: 1.7e308, noise: 1.7e308 },
        request([]),
        () => 1,
      ),
    /non-finite/,
  );
  assert.throws(
    () =>
      randomWalkFrames(
        { refId: "A", seriesCount: 128 },
        request([], { intervalMs: 0.001 }),
      ),
    /1000000/,
  );
});

test("core mixed finite queries preserve snapshot IDs and regions, isolate errors and skip hidden targets", async () => {
  const snapshot = {
    schema: {
      refId: "Original",
      name: "Saved",
      fields: [{ name: "value", type: "number", config: {} }],
    },
    data: { values: [[42]] },
  };
  const source = new LocalGrafana(settings);
  const responses = await rx.lastValueFrom(
    source
      .query(
        request([
          { refId: "R", queryType: "randomWalk", startValue: 1, spread: 0 },
          { refId: "S", queryType: "snapshot", snapshot: [snapshot] },
          { refId: "F", queryType: "unknown" },
          { refId: "H", queryType: "unknown", hide: true },
        ]),
      )
      .pipe(rx.toArray()),
  );
  assert.deepEqual(
    Array.from(responses, (r) => r.key),
    ["R", "S", "F"],
  );
  assert.equal(responses[0].data[0].length, 9);
  assert.equal(responses[1].data[0].refId, "Original");
  assert.equal(snapshot.data.values[0][0], 42);
  assert.equal(responses[2].state, data.LoadingState.Error);
  assert.equal(responses[2].error.refId, "F");
  const result = await rx.firstValueFrom(
    source.query(
      request(
        [
          {
            refId: "T",
            queryType: "timeRegions",
            timeRegion: { from: "09:00", to: "17:00", timezone: "utc" },
          },
        ],
        {
          range: {
            from: data.dateTime("2026-10-05T08:00Z"),
            to: data.dateTime("2026-10-05T18:00Z"),
          },
        },
      ),
    ),
  );
  assert.equal(result.data[0].fields[2].values[0], "");
  assert.equal(result.data[0].refId, undefined);
  assert.equal(
    result.data[0].fields[1].values[0],
    Date.parse("2026-10-05T17:00Z"),
  );
});

test("SDK listFiles uses backend transport without inventing query time bounds", async () => {
  const source = new LocalGrafana(settings);
  const files = await rx.firstValueFrom(source.listFiles("assets", 20));
  assert.equal(files.get(0).name, "index.html");
  const request = backendCalls.at(-1);
  assert.equal(request.range, undefined);
  assert.equal(request.targets[0].queryType, "list");
  assert.equal(request.targets[0].path, "assets");
  assert.equal(request.maxDataPoints, 20);
});

test("direct legacy annotation panel queries retain tags, limit and dashboard scope", async () => {
  const source = new LocalGrafana(settings);
  queryAPI = async () => [{ time: 5000, text: "Legacy event" }];
  const tagged = await rx.firstValueFrom(
    source.query(
      request([
        {
          refId: "Old",
          queryType: "annotations",
          type: "tags",
          tags: ["$tag"],
          matchAny: true,
          limit: 7,
        },
      ]),
    ),
  );
  assert.equal(
    tagged.data[0].fields.find((field) => field.name === "text").values[0],
    "Legacy event",
  );
  let params = new URLSearchParams(apiCalls.at(-1).url.split("?")[1]);
  assert.deepEqual(params.getAll("tags"), ["prod", "go"]);
  assert.equal(params.get("limit"), "7");
  assert.equal(params.get("matchAny"), "true");
  await rx.firstValueFrom(
    source.query(
      request(
        [{ refId: "Dashboard", queryType: "annotations", type: "dashboard" }],
        { dashboardUID: "scope-233" },
      ),
    ),
  );
  params = new URLSearchParams(apiCalls.at(-1).url.split("?")[1]);
  assert.equal(params.get("dashboardUID"), "scope-233");
});

test("native SDK annotations expand array tags, preserve alert fields and cancel HTTP work", async () => {
  const source = new LocalGrafana(settings);
  queryAPI = async () => [
    {
      id: 1,
      dashboardId: 233,
      created: 5000,
      time: 5000,
      timeEnd: 6000,
      text: "Alert",
      tags: ["prod", "go"],
      alertUID: "rule",
      alertName: "Rule",
      newState: "Alerting",
    },
  ];
  const annotation = {
    name: "Tagged",
    target: { refId: "Anno", type: "tags", tags: ["$tag"], matchAny: true },
  };
  const result = await source.getAnnotations({
    annotation,
    range: request([]).range,
    rangeRaw: request([]).range.raw,
  });
  const params = new URLSearchParams(apiCalls.at(-1).url.split("?")[1]);
  assert.deepEqual(params.getAll("tags"), ["prod", "go"]);
  assert.equal(params.get("matchAny"), "true");
  assert.equal(
    result.data[0].fields.find((f) => f.name === "dashboardId").type,
    data.FieldType.number,
  );
  assert.equal(
    result.data[0].fields.find((f) => f.name === "created").type,
    data.FieldType.number,
  );
  assert.equal(
    result.data[0].fields.find((f) => f.name === "alertUID").values[0],
    "rule",
  );
  const prepared = source.annotations.prepareAnnotation({
    name: "Migrated",
    type: "tags",
    tags: ["prod"],
  });
  assert.equal(prepared.target.type, "tags");
  assert.equal(
    source.annotations.prepareQuery({ ...prepared, filter: { ids: [99] } })
      .filter,
    undefined,
  );
  queryAPI = () => new Promise(() => {});
  const listener = source
    .query(request([{ ...annotation, refId: "A", queryType: "annotations" }]))
    .subscribe();
  const signal = apiCalls.at(-1).options.signal;
  listener.unsubscribe();
  assert.equal(signal.aborted, true);
});

test("core Live measurements preserve static siblings, honor buffers/filters and release the actual subscription", () => {
  const subject = new rx.Subject();
  let active = 0;
  liveStream = new rx.Observable((subscriber) => {
    active++;
    const subscription = subject.subscribe(subscriber);
    return () => {
      active--;
      subscription.unsubscribe();
    };
  });
  const source = new LocalGrafana(settings),
    updates = [];
  const listener = source
    .query(
      request([
        { refId: "R", queryType: "randomWalk", startValue: 1, spread: 0 },
        {
          refId: "L",
          queryType: "measurements",
          channel: "$channel",
          filter: { fields: ["time", "value"] },
          buffer: 10000,
        },
      ]),
    )
    .subscribe((result) => updates.push(result));
  assert.equal(active, 1);
  assert.equal(updates[0].key, "R");
  const options = liveCalls.at(-1);
  assert.equal(options.addr.stream, "live");
  assert.equal(options.buffer.maxLength, 100);
  assert.equal(options.buffer.maxDelta, 10000);
  subject.next({
    key: options.key,
    state: data.LoadingState.Streaming,
    data: [
      data.toDataFrame({
        fields: [{ name: "value", type: "number", values: [233] }],
      }),
    ],
  });
  assert.equal(updates.at(-1).data[0].refId, "L");
  assert.equal(updates.at(-1).data[0].meta.channel, "ds/live/counter");
  listener.unsubscribe();
  assert.equal(active, 0);
});

test("SDK source discovery resolves names, IDs, type-only references and capability filters", () => {
  const { resolveSourceSettings, filterSourceSettings } =
    load("source-settings");
  const items = [
    settings,
    {
      id: 1,
      uid: "prom",
      type: "prometheus",
      name: "Metrics",
      isDefault: true,
      meta: {
        id: "prometheus",
        metrics: true,
        annotations: true,
        alerting: true,
      },
    },
    {
      id: 2,
      uid: "logs",
      type: "loki",
      name: "Logs",
      meta: {
        id: "loki",
        logs: true,
        annotations: true,
        aliasIDs: ["logs-alias"],
      },
    },
  ];
  assert.equal(
    resolveSourceSettings(items, { type: "grafana" }).uid,
    "grafana",
  );
  assert.equal(resolveSourceSettings(items, "-- Grafana --").uid, "grafana");
  assert.equal(resolveSourceSettings(items, "-1").uid, "grafana");
  assert.equal(resolveSourceSettings(items, "default").uid, "prom");
  assert.equal(
    resolveSourceSettings(items, { uid: "$source" }, () => "grafana").uid,
    "grafana",
  );
  assert.deepEqual(
    Array.from(filterSourceSettings(items, { metrics: true }), (s) => s.uid),
    ["prom", "grafana"],
  );
  assert.deepEqual(
    Array.from(filterSourceSettings(items, { alerting: true }), (s) => s.uid),
    ["prom"],
  );
  assert.deepEqual(
    Array.from(
      filterSourceSettings(items, { type: "logs-alias", pluginId: "loki" }),
      (s) => s.uid,
    ),
    ["logs"],
  );
  assert.deepEqual(
    Array.from(
      filterSourceSettings(items, { filter: (s) => s.uid !== "grafana" }),
      (s) => s.uid,
    ),
    ["logs", "prom"],
  );
});
