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

test("SQL comparisons preserve table references, CTE scopes, aliases and quoted text in isolated requests", () => {
  const sql =
    "WITH A_cte AS (SELECT A.value, 'A $A A-compare' AS text FROM `A`) SELECT x.value FROM A_cte x JOIN `long name` B ON x.value=B.value /* $A */";
  const queries = [
    {
      refId: "A",
      datasource: { uid: "metricspanel" },
      expr: "vector(233)",
      hide: true,
    },
    { refId: "long name", datasource: { uid: "sdk" }, value: 2, hide: true },
    {
      refId: "Q",
      datasource: { uid: "__expr__" },
      type: "sql",
      expression: sql,
      unknown: 233,
    },
    {
      refId: "M",
      datasource: { uid: "-100" },
      type: "math",
      expression: "$A * 2",
    },
  ];
  const refs = new Map(
    queries.map((query) => [query.refId, query.refId + "-compare"]),
  );
  const compared = exported.comparisonQueries(queries, refs);
  assert.deepEqual(
    Array.from(compared, (query) => query.refId),
    ["A", "long name", "Q", "M"],
  );
  assert.equal(compared[2].expression, sql);
  assert.equal(compared[2].unknown, 233);
  assert.equal(compared[3].expression, "$A * 2");
  assert.notEqual(compared[0], queries[0]);
  assert.equal(queries[2].expression, sql);
});

test("ordinary comparisons still rename input references and preserve datasource query variables", () => {
  const queries = [
    { refId: "A", datasource: { uid: "metricspanel" }, expr: "up{job='$job'}" },
    {
      refId: "M",
      datasource: { type: "__expr__" },
      type: "math",
      expression: "$A*2",
    },
    {
      refId: "sql-source",
      datasource: { uid: "mysql" },
      type: "sql",
      expression: "SELECT * FROM actual_table",
    },
  ];
  const refs = new Map(
    queries.map((query) => [query.refId, query.refId + "-compare"]),
  );
  const compared = exported.comparisonQueries(queries, refs);
  assert.deepEqual(
    Array.from(compared, (query) => query.refId),
    ["A-compare", "M-compare", "sql-source-compare"],
  );
  assert.equal(compared[0].expr, "up{job='$job'}");
  assert.equal(compared[1].expression, "$" + "{A-compare}*2");
  assert.equal(compared[2].expression, "SELECT * FROM actual_table");
});
