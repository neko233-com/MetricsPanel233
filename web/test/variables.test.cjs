const test = require("node:test"),
  assert = require("node:assert/strict");
const fs = require("node:fs"),
  vm = require("node:vm"),
  ts = require("typescript");
function createHarness(options = {}) {
  const calls = [],
    finds = [];
  const sources = options.sources || [
    { uid: "metricspanel", name: "Local", type: "prometheus", isDefault: true },
    { uid: "remote", name: "Remote", type: "prometheus" },
    { uid: "custom", name: "Custom", type: "custom-datasource" },
  ];
  const api = {
    api: async (path, init) => {
      calls.push({ path, init });
      return path === "/api/datasources"
        ? sources
        : { data: options.result || ["upstream"] };
    },
    interpolate: (value, values) =>
      value.replace(/\$\{?(\w+)\}?/g, (_, key) => String(values[key] ?? "")),
  };
  const runtime = {
    setPluginVariables: () => {},
    getPluginDatasource: async (uid, scoped) => ({
      metricFindQuery: async (query, args) => {
        finds.push({ uid, scoped, query, args });
        if (options.find) return options.find(args);
        return [{ text: "plugin", value: "plugin-value" }];
      },
    }),
  };
  const module = { exports: {} };
  const code = ts.transpileModule(
    fs.readFileSync("src/grafana/variables.ts", "utf8"),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText;
  vm.runInNewContext(code, {
    exports: module.exports,
    require: (id) =>
      id === "../api"
        ? api
        : id === "./time-range"
          ? {
              resolveTimeRange: () => ({
                start: 1000,
                end: 2000,
                sdk: { raw: { from: "now-1h", to: "now" } },
              }),
            }
          : id === "./plugin-runtime"
            ? runtime
            : require(id),
    module,
    URLSearchParams,
    AbortController,
  });
  return { ...module.exports, calls, finds };
}
const variable = {
  name: "host",
  type: "query",
  query: "label_values(up, instance)",
  options: [],
  current: "",
  multi: false,
  include_all: false,
};
test("Unbound query variables retain local Prometheus label discovery", async () => {
  const h = createHarness();
  assert.deepEqual(
    Array.from(
      await h.variableOptions(variable, {}, "1h", new AbortController().signal),
    ),
    ["upstream"],
  );
  assert.match(
    h.calls[0].path,
    /^\/prometheus\/api\/v1\/label\/instance\/values\?/,
  );
  assert.match(h.calls[0].path, /match%5B%5D=up/);
});
test("A datasource variable binding sends queries only to its chosen Prometheus proxy", async () => {
  const h = createHarness();
  const signal = new AbortController().signal;
  await h.variableOptions(
    { ...variable, config: { datasource: { uid: "$ds", type: "prometheus" } } },
    { ds: "remote" },
    "1h",
    signal,
  );
  assert.equal(h.calls.length, 2);
  assert.match(
    h.calls[1].path,
    /^\/api\/datasources\/proxy\/uid\/remote\/api\/v1\/label/,
  );
  assert.equal(h.calls[1].init.signal, signal);
});
test("Prometheus query_result variables retain upstream values and timestamps", async () => {
  const h = createHarness({
    result: {
      result: [
        { metric: { __name__: "external", host: "prod" }, value: [2, "233"] },
      ],
    },
  });
  const options = await h.variableOptions(
    {
      ...variable,
      query: "query_result(sum(up))",
      config: { datasource: "Remote" },
    },
    {},
    "1h",
    new AbortController().signal,
  );
  assert.deepEqual(Array.from(options), ['external{host="prod"} 233 2000']);
  assert.match(h.calls[1].path, /proxy\/uid\/remote\/api\/v1\/query\?/);
});
test("SDK variable queries retain their raw model, selected datasource, scope and time", async () => {
  const h = createHarness(),
    model = { query: "SELECT host", opaque: { future: 233 } },
    signal = new AbortController().signal;
  const options = await h.variableOptions(
    {
      ...variable,
      query: "",
      config: { datasource: { uid: "custom" }, query: model },
    },
    { region: "prod" },
    "1h",
    signal,
  );
  assert.deepEqual(Array.from(options), ["plugin-value"]);
  assert.equal(h.calls.length, 1);
  assert.equal(h.finds[0].uid, "custom");
  assert.equal(h.finds[0].query, model);
  assert.equal(h.finds[0].args.signal, signal);
  assert.equal(h.finds[0].scoped.region.value, "prod");
  assert.equal(h.finds[0].args.range.raw.to, "now");
});
test("Missing explicit datasource fails without querying local Prometheus", async () => {
  const h = createHarness();
  await assert.rejects(
    h.variableOptions(
      { ...variable, config: { datasource: "missing" } },
      {},
      "1h",
      new AbortController().signal,
    ),
    /datasource not found/,
  );
  assert.equal(h.calls.length, 1);
});
test("An exact datasource UID takes precedence over another datasource name", async () => {
  const h = createHarness({
    sources: [
      { uid: "cluster-a", name: "cluster-b", type: "prometheus" },
      { uid: "cluster-b", name: "Production B", type: "prometheus" },
    ],
  });
  await h.variableOptions(
    {
      ...variable,
      config: { datasource: { uid: "cluster-b", type: "prometheus" } },
    },
    {},
    "1h",
    new AbortController().signal,
  );
  assert.match(h.calls[1].path, /proxy\/uid\/cluster-b\//);
});
test("An empty typed datasource reference chooses a matching type rather than an unrelated default", async () => {
  const h = createHarness({
    sources: [
      {
        uid: "custom",
        name: "Custom",
        type: "custom-datasource",
        isDefault: true,
      },
      { uid: "remote", name: "Prometheus", type: "prometheus" },
    ],
  });
  await h.variableOptions(
    { ...variable, config: { datasource: { uid: "$ds", type: "prometheus" } } },
    { ds: "" },
    "1h",
    new AbortController().signal,
  );
  assert.match(h.calls[1].path, /proxy\/uid\/remote\//);
  assert.equal(h.finds.length, 0);
});
test("An aborted SDK variable request cannot return stale choices", async () => {
  const controller = new AbortController(),
    h = createHarness({
      find: async () => {
        controller.abort();
        return [{ text: "stale" }];
      },
    });
  await assert.rejects(
    h.variableOptions(
      { ...variable, config: { datasource: "custom" } },
      {},
      "1h",
      controller.signal,
    ),
    /abort/i,
  );
});
