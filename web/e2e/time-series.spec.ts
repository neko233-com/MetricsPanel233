import { test as base, expect, type Locator, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [async ({}, use) => {
    const temp = await mkdtemp(path.join(os.tmpdir(), "metricspanel233-timeseries-e2e-"));
    const service = spawn(process.env.METRICSPANEL_TEST_BINARY || path.join(root, "bin",
      process.platform === "win32" ? "metricspanel.exe" : "metricspanel"),
    ["serve", "--listen", "127.0.0.1:0", "--db", path.join(temp, "control.db")], {
      cwd: root, windowsHide: true,
      env: { ...process.env, METRICSPANEL_TOKEN: "", METRICSPANEL_STORAGE: "sqlite" },
    });
    let endpoint = "", pending = "";
    service.stderr!.on("data", (chunk) => {
      pending += chunk.toString();
      for (;;) {
        const end = pending.indexOf("\n");
        if (end < 0) break;
        const line = pending.slice(0, end);
        pending = pending.slice(end + 1);
        try { const item = JSON.parse(line); if (item.address) endpoint = `http://${item.address}`; } catch {}
      }
    });
    try {
      await expect.poll(() => endpoint, { timeout: 15000 }).not.toBe("");
      await use(endpoint);
    } finally {
      if (service.exitCode === null) {
        const exited = new Promise<void>((resolve) => service.once("exit", () => resolve()));
        service.kill();
        await exited;
      }
      const resolved = path.resolve(temp);
      if (!resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
        !path.basename(resolved).startsWith("metricspanel233-timeseries-e2e-"))
        throw new Error("Unsafe time series fixture cleanup");
      await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
    }
  }, { scope: "worker" }],
});

const start = Date.UTC(2026, 9, 10), end = start + 3600000;
const colors = ["#ff4444", "#44ff44", "#4444ff", "#ffcc44", "#cc44ff", "#44ccff",
  "#ff7744", "#77ff44", "#4477ff", "#ff44cc", "#44ffcc", "#ccff44",
  "#aa4444", "#44aa44", "#4444aa", "#aaaa44", "#aa44aa", "#44aaaa",
  "#ee88bb", "#bb88ee"];

function panel(id: number, title: string, count: number, style: "linear" | "log" | "percent" = "linear", y = 0) {
  return { id: String(id), title, metric: "", unit: "", aggregation: "last", visualization: "timeseries",
    config: { id, type: "timeseries", gridPos: { x: 0, y, w: 24, h: 15 },
      options: { legend: { displayMode: "list", placement: "right", width: 220, calcs: ["lastNotNull"] },
        tooltip: { mode: "multi", sort: "desc", hideZeros: true, maxHeight: 280 } },
      targets: [{ refId: "Snapshot", queryType: "snapshot", datasource: { uid: "grafana", type: "grafana" },
        snapshot: [{ schema: { refId: "A", name: title, fields: [{ name: "Time", type: "time", config: {} },
          ...Array.from({ length: count }, (_, index) => ({ name: `Series ${String(index + 1).padStart(2, "0")}`,
            type: "number", config: { color: { mode: "fixed", fixedColor: colors[index] }, decimals: 2,
              custom: { drawStyle: "line", lineWidth: 2, showPoints: "never", fillOpacity: 0,
                axisPlacement: style === "log" && index === count - 1 ? "right" : "left",
                axisLabel: style === "log" ? index === count - 1 ? "Right log" : "Left log" : style === "percent" ? "Percent stacked" : "Linear values",
                scaleDistribution: { type: style === "log" ? "log" : "linear", log: 10 },
                stacking: { mode: style === "percent" ? "percent" : "none", group: "A" } } } }))] },
          data: { values: [Array.from({ length: 60 }, (_, index) => start + index * (end - start) / 59),
            ...Array.from({ length: count }, (_, series) => Array.from({ length: 60 }, (_, point) =>
              (style === "log" ? 2 ** series : (series + 1) * 10) *
              (1 + 0.13 * Math.sin(point / 7 + series / 3))))] } }] }],
    } };
}

function errors(page: Page) {
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  return issues;
}
async function open(page: Page, endpoint: string, title: string, panels: ReturnType<typeof panel>[], withVariable = false) {
  await page.addInitScript(() => {
    localStorage.setItem("metricspanel-locale", "en");
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (...args) {
      const canvas = this.canvas as HTMLCanvasElement & { drawnText?: string[] };
      (canvas.drawnText ||= []).push(String(args[0]));
      return fillText.apply(this, args);
    };
    const stroke = CanvasRenderingContext2D.prototype.stroke;
    CanvasRenderingContext2D.prototype.stroke = function (...args) {
      const canvas = this.canvas as HTMLCanvasElement & { drawnStrokes?: { color: string; dash: number[] }[] };
      (canvas.drawnStrokes ||= []).push({ color: String(this.strokeStyle), dash: this.getLineDash() });
      return stroke.apply(this, args);
    };
  });
  const create = await page.request.post(endpoint + "/api/v1/dashboards", { data: { name: title, panels,
    ...(withVariable ? { variables: [{ name: "site", type: "constant", query: "desktop", current: "desktop",
      options: ["desktop"], multi: false, include_all: false,
      config: { name: "site", type: "constant", query: "desktop", current: { value: "desktop", text: "desktop" } } }] } : {}),
  } });
  expect(create.ok(), await create.text()).toBeTruthy();
  const saved = await create.json();
  await page.goto(`${endpoint}/d/${saved.id}/time-series?from=${start}&to=${end}&timezone=utc`);
  await expect(page.getByRole("heading", { name: title, exact: true })).toBeVisible();
  return saved;
}
async function colorCounts(plot: Locator, palette = colors) {
  return plot.locator("canvas").evaluate((canvas, palette) => {
    const context = (canvas as HTMLCanvasElement).getContext("2d")!;
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
    const keys = palette.map((color) => color.slice(1).toLowerCase());
    const result = keys.map(() => 0);
    for (let offset = 0; offset < pixels.length; offset += 4) {
      if (pixels[offset + 3] < 250) continue;
      const color = [pixels[offset], pixels[offset + 1], pixels[offset + 2]].map((value) => value.toString(16).padStart(2, "0")).join("");
      const index = keys.indexOf(color);
      if (index >= 0) result[index]++;
    }
    return result;
  }, palette);
}
async function drawnText(plot: Locator) {
  return plot.locator("canvas").evaluate((canvas) =>
    (canvas as HTMLCanvasElement & { drawnText?: string[] }).drawnText || []);
}
async function saveScreenshot(page: Page, name: string) {
  const directory = process.env.METRICSPANEL_TIMESERIES_QA_DIR;
  if (!directory) return;
  await mkdir(directory, { recursive: true });
  await page.screenshot({ path: path.join(directory, name), animations: "disabled", fullPage: true });
}

test("SDK time series renders twenty colored series, log axes, percent stacks and interactive legend", async ({ page, endpoint }) => {
  test.setTimeout(60000);
  const issues = errors(page);
  await open(page, endpoint, "Time series controls", [panel(7, "Twenty series", 20),
    panel(8, "Log axes", 12, "log", 15), panel(9, "Percent stack", 12, "percent", 30)]);
  const twenty = page.getByRole("region", { name: "Twenty series", exact: true });
  await expect(twenty.locator(".uplot canvas")).toBeVisible();
  await expect(twenty).toHaveAttribute("data-series-count", "20");
  await expect.poll(async () => (await colorCounts(twenty)).every((count) => count > 2)).toBe(true);
  const hoverArea = await twenty.locator(".u-over").boundingBox();
  await page.mouse.move(hoverArea!.x + hoverArea!.width * 0.55, hoverArea!.y + hoverArea!.height * 0.5);
  const tooltip = page.locator(".grafana-timeseries-tooltip");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toContainText("Series 20");
  expect((await tooltip.boundingBox())!.height).toBeLessThanOrEqual(280);
  await page.mouse.move(0, 0);
  await expect(twenty.locator("button").filter({ hasText: /^Series 20$/ })).toBeVisible();
  const legend = twenty.locator("button").filter({ hasText: /^Series 01$/ });
  const canvasBox = await twenty.locator("canvas").boundingBox();
  const legendBox = await legend.boundingBox();
  expect(legendBox!.x).toBeGreaterThan(canvasBox!.x + canvasBox!.width);
  await legend.click();
  await expect.poll(async () => (await colorCounts(twenty)).map((count) => count > 2)).toEqual(
    colors.map((_, index) => index === 0));
  await twenty.locator("button").filter({ hasText: /^Series 02$/ }).click({ modifiers: ["Control"] });
  await expect.poll(async () => (await colorCounts(twenty)).map((count) => count > 2)).toEqual(
    colors.map((_, index) => index < 2));
  await legend.click();
  await legend.click();
  await expect.poll(async () => (await colorCounts(twenty)).every((count) => count > 2)).toBe(true);
  const log = page.getByRole("region", { name: "Log axes", exact: true });
  await log.scrollIntoViewIfNeeded();
  await expect.poll(() => drawnText(log)).toContain("Left log");
  await expect.poll(() => drawnText(log)).toContain("Right log");
  await expect.poll(async () => (await colorCounts(log, colors.slice(0, 12))).every((count) => count > 2)).toBe(true);
  const stacked = page.getByRole("region", { name: "Percent stack", exact: true });
  await stacked.scrollIntoViewIfNeeded();
  await expect.poll(() => drawnText(stacked)).toContain("Percent stacked");
  await expect.poll(async () => (await drawnText(stacked)).some((text) => /^100(?:\.0+)?%$/.test(text))).toBe(true);
  await expect.poll(async () => (await colorCounts(stacked, colors.slice(0, 12))).every((count) => count > 2)).toBe(true);
  await saveScreenshot(page, "time-series-options-desktop.png");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(issues).toEqual([]);
});

test("annotations clip at plot boundaries and panel/dashboard zoom preserves saved source", async ({ page, endpoint }) => {
  const issues = errors(page);
  const saved = await open(page, endpoint, "Annotation zoom", [panel(7, "Annotated series", 12)], true);
  const before = await (await page.request.get(endpoint + `/api/v1/dashboard-exports/${saved.id}?format=native`)).text();
  for (const event of [
    { text: "Middle point", time: start + 0.5 * (end - start), timeEnd: start + 0.5 * (end - start) },
    { text: "Left boundary region", time: start - 10000, timeEnd: start + 0.3 * (end - start) },
    { text: "Right boundary region", time: start + 0.7 * (end - start), timeEnd: end + 10000 },
  ]) {
    const response = await page.request.post(endpoint + "/api/annotations", { data: {
      ...event, dashboardUID: saved.id, panelId: 7, tags: ["timeseries-test"],
    } });
    expect(response.ok(), await response.text()).toBeTruthy();
  }
  await page.reload();
  const plot = page.getByRole("region", { name: "Annotated series", exact: true });
  await expect(plot.locator(".uplot canvas")).toBeVisible();
  const point = plot.getByRole("img", { name: "Annotation: Middle point", exact: true });
  const left = plot.getByRole("img", { name: "Annotation: Left boundary region", exact: true });
  const right = plot.getByRole("img", { name: "Annotation: Right boundary region", exact: true });
  await expect(point).toBeVisible();
  await expect(left.locator("rect")).toHaveAttribute("x", "0");
  await expect(right.locator("rect")).toBeVisible();
  expect(Number(await right.locator("rect").getAttribute("x")) + Number(await right.locator("rect").getAttribute("width")))
    .toBeCloseTo(Number(await plot.locator(".grafana-timeseries-annotations").getAttribute("width")), 2);
  await plot.getByRole("button", { name: "Annotations", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Annotations", exact: true })).toContainText("Middle point");
  await page.getByRole("dialog", { name: "Annotations", exact: true }).getByRole("button", { name: "Close", exact: true }).click();
  const initialURL = page.url();
  await plot.getByLabel("Zoom scope", { exact: true }).selectOption("panel");
  const drag = async () => {
    const area = await plot.locator(".u-over").boundingBox();
    await page.mouse.move(area!.x + area!.width * 0.25, area!.y + area!.height * 0.45);
    await page.mouse.down();
    await page.mouse.move(area!.x + area!.width * 0.6, area!.y + area!.height * 0.45, { steps: 8 });
    await page.mouse.up();
  };
  await drag();
  await expect(plot.getByRole("button", { name: "Reset panel zoom", exact: true })).toBeVisible();
  expect(page.url()).toBe(initialURL);
  await expect(right).toHaveCount(0);
  await expect(point).toBeVisible();
  await page.getByRole("button", { name: "Refresh metrics", exact: true }).click();
  await expect(plot.getByRole("button", { name: "Reset panel zoom", exact: true })).toBeVisible();
  await expect(right).toHaveCount(0);
  expect(page.url()).toBe(initialURL);
  await plot.getByRole("button", { name: "Reset panel zoom", exact: true }).click();
  await expect(right).toBeVisible();
  await plot.getByLabel("Zoom scope", { exact: true }).selectOption("dashboard");
  await drag();
  await expect.poll(() => new URL(page.url()).searchParams.get("from")).not.toBe(String(start));
  const from = Number(new URL(page.url()).searchParams.get("from"));
  const to = Number(new URL(page.url()).searchParams.get("to"));
  expect(Number.isSafeInteger(from) && Number.isSafeInteger(to)).toBe(true);
  expect(from).toBeGreaterThan(start);
  expect(to).toBeLessThan(end);
  await expect(right).toHaveCount(0);
  await expect(point).toBeVisible();
  await expect(plot.getByRole("alert")).toHaveCount(0);
  const after = await (await page.request.get(endpoint + `/api/v1/dashboard-exports/${saved.id}?format=native`)).text();
  expect(after).toBe(before);
  await saveScreenshot(page, "time-series-annotations-zoom-desktop.png");
  expect(issues).toEqual([]);
});

test("comparison frames align on the current time range and retain dashed styling without rewriting source", async ({ page, endpoint }) => {
  const issues = errors(page);
  const current = panel(7, "Time comparison", 1);
  const snapshot = current.config.targets[0].snapshot;
  const compared = JSON.parse(JSON.stringify(snapshot[0]));
  compared.schema.name = "Previous day";
  compared.schema.refId = "A-compare";
  compared.schema.meta = { timeCompare: { isTimeShiftQuery: true, diffMs: -86400000 } };
  compared.schema.fields[1].name = "Compared";
  compared.schema.fields[1].config.color.fixedColor = colors[1];
  compared.data.values[0] = compared.data.values[0].map((time: number) => time - 86400000);
  compared.data.values[1] = compared.data.values[1].map((value: number) => value * 1.8);
  snapshot.push(compared);
  const saved = await open(page, endpoint, "Comparison frames", [current]);
  const plot = page.getByRole("region", { name: "Time comparison", exact: true });
  await expect(plot.locator(".uplot canvas")).toBeVisible();
  await expect(plot).toHaveAttribute("data-series-count", "2");
  await expect.poll(async () => (await colorCounts(plot, colors.slice(0, 2))).every((count) => count > 2)).toBe(true);
  await expect(plot.locator("button").filter({ hasText: /Compared \(Comparison\)$/ })).toBeVisible();
  await page.getByRole("button", { name: "Switch language", exact: true }).click();
  await expect(plot.locator("button").filter({ hasText: /Compared \(比较\)$/ })).toBeVisible();
  await page.getByRole("button", { name: "Switch language", exact: true }).click();
  await expect(plot.locator("button").filter({ hasText: /Compared \(Comparison\)$/ })).toBeVisible();
  await expect.poll(() => plot.locator("canvas").evaluate((canvas, color) =>
    ((canvas as HTMLCanvasElement & { drawnStrokes?: { color: string; dash: number[] }[] }).drawnStrokes || [])
      .some((stroke) => stroke.color === color && stroke.dash.join(",") === "1,5,4,5"), colors[1])).toBe(true);
  const exported = await (await page.request.get(endpoint + `/api/v1/dashboard-exports/${saved.id}?format=native`)).json();
  const stored = exported.panels[0].config.targets[0].snapshot;
  expect(stored[1].data.values[0][0]).toBe(start - 86400000);
  expect(stored[1].schema.fields[1].config.custom.lineStyle).toBeUndefined();
  expect(stored[1].schema.meta.timeCompare.diffMs).toBe(-86400000);
  await saveScreenshot(page, "time-series-comparison-desktop.png");
  expect(issues).toEqual([]);
});
