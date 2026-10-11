const test = require("node:test"),
  assert = require("node:assert/strict"),
  fs = require("node:fs"),
  vm = require("node:vm"),
  ts = require("typescript");
const moduleOutput = { exports: {} };
vm.runInNewContext(
  ts.transpileModule(
    fs.readFileSync("src/grafana/community-reference.ts", "utf8"),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText,
  { module: moduleOutput, exports: moduleOutput.exports, URL },
);
const { communityReference } = moduleOutput.exports;
test("Community references resolve official IDs, page links and exact download revisions", () => {
  assert.equal(communityReference(" 1860 ").id, 1860);
  assert.equal(
    communityReference(
      "https://grafana.com/grafana/dashboards/1860-node-exporter-full/",
    ).id,
    1860,
  );
  assert.equal(
    communityReference(
      "https://grafana.com/api/dashboards/1860/revisions/42/download",
    ).revision,
    42,
  );
});
test("Community references reject noncatalog hosts, credentials, paths and out-of-range identifiers", () => {
  for (const value of [
    "0",
    "-1",
    "9999999999999999999999",
    "http://grafana.com/grafana/dashboards/1860/",
    "https://localhost/dashboards/1860",
    "https://grafana.com.evil.test/dashboards/1860",
    "https://secret@grafana.com/dashboards/1860",
    "https://grafana.com:4430/dashboards/1860",
    "https://grafana.com/api/plugins/clock",
  ])
    assert.throws(() => communityReference(value));
});
