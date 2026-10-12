const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exportsObject = {};
vm.runInNewContext(
  ts.transpileModule(
    fs.readFileSync("src/grafana/sql-variable-results.ts", "utf8"),
    {
      compilerOptions: { module: ts.ModuleKind.CommonJS },
    },
  ).outputText,
  { exports: exportsObject },
);
const convert = (frames) =>
  JSON.parse(JSON.stringify(exportsObject.sqlVariableResults(frames)));

test("SQL variable results pair text and value aliases while preserving string IDs", () => {
  assert.deepEqual(
    convert([
      {
        length: 4,
        fields: [
          {
            name: "__value",
            values: ["9007199254740993", "west", "west", null],
          },
          { name: "__text", values: ["East", "West", "duplicate", "missing"] },
        ],
      },
    ]),
    [
      { text: "East", value: "9007199254740993" },
      { text: "West", value: "west" },
    ],
  );
});

test("SQL variable results flatten distinct column values and retain false and zero", () => {
  assert.deepEqual(
    convert([
      {
        length: 4,
        fields: [{ name: "region", values: ["east", "west", null, "east"] }],
      },
      { length: 3, fields: [{ name: "numeric", values: [0, 233, false] }] },
    ]),
    [
      { text: "east", value: "east" },
      { text: "west", value: "west" },
      { text: "0", value: "0" },
      { text: "233", value: "233" },
      { text: "false", value: "false" },
    ],
  );
  assert.deepEqual(convert([]), []);
});
