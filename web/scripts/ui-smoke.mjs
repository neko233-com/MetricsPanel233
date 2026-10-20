// Headless smoke test for the @grafana/ui foundation: theme provider, modal
// rendering and runtime icon assets. Requires a webui build:
//   go build -tags webui -o bin/metricspanel.exe ./cmd/metricspanel
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
  path.join(
    repoRoot,
    "bin",
    process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
  );
const temp = await mkdtemp(path.join(os.tmpdir(), "mp-ui-smoke-"));
const server = spawn(
  binary,
  ["serve", "--listen", "127.0.0.1:0", "--db", path.join(temp, "control.db")],
  {
    cwd: repoRoot,
    windowsHide: true,
    env: { ...process.env, METRICSPANEL_TOKEN: "", METRICSPANEL_STORAGE: "sqlite" },
  },
);
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
while (!endpoint && Date.now() < deadline) await new Promise((r) => setTimeout(r, 100));
if (!endpoint) {
  console.error("server did not start\n" + logs.join("\n"));
  process.exit(1);
}
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1536, height: 1024 } });
const errors = [];
page.on("pageerror", (error) => errors.push(`pageerror: ${error}`));
page.on("console", (message) => {
  if (
    message.type() === "error" &&
    !/favicon|Failed to load resource/i.test(message.text())
  ) {
    errors.push(`console: ${message.text()}`);
  }
});
page.on("response", (response) => {
  // Token mode has no /api/auth/status endpoint; the SPA treats 404 as legacy.
  if (
    response.status() >= 400 &&
    !response.url().endsWith("/api/auth/status")
  ) {
    errors.push(`http ${response.status()}: ${response.url()}`);
  }
});
try {
  await page.goto(endpoint, { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".app-shell", { timeout: 15000 });
  await page.waitForSelector("#getting-started-title", { timeout: 10000 });
  const nav = await page.locator("aside nav button").count();
  await page.locator("aside nav button").nth(1).click();
  await page.getByRole("button", { name: /Import dashboard|导入仪表盘/ }).click();
  const dialog = page.getByRole("dialog").first();
  await dialog.waitFor({ state: "visible", timeout: 10000 });
  const named = await page
    .getByRole("dialog", { name: /Import dashboard|导入仪表盘/ })
    .count();
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden", timeout: 10000 });
  console.log(JSON.stringify({ endpoint, nav, named, errors }, null, 2));
  if (!named || errors.length) process.exitCode = 1;
} finally {
  await browser.close();
  server.kill();
  await rm(temp, { recursive: true, force: true, maxRetries: 3 });
}
