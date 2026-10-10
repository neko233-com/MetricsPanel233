const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/grafana/sql-frames.ts", "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS },
  }).outputText,
  {
    exports: exported,
    require: (name) =>
      name === "@grafana/data"
        ? {
            FieldType: { number: "number" },
            isDataFrame: (f) => Array.isArray(f?.fields),
          }
        : {
            isExpressionRef: (ref) =>
              ref?.uid === "__expr__" || ref?.type === "__expr__",
          },
  },
);
const targets = [
  { refId: "A", datasource: { uid: "metricspanel" } },
  { refId: "Q", datasource: { uid: "__expr__" }, type: "sql" },
];
function frame(refId = "Q", names = ["Memory", "Memory"]) {
  return {
    refId,
    fields: [
      {
        name: "__value__",
        type: "number",
        values: [1, 2],
        config: { unit: "bytes" },
        state: { displayName: "stale" },
      },
      { name: "__display_name__", type: "string", values: names, config: {} },
    ],
  };
}
test("SQL restores a uniform datasource alias without changing inputs or cached field state", () => {
  const original = frame();
  const response = { data: [original], state: "Done" };
  const restored = exported.restoreSQLDisplayNames(response, targets);
  assert.equal(restored.data[0].fields[0].config.displayNameFromDS, "Memory");
  assert.equal(restored.data[0].fields[0].config.unit, "bytes");
  assert.equal(restored.data[0].fields[0].state, null);
  assert.equal(original.fields[0].config.displayNameFromDS, undefined);
  assert.equal(original.fields[0].state.displayName, "stale");
  assert.equal(restored.data[0].fields[1], original.fields[1]);
});
test("SQL full-long inputs restore aliases while unrelated and mixed-series frames retain their names", () => {
  const source = frame("A");
  source.meta = { type: "timeseries-full-long" };
  const unrelated = frame("other");
  const ordinary = frame("A");
  const mixed = frame("Q", ["go", "mysql"]);
  const empty = frame("Q", [null, null]);
  const duplicates = frame();
  duplicates.fields.push({ ...duplicates.fields[1] });
  const response = {
    data: [source, unrelated, ordinary, mixed, empty, duplicates],
  };
  const restored = exported.restoreSQLDisplayNames(response, targets);
  assert.equal(restored.data[0].fields[0].config.displayNameFromDS, "Memory");
  for (let i = 1; i < response.data.length; i++)
    assert.equal(restored.data[i], response.data[i]);
  assert.equal(
    exported.restoreSQLDisplayNames(response, targets.slice(0, 1)),
    response,
  );
});
