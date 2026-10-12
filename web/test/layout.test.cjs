const test = require("node:test"),
  assert = require("node:assert/strict"),
  fs = require("node:fs"),
  vm = require("node:vm"),
  ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(fs.readFileSync("src/grafana/layout.ts", "utf8"), {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText,
  { exports: exported, require: () => ({}) },
);
const plain = (value) => JSON.parse(JSON.stringify(value));
const panel = (id, gridPos, extra = {}) => ({
  id,
  title: id,
  visualization: "timeseries",
  config: { gridPos, ...extra },
});

test("Native default positions and imported positions share the integer 24-column grid", () => {
  assert.deepEqual(plain(exported.panelPosition(panel("native"), 3)), {
    id: "native",
    x: 12,
    y: 12,
    w: 12,
    h: 12,
  });
  assert.deepEqual(
    plain(exported.panelPosition({ id: "row", visualization: "row" }, 2)),
    { id: "row", x: 0, y: 12, w: 24, h: 1 },
  );
  assert.deepEqual(
    plain(
      exported.panelPosition(
        panel("imported", { x: 23.9, y: -2, w: 12, h: Infinity }),
        0,
      ),
    ),
    { id: "imported", x: 23, y: 0, w: 1, h: 12 },
  );
});

test("A move pushes only intersecting panels down and resolves cascaded overlaps", () => {
  const positions = [
    { id: "a", x: 0, y: 0, w: 12, h: 4 },
    { id: "b", x: 12, y: 0, w: 12, h: 4 },
    { id: "c", x: 12, y: 4, w: 12, h: 4 },
    { id: "d", x: 0, y: 4, w: 12, h: 4 },
  ];
  const result = plain(exported.changePanelPosition(positions, "a", { x: 12 }));
  assert.deepEqual(result, [
    { id: "a", x: 12, y: 0, w: 12, h: 4 },
    { id: "b", x: 12, y: 4, w: 12, h: 4 },
    { id: "c", x: 12, y: 8, w: 12, h: 4 },
    { id: "d", x: 0, y: 4, w: 12, h: 4 },
  ]);
  assert.equal(positions[1].y, 0, "The gesture baseline stays unchanged");
  for (let i = 0; i < result.length; i++)
    for (let j = i + 1; j < result.length; j++)
      assert.equal(exported.panelsOverlap(result[i], result[j]), false);
});

test("Resize clamps to valid dimensions without changing panel identities", () => {
  const positions = [{ id: "stable-id", x: 12, y: 0, w: 12, h: 12 }];
  assert.deepEqual(
    plain(
      exported.changePanelPosition(positions, "stable-id", {
        w: 99,
        h: 0,
        y: -5,
      }),
    ),
    [{ id: "stable-id", x: 0, y: 0, w: 24, h: 1 }],
  );
  assert.equal(
    exported.changePanelPosition(positions, "absent", { x: 2 }),
    positions,
  );
});

test("Added and duplicated panels retain their settings and occupy free space after a custom layout", () => {
  const existing = [panel("original", { x: 0, y: 0, w: 24, h: 24 })];
  const duplicated = panel(
    "copy",
    { x: 0, y: 0, w: 24, h: 24 },
    { options: { content: "preserved" } },
  );
  const copy = exported.placeAddedPanel(duplicated, existing);
  assert.deepEqual(plain(copy.config.gridPos), { x: 0, y: 24, w: 24, h: 24 });
  assert.equal(copy.config.options.content, "preserved");
  assert.equal(
    exported.panelsOverlap(
      exported.panelPosition(existing[0], 0),
      exported.panelPosition(copy, 1),
    ),
    false,
  );
  assert.equal(duplicated.config.gridPos.y, 0);
});

test("Repeated panel rendering expands scoped values while editor positions keep base IDs", () => {
  const source = [
    panel(
      "hosts",
      { x: 0, y: 0, w: 12, h: 5 },
      { repeat: "host", maxPerRow: 2 },
    ),
    panel("later", { x: 0, y: 5, w: 24, h: 3 }),
  ];
  const before = JSON.stringify(source);
  const rendered = plain(
    exported.layoutPanels(
      source,
      { host: "$__all" },
      { host: ["alpha", "beta", "gamma"] },
    ),
  );
  assert.deepEqual(
    rendered.map(({ panel: item, x, y, values }) => [
      item.id,
      x,
      y,
      values.host,
    ]),
    [
      ["hosts-0", 0, 0, "alpha"],
      ["hosts-1", 12, 0, "beta"],
      ["hosts-2", 0, 5, "gamma"],
      ["later-0", 0, 10, "$__all"],
    ],
  );
  assert.deepEqual(
    plain(source.map(exported.panelPosition)).map(({ id }) => id),
    ["hosts", "later"],
  );
  assert.equal(JSON.stringify(source), before);
  const originals = exported.layoutPanels(
    source,
    { host: ["alpha", "beta"] },
    {},
  );
  assert.equal(originals[0].sourcePanel, source[0]);
  assert.equal(originals[1].sourcePanel, source[0]);
  assert.equal(originals[2].sourcePanel, source[1]);
  const hyphenated = panel("orders-0", { x: 0, y: 0, w: 12, h: 5 });
  assert.equal(
    exported.layoutPanels([hyphenated], {}, {})[0].sourcePanel.id,
    "orders-0",
  );
});
