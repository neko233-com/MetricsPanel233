const test = require("node:test"),
  assert = require("node:assert/strict"),
  fs = require("node:fs"),
  vm = require("node:vm"),
  ts = require("typescript");
const moduleOutput = { exports: {} };
vm.runInNewContext(
  ts.transpileModule(
    fs.readFileSync("src/features/grafana-import-body.ts", "utf8"),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText,
  { module: moduleOutput, exports: moduleOutput.exports },
);
const { grafanaImportBody } = moduleOutput.exports;
test("Folder imports retain raw integers and escape only supplied metadata", () => {
  const dashboard =
    '{"title":"Raw","unknown":9007199254740993,"text":"9007199254740993","panels":[]}';
  const body = grafanaImportBody(dashboard, {
    folderUid: "ops",
    name: 'New "name"',
    inputs: { DS: "local" },
  });
  assert.ok(body.includes('"dashboard":' + dashboard));
  const fields = JSON.parse(body);
  assert.equal(fields.folderUid, "ops");
  assert.equal(fields.name, 'New "name"');
  assert.equal(fields.inputs.DS, "local");
  assert.throws(() => grafanaImportBody("{", { folderUid: "ops" }));
});
