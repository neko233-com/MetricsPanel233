const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exportsObject = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/grafana/sql-variables.ts", "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS },
  }).outputText,
  { exports: exportsObject },
);
const { interpolateSQLVariables: interpolate } = exportsObject;

test("SQL variables preserve exact server macros and untouched query syntax", () => {
  const sql =
    "SELECT $__timeGroupAlias(created_at, '$__interval'), SUM(value) FROM measurements WHERE $__timeFilter(created_at) AND region IN(${region:sqlstring}) AND time >= ${__from} AND missing=$missing GROUP BY time ORDER BY time";
  const result = interpolate(
    sql,
    {
      region: { value: ["us-east", "ap-south"] },
      __from: { value: "wrong-client-time" },
      __interval: { value: "wrong-client-interval" },
    },
    "postgres",
  );
  assert.equal(
    result,
    sql.replace("${region:sqlstring}", "'us-east','ap-south'"),
  );
});

test("SQL interpolation escapes apostrophes, dialect backslashes and multi-value defaults", () => {
  const values = {
    customer: { value: "O'Reilly\\reports" },
    regions: { value: ["O'Reilly", "a\\b"] },
  };
  assert.equal(
    interpolate("name='$customer' AND region IN($regions)", values, "postgres"),
    "name='O''Reilly\\reports' AND region IN('O''Reilly','a\\b')",
  );
  assert.equal(
    interpolate("name='$customer' AND region IN($regions)", values, "mysql"),
    "name='O''Reilly\\\\reports' AND region IN('O''Reilly','a\\\\b')",
  );
  assert.equal(
    interpolate(
      "IN($one) IN([[all]])",
      {
        one: { value: "only", multi: true },
        all: { value: "any", includeAll: true },
      },
      "postgres",
    ),
    "IN('only') IN('any')",
  );
});

test("SQL explicit formats distinguish quoted values from raw numeric and identifier lists", () => {
  const values = {
    ids: { value: ["233", "466"] },
    label: { value: "Joe's" },
    columns: { value: ["time", "value"] },
  };
  assert.equal(
    interpolate(
      "SELECT ${columns:csv} WHERE id IN(${ids:csv}) AND label=${label:sqlstring}",
      values,
      "postgres",
    ),
    "SELECT time,value WHERE id IN(233,466) AND label='Joe''s'",
  );
  assert.equal(
    interpolate("SELECT [[columns:raw]]", values, "mysql"),
    "SELECT time,value",
  );
  assert.throws(
    () => interpolate("${ids:unknown}", values, "mysql"),
    /Unsupported SQL variable format/,
  );
});

test("SQL custom All expressions remain raw and absent values remain visible", () => {
  assert.equal(
    interpolate(
      "metric IN($all) AND region=$missing AND user=$unset",
      {
        all: { value: { raw: "SELECT name FROM known_metrics" } },
        unset: { value: undefined },
      },
      "postgres",
    ),
    "metric IN(SELECT name FROM known_metrics) AND region=$missing AND user=$unset",
  );
  assert.throws(
    () =>
      interpolate(
        "$invalid",
        { invalid: { value: { unsafe: "object" } } },
        "postgres",
      ),
    /SQL variables must contain/,
  );
});
