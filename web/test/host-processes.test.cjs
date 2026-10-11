const test = require("node:test"), assert = require("node:assert/strict"), fs = require("node:fs"), vm = require("node:vm"), ts = require("typescript");
const exported = {};
vm.runInNewContext(ts.transpileModule(fs.readFileSync("src/api.ts", "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: exported, require: () => ({}) });

test("CPU, memory and icon refreshes keep process row order and replace sampled values", () => {
  const previous = [{ id: "a", name: "Alpha", pid: 8 }, { id: "b", name: "alpha", pid: 10 }];
  const next = [{ ...previous[1], cpuPercent: 99, memoryRssBytes: "18446744073709551615", iconKey: "changed" }, { ...previous[0], cpuPercent: 1 }];
  const rows = exported.stableProcessRows(previous, next);
  assert.deepEqual(Array.from(rows, (item) => item.pid), [8, 10]);
  assert.equal(rows[1], next[0]);
  assert.equal(rows[1].memoryRssBytes, "18446744073709551615");
});
test("Process names, new members and PID reuse adopt the new ordered snapshot", () => {
  const previous = [{ id: "old-start", name: "alpha", pid: 8 }, { id: "b", name: "beta", pid: 10 }];
  const renamed = [{ ...previous[1] }, { ...previous[0], name: "Zulu" }];
  assert.equal(exported.stableProcessRows(previous, renamed), renamed);
  const reused = [{ id: "new-start", name: "alpha", pid: 8 }, previous[1]];
  assert.equal(exported.stableProcessRows(previous, reused), reused);
  const expanded = [previous[0], { id: "new", name: "atlas", pid: 20 }, previous[1]];
  assert.equal(exported.stableProcessRows(previous, expanded), expanded);
});
test("Memory formatting handles unavailable and uint64 values without Number conversion", () => {
  assert.equal(exported.formatProcessMemory(null), "—");
  assert.equal(exported.formatProcessMemory("invalid"), "—");
  assert.equal(exported.formatProcessMemory("0"), "0 B");
  assert.equal(exported.formatProcessMemory("1024"), "1 KiB");
  assert.equal(exported.formatProcessMemory("9007199254740993"), "8 PiB");
  assert.equal(exported.formatProcessMemory("18446744073709551615"), "16 EiB");
});
