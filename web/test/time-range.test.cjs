const test = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const ts = require("typescript");
// Compile the production helper to CommonJS for the SDK's supported Node entrypoint.
const source = readFileSync(
  path.join(__dirname, "../src/grafana/time-range.ts"),
  "utf8",
);
const exportsObject = {};
const code = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2022,
  },
}).outputText;
vm.runInNewContext(code, {
  exports: exportsObject,
  require,
  Date,
  Intl,
  URLSearchParams,
});
const { resolveTimeRange, rangeFromURL, rangeFromDashboard } = exportsObject;
const now = Date.parse("2026-10-10T12:34:56.789Z");

test("one now anchors both bounds; fixed windows stay fixed across refreshes", () => {
  const relative = resolveTimeRange("1h", now);
  assert.equal(relative.start, now - 3600000);
  assert.equal(relative.end, now);
  const fixed = { from: now - 600000, to: now, timezone: "utc" };
  assert.equal(resolveTimeRange(fixed, now + 3600000).start, fixed.from);
  assert.equal(resolveTimeRange(fixed, now + 3600000).end, fixed.to);
});
test("date math rounds in the requested zone, including a 23-hour DST day", () => {
  const dst = resolveTimeRange(
    { from: "now/d", to: "now/d", timezone: "America/New_York" },
    Date.parse("2026-03-08T18:00:00Z"),
  );
  assert.equal(dst.start, Date.parse("2026-03-08T05:00:00Z"));
  assert.equal(dst.end, Date.parse("2026-03-09T03:59:59.999Z"));
  const shanghai = resolveTimeRange(
    {
      from: "2026-10-10T00:00:00",
      to: "2026-10-10T01:00:00",
      timezone: "Asia/Shanghai",
    },
    now,
  );
  assert.equal(shanghai.start, Date.parse("2026-10-09T16:00:00Z"));
  assert.equal(shanghai.end, Date.parse("2026-10-09T17:00:00Z"));
});
test("URL from/to take precedence and centered windows retain exact width", () => {
  const fixed = rangeFromURL(
    new URLSearchParams("time=10000&time.window=1001&timezone=utc"),
  );
  assert.equal(fixed.from, 9499);
  assert.equal(fixed.to, 10500);
  const preferred = rangeFromURL(
    new URLSearchParams(
      `from=${now - 5000}&to=${now}&time=10000&time.window=1000`,
    ),
  );
  assert.equal(resolveTimeRange(preferred, now).start, now - 5000);
  const zoneOnly = rangeFromURL(new URLSearchParams("timezone=utc"), {
    from: "now-6h",
    to: "now",
  });
  assert.equal(zoneOnly.from, "now-6h");
  assert.equal(zoneOnly.timezone, "utc");
});
test("classic envelopes and V1/V2 resources preserve saved defaults", () => {
  const timezoneOnly = rangeFromDashboard({ timezone: "utc" });
  assert.equal(timezoneOnly.from, "now-6h");
  assert.equal(timezoneOnly.timezone, "utc");
  for (const dashboard of [
    { dashboard: { time: { from: "now-6h", to: "now" }, timezone: "utc" } },
    { spec: { time: { from: "now-6h", to: "now" }, timezone: "utc" } },
    { spec: { timeSettings: { from: "now-6h", to: "now", timezone: "utc" } } },
  ]) {
    const value = rangeFromDashboard(dashboard);
    assert.equal(value.from, "now-6h");
    assert.equal(value.to, "now");
    assert.equal(value.timezone, "utc");
  }
});
test("invalid, inverted, oversized and malformed windows fail visibly", () => {
  for (const value of [
    { from: now, to: now },
    { from: now, to: now - 1 },
    { from: now - 32 * 86400000, to: now },
    { from: "bad", to: "now" },
    { from: "now-1h", to: "now", timezone: "Bad/Zone" },
    { from: "2026-10-10T00:00:00Z||+1h||bad", to: "now" },
  ])
    assert.throws(() => resolveTimeRange(value, now), /Invalid|positive/);
  assert.throws(
    () => rangeFromURL(new URLSearchParams("time=1")),
    /Invalid time window/,
  );
});
