const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/grafana/expression-ref.ts", "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS },
  }).outputText,
  { exports: exported },
);
test("expression references resolve UID, legacy ID, name and type-only references", () => {
  for (const ref of [
    "__expr__",
    "-100",
    "Expression",
    { uid: "-100" },
    { type: "__expr__" },
  ])
    assert.equal(exported.isExpressionRef(ref), true);
  for (const ref of [undefined, null, "", "metricspanel", { uid: "grafana" }])
    assert.equal(exported.isExpressionRef(ref), false);
});
test("comparison dependency names use braces and preserve ordinary variables", () => {
  const refs = new Map([
    ["A", "A-compare"],
    ["long name", "long name-compare"],
  ]);
  assert.equal(
    exported.remapExpressionInput("$A + $" + "{long name} + $rate", refs),
    "$" + "{A-compare} + $" + "{long name-compare} + $rate",
  );
  assert.equal(exported.remapExpressionInput("A", refs), "A-compare");
  assert.equal(exported.remapExpressionInput("$A", refs), "$" + "{A-compare}");
});

test("classic comparison references preserve range parameters and unknown fields without mutations", () => {
  const query = {
    refId: "C",
    type: "classic_conditions",
    unknown: 233,
    conditions: [
      {
        query: { params: ["long name", "5m", "now"], unknown: "keep" },
        reducer: { type: "avg" },
        evaluator: { type: "gt", params: [2] },
      },
      { query: { params: ["missing"] } },
    ],
  };
  const result = exported.remapExpressionQuery(
    query,
    new Map([["long name", "long name-compare"]]),
  );
  assert.equal(result.unknown, 233);
  assert.deepEqual(Array.from(result.conditions[0].query.params), [
    "long name-compare",
    "5m",
    "now",
  ]);
  assert.equal(result.conditions[0].query.unknown, "keep");
  assert.equal(query.conditions[0].query.params[0], "long name");
  assert.equal(result.conditions[1], query.conditions[1]);
  const malformed = exported.remapExpressionQuery(
    {
      type: "classic_conditions",
      conditions: [null, 233, { query: { params: "A" } }],
    },
    new Map([["A", "A-compare"]]),
  );
  assert.equal(malformed.conditions[0], null);
  assert.equal(malformed.conditions[1], 233);
  assert.equal(malformed.conditions[2].query.params, "A");
});
