const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const helpers = {};
vm.runInNewContext(ts.transpileModule(fs.readFileSync("src/grafana/time-series.ts", "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS },
}).outputText, { exports: helpers });
const plain = (value) => JSON.parse(JSON.stringify(value));
const field = (name, type, values, config = {}) => ({ name, type, values, config });

test("all temporal numeric and enum fields survive beyond eight series", () => {
  const frame = { length: 2, fields: [field("Time", "time", [1000, 2000]),
    ...Array.from({ length: 24 }, (_, index) => field(`Series ${index}`, "number", [index, null])),
    field("State", "enum", [0, 1]), field("Label", "string", ["a", "b"])] };
  const cloned = helpers.cloneTimeSeriesFrames([frame,
    { length: 1, fields: [field("Table value", "number", [5])] }]);
  assert.equal(cloned.length, 1);
  assert.equal(cloned[0].fields.filter((f) => f.type === "number" || f.type === "enum").length, 25);
  assert.equal(helpers.hasTimeSeriesSamples(cloned), true);
  assert.equal(helpers.hasTimeSeriesSamples([]), false);
  assert.equal(helpers.hasTimeSeriesSamples([{ length: 0, fields: frame.fields }]), false);
  assert.equal(helpers.hasTimeSeriesSamples([{ length: 2, fields: [field("Time", "time", [null, undefined])] }]), false);
});

test("SDK mutation leaves original values, graph settings, labels and state intact", () => {
  const display = (value) => ({ text: String(value) });
  const original = { refId: "A", name: "database", length: 2, meta: { custom: { preserved: true } },
    fields: [field("Time", "time", [1000, 2000]), {
      ...field("Value", "number", [3, null], { unit: "bytes", color: { mode: "fixed", fixedColor: "red" },
        custom: { drawStyle: "bars", stacking: { mode: "percent", group: "A" },
          scaleDistribution: { type: "log", log: 10 }, axisPlacement: "right", hideFrom: { legend: true, tooltip: false, viz: false } } }),
      labels: { database: "orders" }, state: { calcs: { last: 3 }, displayName: "orders" }, display,
    }] };
  const before = JSON.stringify(original);
  const cloned = helpers.cloneTimeSeriesFrames([original])[0];
  cloned.fields[0].values.push(3000);
  const value = cloned.fields[1];
  value.values[0] = 9;
  value.config.custom.stacking.group = "changed";
  value.config.color.fixedColor = "blue";
  value.labels.database = "changed";
  value.state.calcs.last = 9;
  helpers.setFieldVisibility(value, false);
  assert.equal(value.config.custom.hideFrom.legend, true);
  assert.equal(value.config.custom.hideFrom.tooltip, false);
  assert.equal(value.config.custom.hideFrom.viz, true);
  assert.equal(value.display, display);
  assert.equal(cloned.meta, original.meta);
  assert.equal(JSON.stringify(original), before);
});

test("legend preserves imported settings and applies hidden and placement defaults", () => {
  assert.deepEqual(plain(helpers.timeSeriesLegend({ legend: { displayMode: "table", placement: "right", calcs: ["lastNotNull", "mean", 5],
    width: 240, enableFacetedFilter: true, sortBy: "Mean", sortDesc: true } })), {
    displayMode: "table", placement: "right", calcs: ["lastNotNull", "mean"], width: 240,
    enableFacetedFilter: true, sortBy: "Mean", sortDesc: true, showLegend: true,
  });
  assert.equal(helpers.timeSeriesLegend({ legend: { displayMode: "hidden" } }).showLegend, false);
  assert.deepEqual(plain(helpers.timeSeriesLegend({})), { showLegend: true, displayMode: "list", placement: "bottom", calcs: [] });
});

test("legend selection isolates, restores saved visibility, appends and applies facet names", () => {
  const all = ["a", "b", "c"];
  assert.deepEqual(plain(helpers.changeSeriesVisibility(all, all, "b", "select")), ["b"]);
  assert.equal(helpers.changeSeriesVisibility(all, ["b"], "b", "select"), null);
  assert.deepEqual(plain(helpers.changeSeriesVisibility(all, ["a", "b"], "b", "append")), ["a"]);
  assert.deepEqual(plain(helpers.changeSeriesVisibility(all, ["a"], "c", "append")), ["a", "c"]);
  assert.deepEqual(plain(helpers.changeSeriesVisibility(all, all, ["c", "unknown"], "setExactly")), ["c"]);
  assert.deepEqual(plain(helpers.changeSeriesVisibility(all, all, [], "setExactly")), []);
  assert.equal(helpers.changeSeriesVisibility(all, [], null, "setExactly"), null);
});

test("tooltip respects hidden fields, zero filter, nulls, modes and saved numeric sorting", () => {
  const frame = { length: 1, fields: [field("Time", "time", [1000]), field("Low", "number", [1]),
    field("Zero", "number", [0]), field("High", "number", [10]), field("Null", "number", [null]),
    field("Hidden tooltip", "number", [99], { custom: { hideFrom: { tooltip: true } } }),
    field("Hidden viz", "number", [98], { custom: { hideFrom: { viz: true } } }), field("State", "enum", [2])] };
  assert.deepEqual(plain(helpers.tooltipFieldIndices(frame, 3, 0, "multi", true, "desc")), [3, 7, 1, 4]);
  assert.deepEqual(plain(helpers.tooltipFieldIndices(frame, 3, 0, "single", true, "asc")), [3]);
  assert.deepEqual(plain(helpers.tooltipFieldIndices(frame, 5, 0, "single", false, "none")), []);
  assert.deepEqual(plain(helpers.tooltipFieldIndices(frame, 3, 0, "none", false, "none")), []);
  assert.deepEqual(plain(helpers.timeSeriesTooltip({ tooltip: { mode: "multi", sort: "desc", hideZeros: true, maxHeight: 250, maxWidth: -1 } })),
    { mode: "multi", sort: "desc", hideZeros: true, maxHeight: 250 });
});

test("annotation points and regions clip to the current plot range without disappearing", () => {
  const project = (time) => time - 100;
  const geometry = (time, timeEnd) => helpers.annotationGeometry({ time, timeEnd }, 100, 200, 100, project);
  assert.deepEqual(plain(geometry(150, 150)), { left: 50, right: 50, region: false });
  assert.deepEqual(plain(geometry(50, 170)), { left: 0, right: 70, region: true });
  assert.deepEqual(plain(geometry(150, 250)), { left: 50, right: 100, region: true });
  assert.deepEqual(plain(geometry(20, 250)), { left: 0, right: 100, region: true });
  assert.equal(geometry(201, 201), undefined);
  assert.equal(geometry(50, 99), undefined);
  assert.equal(geometry(160, 120), undefined);
  assert.equal(geometry(NaN, 200), undefined);
});

test("uPlot fractional ranges become integer milliseconds without invalid or collapsed selections", () => {
  assert.deepEqual(plain(helpers.timeSeriesZoomSelection({ from: 1000.45, to: 2000.9 }, "Asia/Shanghai")),
    { from: 1000, to: 2001, timezone: "Asia/Shanghai" });
  assert.equal(helpers.timeSeriesZoomSelection({ from: 1.2, to: 1.4 }, "utc"), undefined);
  assert.equal(helpers.timeSeriesZoomSelection({ from: -50, to: 20 }, "utc"), undefined);
  assert.equal(helpers.timeSeriesZoomSelection({ from: NaN, to: 20 }, "utc"), undefined);
});

test("official comparison alignment operates on render copies and adds the SDK dashed line", () => {
  const source = fs.readFileSync("node_modules/@grafana/data/dist/esm/dataframe/utils.mjs", "utf8");
  const context = { FieldType: { time: "time", number: "number", boolean: "boolean", enum: "enum" } };
  vm.runInNewContext(source.slice(source.indexOf("function alignTimeRangeCompareData("),
    source.indexOf("function shouldAlignTimeCompare(")), context);
  const input = [
    { length: 2, fields: [field("Time", "time", [1000, 2000]), field("Current", "number", [2, 3])] },
    { length: 2, meta: { timeCompare: { isTimeShiftQuery: true, diffMs: -1000 } },
      fields: [field("Time", "time", [0, 1000]), field("Compared", "number", [8, 9])] },
    { length: 2, meta: { timeCompare: { isTimeShiftQuery: false, diffMs: -1000 } },
      fields: [field("Time", "time", [0, 1000]), field("Unmarked", "number", [5, 6])] },
  ];
  const before = JSON.stringify(input);
  let calls = 0;
  const prepared = helpers.prepareTimeSeriesFrames(input, (frame, diff) => {
    calls++;
    return context.alignTimeRangeCompareData(frame, diff, {});
  });
  assert.equal(calls, 1);
  assert.deepEqual(plain(prepared[1].fields[0].values), [1000, 2000]);
  assert.deepEqual(plain(prepared[1].fields[1].config.custom.lineStyle), { fill: "dash", dash: [1, 5, 4, 5] });
  assert.deepEqual(plain(prepared[2].fields[0].values), [0, 1000]);
  assert.equal(JSON.stringify(input), before);
  prepared[1].fields[1].values[0] = 99;
  assert.equal(input[1].fields[1].values[0], 8);
});
