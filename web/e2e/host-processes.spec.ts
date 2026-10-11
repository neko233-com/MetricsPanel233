import { test as base, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, rm, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
type Host = { url: string; pid: number; token: string };
const test = base.extend<{}, { host: Host }>({
  host: [async ({}, use) => {
    const binary = process.env.METRICSPANEL_TEST_BINARY || path.join(repoRoot, "bin", process.platform === "win32" ? "metricspanel.exe" : "metricspanel");
    if (!(await stat(binary)).isFile()) throw new Error("Process fixture binary must be a regular file");
    const directory = await mkdtemp(path.join(os.tmpdir(), "metricspanel233-process-e2e-"));
    const token = "host-process-e2e-token";
    const service = spawn(binary, ["serve", "--listen", "127.0.0.1:0", "--db", path.join(directory, "control.db")], {
      cwd: repoRoot, windowsHide: true,
      env: { ...process.env, METRICSPANEL_TOKEN: token, METRICSPANEL_STORAGE: "sqlite" },
    });
    let url = "", pending = "", startError: Error | null = null;
    const closed = new Promise<void>((resolve) => service.once("close", () => resolve()));
    service.on("error", (error) => { startError = error; });
    service.stderr.on("data", (chunk) => {
      pending += chunk.toString();
      let end: number;
      while ((end = pending.indexOf("\n")) !== -1) {
        const line = pending.slice(0, end); pending = pending.slice(end + 1);
        try { const entry = JSON.parse(line); if (entry.address) url = `http://${entry.address}`; } catch { /* Other log lines. */ }
      }
    });
    try {
      await expect.poll(() => { if (startError) throw startError; return url; }, { timeout: 15000 }).not.toBe("");
      await use({ url, pid: service.pid!, token });
    } finally {
      if (service.exitCode === null) {
        service.kill();
      }
      await closed;
      const target = path.resolve(directory);
      if (!target.startsWith(path.resolve(os.tmpdir()) + path.sep) || !path.basename(target).startsWith("metricspanel233-process-e2e-"))
        throw new Error("Unsafe process fixture cleanup target");
      await rm(target, { recursive: true, force: true, maxRetries: 3 });
    }
  }, { scope: "worker" }],
});

const source = { id: "fixture-host", hostname: "collector-node", os: "windows", scope: "host", capabilities: { cpu: true, memory: true, icons: true } };
const processItem = (pid: number, name: string, fields: Record<string, unknown> = {}) => ({
  id: `process-${pid}`, pid, ppid: 1, name, nameSource: "native", startToken: `start-${pid}`,
  startedAt: 1791710400000, cpuSecondsTotal: 2, cpuPercent: 0.5, memoryRssBytes: "10485760", threads: 3, iconKey: `icon-${pid}`,
  states: { startTime: "ok", cpu: "ok", memory: "ok" }, ...fields,
});
const processPage = (items: ReturnType<typeof processItem>[], fields: Record<string, unknown> = {}) => ({
  source, snapshotId: "snapshot-one", sampledAt: Date.now(), expiresAt: Date.now() + 60000,
  prefix: "", order: "name_asc_pid_asc", total: items.length, items, continueToken: "", partial: false, warnings: [], ...fields,
});
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aC9sAAAAASUVORK5CYII=", "base64");
async function open(page: Page, host: Host, locale = "en") {
  await page.addInitScript(({ locale, token }) => { localStorage.setItem("metricspanel-locale", locale); sessionStorage.setItem("metricspanel-token", token); }, { locale, token: host.token });
  await page.goto(`${host.url}/?refresh=#processes`);
  await expect(page.locator(".processes-panel")).toBeVisible();
  await page.locator(".process-refresh-controls").getByRole("combobox").selectOption("0");
}
async function screenshot(page: Page, name: string) {
  if (!process.env.METRICSPANEL_PROCESS_QA_DIR) return;
  const directory = path.resolve(process.env.METRICSPANEL_PROCESS_QA_DIR);
  if (directory === repoRoot || directory.startsWith(repoRoot + path.sep)) throw new Error("Process QA must stay outside the repository");
  await mkdir(directory, { recursive: true });
  await page.screenshot({ path: path.join(directory, name), fullPage: false, animations: "disabled" });
}
function watch(page: Page, allowed: number[] = []) {
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (!["error", "warning"].includes(entry.type())) return;
    if (entry.location().url.includes("/api/v1/host/") && allowed.some((status) => entry.text().includes(`status of ${status}`))) return;
    issues.push(entry.text());
  });
  return issues;
}
async function pageChecks(page: Page, host: Host, heading: string) {
  expect(page.url()).toContain(host.url); expect(page.url()).toContain("#processes");
  await expect(page).toHaveTitle(/MetricsPanel233/);
  await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
  expect(await page.locator("main").innerText()).not.toBe("");
  await expect(page.locator("vite-error-overlay")).toHaveCount(0);
}

test("Host processes read the protected native host, preserve its PID identity and return PNG icons", async ({ page, request, host }) => {
  const issues = watch(page);
  expect((await request.get(`${host.url}/api/v1/host/processes`)).status()).toBe(401);
  const headers = { Authorization: `Bearer ${host.token}` };
  const response = await request.get(`${host.url}/api/v1/host/processes?limit=1000`, { headers });
  expect(response.ok()).toBe(true);
  const list = await response.json();
  expect(list.order).toBe("name_asc_pid_asc"); expect(list.items.length).toBeGreaterThan(0);
  const native = list.items.find((item: { pid: number }) => item.pid === host.pid);
  expect(native).toBeTruthy(); expect(native.name).not.toBe("");
  const detail = await request.get(`${host.url}/api/v1/host/processes/${host.pid}?startToken=${encodeURIComponent(native.startToken)}`, { headers });
  expect(detail.ok()).toBe(true); expect((await detail.json()).process.id).toBe(native.id);
  expect((await request.get(`${host.url}/api/v1/host/process-icons/${native.iconKey}`)).status()).toBe(401);
  const icon = await request.get(`${host.url}/api/v1/host/process-icons/${native.iconKey}`, { headers });
  expect(icon.ok()).toBe(true); expect(icon.headers()["content-type"]).toContain("image/png");
  expect((await icon.body()).subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  await open(page, host);
  await expect(page.locator(".process-table tbody tr").first()).toBeVisible();
  await expect(page.locator('.process-icon[data-icon="loaded"]').first()).toBeVisible();
  await screenshot(page, "host-processes-native-list-en-desktop.png");
  await page.getByLabel("Name prefix", { exact: true }).fill(native.name);
  await page.getByRole("button", { name: "Filter processes", exact: true }).click();
  const row = page.locator(".process-table tbody tr").filter({ has: page.locator("td:nth-child(2)", { hasText: new RegExp(`^${host.pid}$`) }) });
  await expect(row).toBeVisible(); await expect(row.locator(".process-name")).toHaveText(native.name);
  await row.locator(".process-name").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: native.name, exact: true })).toBeVisible();
  await expect(dialog).toContainText(list.source.hostname);
  await screenshot(page, "host-processes-native-detail-en-desktop.png");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await pageChecks(page, host, "Processes");
  await screenshot(page, "host-processes-native-en-desktop.png");
  expect(issues).toEqual([]);
});

test("Process metrics and icons refresh without moving rows while name filters and PID reuse are explicit", async ({ page, host }) => {
  const issues = watch(page, [404, 409]);
  let sample = 0;
  let detailReads = 0;
  const calls: string[] = [], iconAuth: string[] = [];
  const initial = [processItem(8, "Alpha"), processItem(10, "alpha"), processItem(3, "Beta")];
  await page.route("**/api/v1/host/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.includes("/process-icons/")) {
      iconAuth.push(route.request().headers().authorization);
      return url.pathname.endsWith("icon-10") ? route.fulfill({ status: 404, json: { error: "not found" } }) : route.fulfill({ contentType: "image/png", body: png });
    }
    if (/\/processes\/\d+$/.test(url.pathname)) {
      if (url.pathname.endsWith("/50")) {
        detailReads++;
        if (detailReads === 1) {
          expect(url.searchParams.has("startToken")).toBe(false);
          return route.fulfill({ json: { source, sampledAt: Date.now(), process: processItem(50, "PID lookup original", { startToken: "lookup-start-A" }) } });
        }
        expect(url.searchParams.get("startToken")).toBe("lookup-start-A");
        return route.fulfill({ status: 409, json: { error: "Current start token is lookup-start-B" } });
      }
      expect(url.searchParams.get("startToken")).toBe("start-8");
      return route.fulfill({ status: 409, json: { error: "PID reused" } });
    }
    const prefix = url.searchParams.get("prefix") || ""; calls.push(prefix);
    const items = sample === 0 ? initial : sample === 1 ? [...initial].reverse().map((item) => ({ ...item, cpuPercent: 88.8, memoryRssBytes: "18446744073709551615" })) : [initial[1], initial[2], { ...initial[0], name: "Zulu" }];
    return route.fulfill({ json: processPage(items.filter((item) => item.name.toLowerCase().startsWith(prefix.toLowerCase())), { prefix: prefix.toLowerCase() }) });
  });
  await open(page, host);
  const rows = page.locator(".process-table tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(page.locator('[data-process-id="process-8"] .process-icon')).toHaveAttribute("data-icon", "loaded");
  await expect(page.locator('[data-process-id="process-10"] .process-icon')).toHaveAttribute("data-icon", "generic");
  const identities = await rows.evaluateAll((entries) => entries.map((entry) => entry.getAttribute("data-process-id")));
  sample = 1; await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(rows.first()).toContainText("88.8%");
  expect(await rows.evaluateAll((entries) => entries.map((entry) => entry.getAttribute("data-process-id")))).toEqual(identities);
  await expect(rows.first()).toContainText("16 EiB");
  await page.locator('[data-process-id="process-8"] .process-name').click();
  await expect(page.getByRole("dialog")).toContainText("PID now belongs to another process");
  await expect(page.getByRole("dialog").getByRole("button", { name: "Refresh", exact: true })).toHaveCount(0);
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  await page.getByLabel("PID", { exact: true }).fill("50");
  await page.getByRole("button", { name: "Find PID", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("PID lookup original");
  await page.getByRole("dialog").getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("PID now belongs to another process");
  await expect(page.getByRole("dialog")).not.toContainText("lookup-start-B");
  expect(detailReads).toBe(2);
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  sample = 2; await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(rows.last()).toContainText("Zulu");
  const beforeTyping = calls.length;
  await page.getByLabel("Name prefix", { exact: true }).fill("Z");
  expect(calls.length).toBe(beforeTyping);
  await page.getByRole("button", { name: "Filter processes", exact: true }).click();
  await expect(rows).toHaveCount(1); await expect(rows.first()).toContainText("Zulu");
  await page.getByRole("group", { name: "Prefix shortcuts" }).getByRole("button", { name: "B", exact: true }).click();
  await expect(rows).toHaveCount(1); await expect(rows.first()).toContainText("Beta");
  await pageChecks(page, host, "Processes");
  await screenshot(page, "host-processes-filtered-en-desktop.png");
  expect(iconAuth.length).toBeGreaterThan(0); expect(iconAuth.every((value) => value === `Bearer ${host.token}`)).toBe(true);
  expect(issues).toEqual([]);
});

test("Process pagination refreshes the loaded snapshot and reports expired cursors without discarding rows", async ({ page, host }) => {
  const issues = watch(page, [410, 503]);
  let failRead = true, expired = false, sample = 0;
  const first = [processItem(8, "Alpha"), processItem(10, "Beta")], second = [processItem(20, "Gamma")];
  await page.route("**/api/v1/host/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.includes("process-icons")) return route.fulfill({ contentType: "image/png", body: png });
    if (failRead) return route.fulfill({ status: 503, json: { error: "Process source unavailable" } });
    if (url.searchParams.get("continue") && expired) return route.fulfill({ status: 410, json: { error: "expired" } });
    const items = url.searchParams.get("continue") ? second : first;
    return route.fulfill({ json: processPage(items.map((item) => ({ ...item, cpuPercent: sample })), { snapshotId: `snapshot-${sample}`, total: 3, continueToken: url.searchParams.get("continue") ? "" : "next-page" }) });
  });
  await open(page, host);
  await expect(page.getByRole("alert")).toContainText("Process source unavailable");
  failRead = false; await page.getByRole("button", { name: "Retry", exact: true }).click();
  const rows = page.locator(".process-table tbody tr");
  await expect(rows).toHaveCount(2);
  expired = true; await page.getByRole("button", { name: "Load more", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Process snapshot expired"); await expect(rows).toHaveCount(2);
  expired = false; await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.getByRole("button", { name: "Load more", exact: true }).click(); await expect(rows).toHaveCount(3);
  sample = 2; await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(rows).toHaveCount(3); await expect(rows.last()).toContainText("2.0%");
  await expect(page.getByRole("button", { name: "Load more", exact: true })).toHaveCount(0);
  await screenshot(page, "host-processes-pagination-en-desktop.png");
  expect(issues).toEqual([]);
});

test("Chinese desktop processes show host scope and unavailable fields with readable PID details", async ({ page, host }) => {
  const issues = watch(page);
  const item = processItem(4294967295, "数据库工作进程-long-worker", { cpuPercent: null, memoryRssBytes: null, startedAt: null, startToken: "", iconKey: "", states: { startTime: "denied", cpu: "warming_up", memory: "denied" } });
  await page.setViewportSize({ width: 1536, height: 1024 });
  await page.route("**/api/v1/host/processes**", async (route) => {
    const url = new URL(route.request().url());
    if (/\/processes\/\d+$/.test(url.pathname)) return route.fulfill({ json: { source, sampledAt: Date.now(), process: item } });
    const prefix = url.searchParams.get("prefix") || "";
    return route.fulfill({ json: processPage(prefix && !item.name.startsWith(prefix) ? [] : [item], { prefix }) });
  });
  await open(page, host, "zh");
  await pageChecks(page, host, "进程");
  await expect(page.locator(".process-source")).toContainText("主机命名空间");
  await expect(page.locator(".process-table")).toContainText("首次采样");
  await expect(page.locator(".process-table tbody tr")).toContainText("4294967295");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await screenshot(page, "host-processes-zh-desktop.png");
  await page.getByLabel("名称前缀", { exact: true }).fill("汉".repeat(43));
  await page.getByRole("button", { name: "筛选进程", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("名称前缀不能超过 128 个 UTF-8 字节");
  await expect(page.locator(".process-table tbody tr")).toHaveCount(1);
  await page.getByLabel("名称前缀", { exact: true }).fill("");
  await page.getByLabel("PID", { exact: true }).fill("not-a-pid"); await page.getByRole("button", { name: "查找 PID", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("请输入有效的数字 PID");
  await page.getByLabel("PID", { exact: true }).fill("4294967295"); await page.getByRole("button", { name: "查找 PID", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("无读取权限"); await expect(dialog).toContainText("PID 可能已被复用");
  expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await screenshot(page, "host-processes-detail-zh-desktop.png");
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByLabel("名称前缀", { exact: true }).fill("none"); await page.getByRole("button", { name: "筛选进程", exact: true }).click();
  await expect(page.getByText("没有符合此前缀的进程", { exact: true })).toBeVisible();
  expect(issues).toEqual([]);
});
