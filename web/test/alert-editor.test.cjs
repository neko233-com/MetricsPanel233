const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const modules = {};
for (const name of ["expression-ref", "alert-editor"]) {
  const exports = {};
  vm.runInNewContext(
    ts.transpileModule(fs.readFileSync(`src/grafana/${name}.ts`, "utf8"), {
      compilerOptions: { module: ts.ModuleKind.CommonJS },
    }).outputText,
    {
      exports,
      TextEncoder,
      require: (path) => modules[path.replace("./", "")],
    },
  );
  modules[name] = exports;
}
const {
  alertEditorTarget,
  alertQueryUID,
  withAlertTarget,
  validateAlertEditorGraph,
  nextAlertReference,
} = modules["alert-editor"];
const json = (value) => JSON.parse(JSON.stringify(value));
function graph() {
  return {
    condition: "A",
    record: { from: "A", metric: "record", opaque: 233 },
    opaque: { stable: true },
    data: [
      {
        refId: "A",
        datasourceUid: "prometheus",
        relativeTimeRange: { from: 120, to: 5, opaque: true },
        queryType: "custom",
        model: {
          expr: "vector(233)",
          instant: true,
          opaque: { large: "18446744073709551615" },
        },
      },
      {
        refId: "C",
        datasourceUid: "__expr__",
        model: { type: "math", expression: "$A > 200" },
      },
    ],
  };
}
test("alert SDK edits preserve opaque envelopes, aliases, windows and untouched nodes", () => {
  const original = graph(),
    before = structuredClone(original);
  const target = alertEditorTarget(original.data[0]);
  assert.equal(target.datasource.uid, "metricspanel");
  const result = json(
    withAlertTarget(original, 0, { ...target, expr: "vector(466)" }),
  );
  const expected = structuredClone(original);
  expected.data[0].model.expr = "vector(466)";
  assert.deepEqual(result, expected);
  assert.deepEqual(original, before);
  assert.equal(
    withAlertTarget(original, 0, { ...target, expr: "other" }).data[1],
    original.data[1],
  );
  for (const datasource of [
    "prometheus",
    { uid: "prometheus", type: "prometheus", opaque: true },
  ]) {
    original.data[0].model.datasource = datasource;
    assert.deepEqual(
      json(withAlertTarget(original, 0, alertEditorTarget(original.data[0])))
        .data[0].model.datasource,
      datasource,
    );
  }
});
test("alert reference and datasource changes retain graph metadata and recording input", () => {
  const original = graph();
  original.data[0].model.refId = "A";
  const changed = json(
    withAlertTarget(original, 0, {
      ...alertEditorTarget(original.data[0]),
      refId: "Business",
      datasource: { uid: "sdk", type: "plugin" },
      value: 2,
    }),
  );
  assert.equal(changed.condition, "Business");
  assert.equal(changed.record.from, "Business");
  assert.equal(changed.data[0].model.refId, "Business");
  assert.equal(changed.data[0].datasourceUid, "sdk");
  assert.equal(changed.data[0].model.datasource, undefined);
  assert.equal(changed.data[1].model.expression, "${Business} > 200");
  assert.deepEqual(changed.data[0].model.opaque, original.data[0].model.opaque);
  assert.equal(
    alertQueryUID({ ...original.data[0], datasourceUid: "-1" }),
    "grafana",
  );
  assert.equal(
    alertQueryUID({ ...original.data[0], datasourceUid: "-100" }),
    "__expr__",
  );
  assert.equal(nextAlertReference(original), "B");
  assert.equal(
    alertQueryUID({
      ...original.data[0],
      datasourceUid: "sdk",
      model: { datasource: { type: "__expr__", uid: "__expr__" } },
    }),
    "sdk",
  );
});
test("alert graph validation rejects duplicate refs, malformed models and invalid ranges", () => {
  assert.doesNotThrow(() => validateAlertEditorGraph(graph()));
  for (const mutate of [
    (g) => (g.data = []),
    (g) => (g.data[1].refId = "A"),
    (g) => (g.data[0].refId = "界".repeat(34)),
    (g) => (g.data[0].model = []),
    (g) => (g.data[0].relativeTimeRange.from = NaN),
    (g) => (g.data[0].relativeTimeRange.from = 1.5),
    (g) => (g.data[0].relativeTimeRange.to = 121),
    (g) => (g.data[0].relativeTimeRange.from = 2678401),
    (g) => (g.record.from = "Missing"),
    (g) => (g.data[0].datasourceUid = ""),
  ]) {
    const input = graph();
    mutate(input);
    assert.throws(() => validateAlertEditorGraph(input));
  }
});
