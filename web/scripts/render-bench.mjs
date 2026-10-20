// Reproducible renderer benchmark. Run after a webui build:
//   node scripts/render-bench.mjs
// It opens a generated dashboard with N points, measures model/first-frame
// timings from the browser and prints JSON. Numbers are workstation-specific.
import { chromium } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = path.resolve(webRoot, "..");
const binary =
  process.env.METRICSPANEL_TEST_BINARY ||
  path.join(repoRoot, "bin", process.platform === "win32" ? "metricspanel.exe" : "metricspanel");
const temp = await mkdtemp(path.join(os.tmpdir(), "mp-render-bench-"));
const server = spawn(binary, ["serve", "--listen", "127.0.0.1:0", "--db", path.join(temp, "control.db")], {
  cwd: repoRoot,
  windowsHide: true,
  env: { ...process.env, METRICSPANEL_TOKEN: "", METRICSPANEL_STORAGE: "sqlite" },
});
let endpoint = "";
const logs = [];
server.on("error", (error) => console.error("spawn error:", error));
server.stderr.on("data", (chunk) => {
  for (const line of chunk.toString().split("\n")) {
    logs.push(line);
    try {
      const item = JSON.parse(line);
      if (item.address) endpoint = `http://${item.address}`;
    } catch {}
  }
});
const deadline = Date.now() + 15000;
while (!endpoint && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 100));
if (!endpoint) {
  console.error("server did not start\n" + logs.join("\n"));
  process.exit(1);
}
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1536, height: 1024 }, locale: "en-US" });
try {
  // Panels are limited by the 512 KiB dashboard config. Keep the snapshot
  // small; for larger arrays, feed frames directly in a dedicated benchmark.
  const points = Number(process.env.RENDER_BENCH_POINTS || 2000);
  const series = Number(process.env.RENDER_BENCH_SERIES || 5);
  const now = Date.now();
  const data = [];
  for (let s = 0; s < series; s += 1) {
    const values = [];
    for (let i = 0; i < points; i += 1) values.push(Math.sin(i / 37 + s) * (s + 1));
    data.push({ name: `Series ${s + 1}`, type: "number", values, config: {} });
  }
  const dashboard = {
    name: "Renderer benchmark",
    panels: [{
      id: "bench",
      title: "Benchmark",
      metric: "",
      unit: "",
      aggregation: "last",
      visualization: "timeseries",
      config: {
        type: "timeseries",
        gridPos: { x: 0, y: 0, w: 24, h: 16 },
        targets: [{
          refId: "A", queryType: "snapshot", datasource: { uid: "grafana", type: "grafana" },
          snapshot: [{ schema: { refId: "A", name: "Benchmark", fields: [
            { name: "Time", type: "time", config: {} }, ...data,
          ] }, data: { values: [
            Array.from({ length: points }, (_, i) => now + i * 1000), ...data.map((field) => field.values),
          ] } }],
        }],
      },
    }],
  };
  const created = await page.request.post(endpoint + "/api/v1/dashboards", { data: dashboard });
  if (!created.ok()) throw new Error(await created.text());
  const saved = await created.json();
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-renderer", "own"),
  );
  const started = Date.now();
  await page.goto(`${endpoint}/d/${saved.id}/benchmark?from=${now}&to=${now + points * 1000}&timezone=utc`);
  await page.locator(".uplot canvas").first().waitFor({ state: "visible", timeout: 60000 });
  const rendered = Date.now() - started;
  const metrics = await page.evaluate(() => ({
    canvases: document.querySelectorAll("canvas").length,
    legendButtons: document.querySelectorAll(".native-timeseries-legend-item button").length,
    memory: performance.memory?.usedJSHeapSize || 0,
  }));
  console.log(JSON.stringify({ points, series, renderedMs: rendered, ...metrics }, null, 2));
} finally {
  await browser.close();
  server.kill();
  await rm(temp, { recursive: true, force: true, maxRetries: 3 });
}
