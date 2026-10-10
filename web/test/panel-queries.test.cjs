const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/grafana/panel-queries.ts", "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS },
  }).outputText,
  { exports: exported, structuredClone },
);
const queries = [
  {
    refId: "A",
    datasource: { uid: "metricspanel", type: "prometheus" },
    expr: "vector(233)",
    hide: true,
  },
  {
    refId: "B",
    datasource: { uid: "__expr__", type: "__expr__" },
    type: "math",
    expression: "$A * 2",
    opaque: { stable: true },
  },
];
function dashboard(grafana) {
  return {
    id: "saved",
    name: "Queries",
    updated_at: 233,
    grafana,
    panels: [
      {
        id: "native-id",
        title: "Values",
        visualization: "table",
        config: { id: 7, type: "table", targets: structuredClone(queries) },
      },
    ],
  };
}
const json = (value) => JSON.parse(JSON.stringify(value));
test("classic and V1 panel edits preserve rows, envelopes, other panels and unknown fields", () => {
  const panel = {
    id: 7,
    type: "table",
    title: "Values",
    targets: structuredClone(queries),
    transformations: [
      { id: "organize", options: { renameByName: { Value: "Business" } } },
    ],
    opaque: { count: "18446744073709551615" },
  };
  const model = {
    title: "Queries",
    panels: [
      { id: 1, type: "row", panels: [panel] },
      { id: 8, type: "stat", targets: [] },
    ],
    annotations: { list: [] },
    opaque: [1, 2, 3],
  };
  for (const raw of [
    model,
    {
      apiVersion: "dashboard.grafana.app/v1beta1",
      kind: "Dashboard",
      metadata: { name: "saved", resourceVersion: "v233" },
      spec: model,
      opaque: true,
    },
  ]) {
    const d = dashboard(structuredClone(raw)),
      original = structuredClone(d);
    const changed = structuredClone(queries);
    changed[1].expression = "$A * 3";
    const saved = exported.withPanelQueries(d, "native-id", changed);
    const expected = structuredClone(raw);
    (expected.spec || expected).panels[0].panels[0].targets = changed;
    assert.deepEqual(json(saved.grafana), expected);
    assert.deepEqual(d, original);
    assert.equal(saved.panels[0].config.targets[1].expression, "$A * 3");
    assert.equal(exported.withPanelQueries(d, "native-id", queries), d);
  }
});
test("V2 edits and RefID renames preserve query envelopes and leave untouched resources exact", () => {
  const resources = queries.map((target, i) => {
    const { refId, hide, datasource, ...spec } = target;
    return {
      kind: "PanelQuery",
      opaque: i,
      spec: {
        refId,
        hidden: !!hide,
        opaque: { order: i },
        query: {
          kind: "DataQuery",
          group: datasource.type,
          version: "v233",
          datasource: { name: datasource.uid, opaque: 233 },
          spec,
        },
      },
    };
  });
  const raw = {
    apiVersion: "dashboard.grafana.app/v2beta1",
    kind: "Dashboard",
    metadata: { name: "saved" },
    spec: {
      elements: {
        values: {
          kind: "Panel",
          spec: {
            id: 7,
            data: {
              kind: "QueryGroup",
              spec: {
                queries: resources,
                transformations: [{ kind: "organize" }],
                queryOptions: { timeCompare: "1d" },
              },
            },
            vizConfig: { group: "table", spec: { opaque: true } },
          },
        },
        other: { kind: "LibraryPanel", spec: { opaque: 233 } },
      },
      layout: { kind: "TabsLayout", spec: { unknown: true } },
    },
  };
  const d = dashboard(raw),
    changed = structuredClone(queries);
  changed[1].expression = "$A * 3";
  changed[1].refId = "named result";
  const saved = exported.withPanelQueries(d, "native-id", changed, ["A", "B"]);
  const expected = structuredClone(raw);
  const result = expected.spec.elements.values.spec.data.spec.queries[1];
  result.spec.refId = "named result";
  result.spec.query.spec.expression = "$A * 3";
  assert.deepEqual(json(saved.grafana), expected);
  assert.deepEqual(
    json(saved.grafana.spec.elements.values.spec.data.spec.queries[0]),
    resources[0],
  );
  assert.deepEqual(raw.spec.elements.values.spec.data.spec.queries, resources);
});
test("panel edits reject duplicate or absent refs, oversize groups and ambiguous source panels", () => {
  const d = dashboard({ panels: [{ id: 7 }, { id: 7 }] });
  for (const invalid of [
    [{ refId: "A" }, { refId: "A" }],
    [{ refId: "" }],
    [{ refId: "x".repeat(101) }],
    Array.from({ length: 33 }, (_, i) => ({ refId: String(i) })),
  ])
    assert.throws(() => exported.withPanelQueries(d, "native-id", invalid));
  assert.throws(() => exported.withPanelQueries(d, "missing", []));
  assert.throws(() =>
    exported.withPanelQueries(d, "native-id", [{ refId: "C" }]),
  );
});
