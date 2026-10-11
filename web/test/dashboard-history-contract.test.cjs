const test = require("node:test"),
  assert = require("node:assert/strict"),
  fs = require("node:fs"),
  vm = require("node:vm"),
  ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/api.ts", "utf8"), {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText,
  { exports: exported, require: () => ({}) },
);
const uid = (grafana) =>
  exported.dashboardUID({ id: "native-stable-id", grafana });
test("Wrapped dashboard identity ignores outer metadata and follows the actual document", () => {
  assert.equal(
    uid({
      uid: "wrong-outer",
      metadata: { name: "wrong-resource" },
      dashboard: { uid: "inner-classic" },
    }),
    "inner-classic",
  );
  assert.equal(
    uid({
      uid: "wrong-outer",
      metadata: { name: "wrong-resource" },
      dashboard: { metadata: { name: "inner-v2" }, spec: { elements: {} } },
    }),
    "inner-v2",
  );
  assert.equal(
    uid({
      uid: "wrong-outer",
      metadata: { name: "wrong-resource" },
      dashboard: { spec: { uid: "inner-v1" } },
    }),
    "inner-v1",
  );
  assert.equal(
    uid({
      uid: "wrong-outer",
      metadata: { name: "wrong-resource" },
      dashboard: { title: "No identity" },
    }),
    "native-stable-id",
  );
  assert.equal(uid({ uid: "classic" }), "classic");
  assert.equal(
    uid({ metadata: { name: "resource" }, spec: { uid: "v1" } }),
    "resource",
  );
  assert.equal(uid(undefined), "native-stable-id");
  assert.equal(
    uid({ uid: "wrong-outer", dashboard: null }),
    "native-stable-id",
  );
  assert.equal(uid({ uid: "wrong-outer", dashboard: [] }), "native-stable-id");
  assert.equal(uid({ dashboard: { uid: 233 } }), "native-stable-id");
});
