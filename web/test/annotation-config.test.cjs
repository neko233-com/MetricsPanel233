const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs"),
  path = require("node:path"),
  vm = require("node:vm"),
  ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(
    fs.readFileSync(
      path.join(__dirname, "../src/grafana/annotation-config.ts"),
      "utf8",
    ),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText,
  { exports: exported, require: () => ({}), structuredClone, URLSearchParams },
);
const { readAnnotationDrafts, withAnnotationQueries, annotationQueries } =
  exported;
const plain = (value) => JSON.parse(JSON.stringify(value));
const native = { id: "test", name: "Test", panels: [], updated_at: 1 };

test("native annotation lists retain unknown queries and distinguish omitted from disabled defaults", () => {
  assert.equal(annotationQueries(native)[0].builtIn, 1);
  assert.equal(annotationQueries({ ...native, annotations: [] }).length, 0);
  const draft = {
    config: {
      name: "Plugin",
      datasource: { uid: "p" },
      target: { unknown: { value: 233 } },
    },
  };
  const saved = withAnnotationQueries(native, [draft]);
  assert.equal(saved.grafana, undefined);
  assert.deepEqual(plain(saved.annotations), [draft.config]);
  saved.annotations[0].target.unknown.value = 1;
  assert.equal(draft.config.target.unknown.value, 233);
  assert.throws(
    () => withAnnotationQueries(native, Array(33).fill(draft)),
    /32 annotation queries/,
  );
});

test("classic and V1 annotation edits preserve envelope, variables, panels and unknown fields", () => {
  const model = {
    uid: "proof",
    title: "Template",
    panels: [{ id: 1, unknown: 233 }],
    annotations: {
      unknown: "retained",
      list: [{ name: "Old", query: "sql", extension: { extra: true } }],
    },
    templating: { list: [{ name: "service" }] },
  };
  for (const raw of [
    model,
    { dashboard: model, meta: { unknown: 233 } },
    {
      apiVersion: "dashboard.grafana.app/v1beta1",
      metadata: { name: "proof", unknown: 233 },
      spec: model,
    },
  ]) {
    const dashboard = { ...native, grafana: structuredClone(raw) },
      before = structuredClone(dashboard);
    const drafts = readAnnotationDrafts(dashboard);
    drafts[0].config.name = "New";
    const saved = withAnnotationQueries(dashboard, drafts),
      result = saved.grafana.dashboard || saved.grafana.spec || saved.grafana;
    assert.equal(result.annotations.list[0].name, "New");
    assert.equal(result.annotations.unknown, "retained");
    assert.equal(result.annotations.list[0].query, "sql");
    assert.deepEqual(result.panels, model.panels);
    assert.deepEqual(result.templating, model.templating);
    assert.deepEqual(dashboard, before);
  }
});

test("V2 query edits, deletion and reordering retain each resource's own metadata and legacy options", () => {
  const resource = (name, extra) => ({
    kind: "AnnotationQuery",
    unknown: extra,
    spec: {
      name,
      enable: true,
      unknownSpec: 233,
      legacyOptions: { mappings: { time: { value: "When" } }, custom: extra },
      query: {
        kind: "DataQuery",
        group: "plugin",
        version: "v0",
        datasource: { name: "events", unknown: 233 },
        spec: { refId: "B", annotation: true, text: "Old" },
      },
    },
  });
  const dashboard = {
    ...native,
    grafana: {
      apiVersion: "dashboard.grafana.app/v2beta1",
      metadata: { name: "proof" },
      spec: {
        panels: { unrelated: 233 },
        annotations: [resource("first", 1), resource("second", 2)],
      },
    },
  };
  const drafts = readAnnotationDrafts(dashboard);
  assert.deepEqual(
    plain(drafts[1].config.target),
    { refId: "B", annotation: true, text: "Old" },
    "legacy editor metadata does not become datasource query arguments",
  );
  assert.deepEqual(
    plain(withAnnotationQueries(dashboard, drafts)),
    dashboard,
    "opening and saving unchanged queries preserves the exact resources",
  );
  drafts[1].config.name = "Edited";
  drafts[1].config.target.text = "New";
  drafts[1].config.mappings = { time: { value: "TIMESTAMP" } };
  const saved = withAnnotationQueries(dashboard, [drafts[1]]).grafana;
  const anno = saved.spec.annotations[0];
  assert.equal(anno.unknown, 2);
  assert.equal(anno.spec.unknownSpec, 233);
  assert.equal(anno.spec.query.spec.text, "New");
  assert.equal(anno.spec.query.spec.refId, "B");
  assert.equal(anno.spec.query.version, "v0");
  assert.equal(anno.spec.query.datasource.unknown, 233);
  assert.equal(anno.spec.legacyOptions.custom, 2);
  assert.equal(anno.spec.legacyOptions.mappings.time.value, "TIMESTAMP");
  assert.deepEqual(saved.spec.panels, dashboard.grafana.spec.panels);
  assert.equal(dashboard.grafana.spec.annotations[1].spec.name, "second");
});
