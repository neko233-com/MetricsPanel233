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
