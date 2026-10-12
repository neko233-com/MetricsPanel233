const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const ts = require("typescript");
const exported = {};
vm.runInNewContext(
  ts.transpileModule(
    fs.readFileSync("src/grafana/variable-resolution.ts", "utf8"),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
    },
  ).outputText,
  { exports: exported },
);
const {
  createVariableResolution,
  reduceVariableResolution: reduce,
  variableResolutionView: view,
} = exported;
const requested = { region: "east", host: "stale" };
const result = {
  values: { region: "east", host: "east-1" },
  choices: {
    region: [{ text: "East", value: "east" }],
    host: [{ text: "East host", value: "east-1" }],
  },
};
function resolved() {
  let state = reduce(createVariableResolution(requested), {
    type: "start",
    request: 1,
    context: "dashboard/definitions/east",
  });
  return reduce(state, {
    type: "resolved",
    request: 1,
    context: "dashboard/definitions/east",
    result,
  });
}

test("initial dependent-variable resolution blocks panels until normalized values are ready", () => {
  const state = createVariableResolution(requested);
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).ready,
    false,
  );
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).loading,
    true,
  );
  const completed = view(
    resolved(),
    "dashboard/definitions/east",
    requested,
    true,
  );
  assert.equal(completed.ready, true);
  assert.equal(completed.values.host, "east-1");
});

test("metric and time-range refresh preserve mounted panels and successful effective selections", () => {
  let state = resolved();
  const before = view(state, "dashboard/definitions/east", requested, true);
  state = reduce(state, {
    type: "start",
    request: 2,
    context: "dashboard/definitions/east",
  });
  const pending = view(state, "dashboard/definitions/east", requested, true);
  assert.equal(pending.ready, true);
  assert.equal(pending.loading, true);
  assert.equal(pending.values, before.values);
  assert.equal(pending.choices, before.choices);
  state = reduce(state, {
    type: "resolved",
    request: 2,
    context: "dashboard/definitions/east",
    result: { ...result, values: { region: "east", host: "east-2" } },
  });
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).values.host,
    "east-2",
  );
});

test("refresh errors retain existing panels while first-load errors remain blocking", () => {
  let state = reduce(resolved(), {
    type: "start",
    request: 2,
    context: "dashboard/definitions/east",
  });
  state = reduce(state, {
    type: "failed",
    request: 2,
    context: "dashboard/definitions/east",
    error: "Datasource unavailable",
  });
  const previous = view(state, "dashboard/definitions/east", requested, true);
  assert.equal(previous.ready, true);
  assert.equal(previous.loading, false);
  assert.equal(previous.error, "Datasource unavailable");
  assert.equal(previous.values.host, "east-1");
  state = reduce(createVariableResolution(requested), {
    type: "start",
    request: 1,
    context: "dashboard/definitions/east",
  });
  state = reduce(state, {
    type: "failed",
    request: 1,
    context: "dashboard/definitions/east",
    error: "Datasource unavailable",
  });
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).ready,
    false,
  );
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).loading,
    false,
  );
});

test("changed dashboard, definition or selection blocks prior values and ignores canceled work", () => {
  const prior = resolved();
  for (const context of [
    "other/definitions/east",
    "dashboard/changed/east",
    "dashboard/definitions/west",
  ]) {
    assert.equal(view(prior, context, requested, true).ready, false);
  }
  let state = reduce(prior, { type: "invalidate", request: 2 });
  state = reduce(state, {
    type: "start",
    request: 3,
    context: "dashboard/definitions/west",
  });
  const unchanged = reduce(state, {
    type: "resolved",
    request: 1,
    context: "dashboard/definitions/east",
    result,
  });
  assert.equal(unchanged, state);
  assert.equal(
    view(
      state,
      "dashboard/definitions/west",
      { region: "west", host: "stale" },
      true,
    ).ready,
    false,
  );
  state = reduce(state, {
    type: "resolved",
    request: 3,
    context: "dashboard/definitions/west",
    result: { ...result, values: { region: "west", host: "west-1" } },
  });
  assert.equal(
    view(state, "dashboard/definitions/west", requested, true).values.host,
    "west-1",
  );
});

test("returning to an earlier selection still waits after genuine changes; no-variable dashboards stay ready", () => {
  const state = reduce(resolved(), { type: "invalidate", request: 2 });
  assert.equal(
    view(state, "dashboard/definitions/east", requested, true).ready,
    false,
  );
  const empty = view(state, "empty/dashboard", {}, false);
  assert.equal(empty.ready, true);
  assert.equal(empty.loading, false);
  assert.equal(Object.keys(empty.values).length, 0);
  assert.equal(Object.keys(empty.choices).length, 0);
});
