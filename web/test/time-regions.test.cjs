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
      path.join(__dirname, "../src/grafana/time-regions.ts"),
      "utf8",
    ),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText,
  { exports: exported, require, Date, Intl },
);
const {
  simpleTimeRegionPlan,
  calculateTimeRegions,
  timeRegionQuery,
  isTimeRegionQuery,
} = exported;
const plain = (value) => JSON.parse(JSON.stringify(value));
const ms = (value) => Date.parse(value);
const between = (from, to) => ({ from: ms(from), to: ms(to) });
const iso = (regions) =>
  Array.from(regions, (r) => [
    new Date(r.from).toISOString(),
    new Date(r.to).toISOString(),
  ]);

test("simple regions distinguish daily overnight, explicit weekday week rollover, day-only inclusion and points", () => {
  const plans = [
    [{ from: "09:00", to: "17:00" }, "0 9 * * *", 8],
    [{ from: "22:00", to: "02:00" }, "0 22 * * *", 4],
    [{ fromDayOfWeek: 1, from: "22:00", to: "02:00" }, "0 22 * * 1", 148],
    [{ fromDayOfWeek: 6, toDayOfWeek: 1 }, "0 0 * * 6", 72],
    [{ fromDayOfWeek: 1, toDayOfWeek: 2 }, "0 0 * * 1", 48],
    [{ fromDayOfWeek: 7 }, "0 0 * * 7", 24],
    [{ fromDayOfWeek: 1, toDayOfWeek: 7 }, "0 0 * * 1", 0],
    [{ from: "09:00" }, "0 9 * * *", 0],
  ];
  for (const [config, cronExpr, hours] of plans)
    assert.deepEqual(plain(simpleTimeRegionPlan(config)), {
      cronExpr,
      durationMs: hours * 3600000,
    });
  assert.equal(simpleTimeRegionPlan({ to: "17:00" }), undefined);
  assert.equal(simpleTimeRegionPlan({ from: "", to: "" }), undefined);
});

test("the pinned Grafana weekday golden retains full unclipped starts and ends", () => {
  const regions = calculateTimeRegions(
    { fromDayOfWeek: 1, toDayOfWeek: 2, timezone: "utc" },
    between("2023-03-01T00:00Z", "2023-03-31T00:00Z"),
  );
  assert.deepEqual(
    Array.from(regions, (r) => r.from),
    [1678060800000, 1678665600000, 1679270400000, 1679875200000],
  );
  assert.deepEqual(
    Array.from(regions, (r) => r.to),
    [1678233600000, 1678838400000, 1679443200000, 1680048000000],
  );
  assert.deepEqual(
    iso(
      calculateTimeRegions(
        { from: "09:00", to: "17:00", timezone: "utc" },
        between("2026-10-05T12:00Z", "2026-10-06T12:00Z"),
      ),
    ),
    [
      ["2026-10-05T09:00:00.000Z", "2026-10-05T17:00:00.000Z"],
      ["2026-10-06T09:00:00.000Z", "2026-10-06T17:00:00.000Z"],
    ],
  );
});

test("overlap excludes ends at the lower bound; starts at the upper bound remain included", () => {
  const config = { from: "09:00", to: "17:00", timezone: "utc" };
  assert.deepEqual(
    iso(
      calculateTimeRegions(
        config,
        between("2026-10-05T17:00Z", "2026-10-06T09:00Z"),
      ),
    ),
    [["2026-10-06T09:00:00.000Z", "2026-10-06T17:00:00.000Z"]],
  );
  const point = { from: "09:00", timezone: "utc" };
  assert.equal(
    calculateTimeRegions(
      point,
      between("2026-10-05T09:00Z", "2026-10-05T10:00Z"),
    ).length,
    0,
  );
});

test("UTC, IANA and browser zones determine starts without inheriting dashboard timezone", () => {
  const window = between("2023-03-01T00:00Z", "2023-03-08T00:00Z");
  assert.equal(
    calculateTimeRegions(
      { fromDayOfWeek: 1, timezone: "America/Chicago" },
      window,
    )[0].from,
    1678082400000,
  );
  assert.equal(
    calculateTimeRegions(
      { fromDayOfWeek: 1, timezone: "Asia/Shanghai" },
      window,
    )[0].from,
    ms("2023-03-05T16:00Z"),
  );
  assert.deepEqual(
    plain(
      calculateTimeRegions({ fromDayOfWeek: 1, timezone: "browser" }, window),
    ),
    plain(
      calculateTimeRegions(
        {
          fromDayOfWeek: 1,
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        },
        window,
      ),
    ),
  );
  assert.deepEqual(
    plain(calculateTimeRegions({ fromDayOfWeek: 1 }, window)),
    plain(
      calculateTimeRegions({ fromDayOfWeek: 1, timezone: "browser" }, window),
    ),
  );
});

test("Croner 9.1 DST gaps normalize forward and folds run once, with fixed elapsed region duration", () => {
  const spring = calculateTimeRegions(
    {
      mode: "cron",
      cronExpr: "30 2 * * *",
      duration: "8h",
      timezone: "America/New_York",
    },
    between("2026-03-07T00:00Z", "2026-03-10T00:00Z"),
  );
  assert.deepEqual(
    Array.from(spring, (r) => new Date(r.from).toISOString()),
    [
      "2026-03-07T07:30:00.000Z",
      "2026-03-08T07:30:00.000Z",
      "2026-03-09T06:30:00.000Z",
    ],
  );
  const fall = calculateTimeRegions(
    {
      mode: "cron",
      cronExpr: "30 1 * * *",
      duration: "8h",
      timezone: "America/New_York",
    },
    between("2026-10-31T00:00Z", "2026-11-03T00:00Z"),
  );
  assert.deepEqual(
    Array.from(fall, (r) => new Date(r.from).toISOString()),
    [
      "2026-10-31T05:30:00.000Z",
      "2026-11-01T05:30:00.000Z",
      "2026-11-02T06:30:00.000Z",
    ],
  );
  for (const r of [...spring, ...fall]) assert.equal(r.to - r.from, 28800000);
  const weekend = calculateTimeRegions(
    { fromDayOfWeek: 7, timezone: "America/New_York" },
    between("2026-03-08T00:00Z", "2026-03-09T00:00Z"),
  );
  assert.equal(weekend[0].to - weekend[0].from, 86400000);
});

test("advanced Cron supports seconds, names, last-day and nth-weekday using the pinned parser", () => {
  for (const [expr, start, end, expected] of [
    [
      "15 30 9 * * MON-FRI",
      "2026-10-05T09:30:00Z",
      "2026-10-05T09:30:20Z",
      "2026-10-05T09:30:15.000Z",
    ],
    [
      "0 9 L * *",
      "2026-10-30T00:00Z",
      "2026-11-01T00:00Z",
      "2026-10-31T09:00:00.000Z",
    ],
    [
      "0 9 * * MON#2",
      "2026-10-01T00:00Z",
      "2026-10-15T00:00Z",
      "2026-10-12T09:00:00.000Z",
    ],
  ]) {
    const result = calculateTimeRegions(
      { mode: "cron", cronExpr: expr, duration: "1h 30m", timezone: "utc" },
      between(start, end),
    );
    assert.equal(result.length, 1);
    assert.equal(new Date(result[0].from).toISOString(), expected);
    assert.equal(result[0].to - result[0].from, 5400000);
  }
});

test("invalid configuration and excessive recurrence are bounded errors, without scheduling timers", () => {
  const window = between("2026-10-01T00:00Z", "2026-10-02T00:00Z");
  for (const config of [
    { from: "25:00" },
    { fromDayOfWeek: 0 },
    { fromDayOfWeek: 8 },
    { from: "09:00", timezone: "Invalid/Zone" },
    { mode: "cron", cronExpr: "invalid", duration: "8h" },
    { mode: "cron", cronExpr: "* * * * *", duration: "-1h" },
    { mode: "cron", cronExpr: "* * * * *", duration: "junk" },
    { mode: "unknown" },
  ])
    assert.throws(() => calculateTimeRegions(config, window));
  assert.throws(
    () =>
      calculateTimeRegions(
        {
          mode: "cron",
          cronExpr: "* * * * * *",
          duration: "0s",
          timezone: "utc",
        },
        window,
      ),
    /1000 time regions/,
  );
  assert.equal(calculateTimeRegions({}, window).length, 0);
  assert.throws(
    () => calculateTimeRegions({ from: "09:00" }, { from: NaN, to: 1 }),
    /range or duration/,
  );
});

test("generated events are read-only and namespaced, and expose the official time/timeEnd/text frame", () => {
  const config = {
    name: "Office hours",
    target: {
      queryType: "timeRegions",
      timeRegion: {
        from: "09:00",
        to: "17:00",
        timezone: "utc",
        secretExtra: "not-in-identity",
      },
    },
  };
  const range = between("2026-10-05T08:00Z", "2026-10-05T18:00Z");
  const result = timeRegionQuery(config, range);
  assert.equal(isTimeRegionQuery(config), true);
  assert.equal(isTimeRegionQuery({ target: { type: "timeRegions" } }), false);
  assert.equal(result.events.length, 1);
  assert.equal(result.events[0].id, undefined);
  assert.equal(result.events[0].readOnly, true);
  assert.match(result.events[0].key, /^time-region:/);
  assert.ok(!result.events[0].key.includes("not-in-identity"));
  assert.deepEqual(
    Array.from(result.frames[0].fields, (f) => f.name),
    ["time", "timeEnd", "text"],
  );
  assert.equal(result.frames[0].length, 1);
  assert.equal(result.frames[0].fields[2].values[0], "Office hours");
  assert.equal(
    timeRegionQuery(config, range).events[0].key,
    result.events[0].key,
  );
});
