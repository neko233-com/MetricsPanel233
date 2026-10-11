import { test as base, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-catalog-e2e-"),
      );
      const binary =
        process.env.METRICSPANEL_TEST_BINARY ||
        path.join(
          repoRoot,
          "bin",
          process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
        );
      const server = spawn(
        binary,
        [
          "serve",
          "--listen",
          "127.0.0.1:0",
          "--db",
          path.join(temp, "control.db"),
        ],
        {
          cwd: repoRoot,
          windowsHide: true,
          env: {
            ...process.env,
            METRICSPANEL_TOKEN: "",
            METRICSPANEL_STORAGE: "sqlite",
          },
        },
      );
      let endpoint = "",
        pending = "",
        logs = "";
      server.stderr.on("data", (chunk) => {
        logs += chunk.toString();
        pending += chunk.toString();
        let end: number;
        while ((end = pending.indexOf("\n")) !== -1) {
          const line = pending.slice(0, end);
          pending = pending.slice(end + 1);
          try {
            const item = JSON.parse(line);
            if (item.address) endpoint = `http://${item.address}`;
          } catch {
            /* Other log lines do not publish the listener address. */
          }
        }
      });
      try {
        await expect
          .poll(() => endpoint, { timeout: 15000, message: logs })
          .not.toBe("");
        await use(endpoint);
      } finally {
        if (server.exitCode === null) {
          const exited = new Promise<void>((resolve) =>
            server.once("exit", () => resolve()),
          );
          server.kill();
          await exited;
        }
        const target = path.resolve(temp);
        if (
          !target.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path.basename(target).startsWith("metricspanel233-catalog-e2e-")
        )
          throw new Error("Unsafe catalog fixture cleanup target");
        await rm(target, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

const clock = {
  id: "grafana-clock-panel",
  name: "Clock",
  type: "panel",
  version: "3.2.4",
  description: "Displays the current time.",
  author: "Grafana Labs",
  downloads: 123456,
  deprecated: false,
  angular_detected: false,
  grafana_dependency: ">=10.4.0",
  url: "https://grafana.com/grafana/plugins/grafana-clock-panel/",
  backend: false,
};
const legacy = {
  ...clock,
  id: "community-legacy-datasource",
  name: "Legacy datasource",
  type: "datasource",
  description: "<img src=x onerror=alert('unsafe')>",
  deprecated: true,
  angular_detected: true,
  backend: true,
};
const renderer = {
  ...clock,
  id: "grafana-image-renderer",
  name: "Image renderer",
  type: "renderer",
  deprecated: true,
};
const versions = [
  {
    version: "4.0.0-beta.1",
    created_at: "2026-10-11T00:00:00Z",
    grafana_dependency: ">=13.0.0",
    angular_detected: false,
    platform_available: true,
  },
  {
    version: "3.2.4",
    created_at: "2026-10-01T00:00:00Z",
    grafana_dependency: ">=10.4.0",
    angular_detected: false,
    platform_available: true,
  },
  {
    version: "3.2.3",
    created_at: "2026-09-01T00:00:00Z",
    grafana_dependency: ">=10.4.0",
    angular_detected: false,
    platform_available: true,
  },
  {
    version: "2.0.0",
    created_at: "2025-01-01T00:00:00Z",
    grafana_dependency: ">=8.0.0",
    angular_detected: true,
    platform_available: true,
  },
  {
    version: "1.0.0",
    created_at: "2024-01-01T00:00:00Z",
    grafana_dependency: ">=7.0.0",
    angular_detected: false,
    platform_available: false,
  },
];
const installedClock = (version: string) => ({
  id: clock.id,
  name: clock.name,
  type: clock.type,
  version,
  backend: false,
  enabled: true,
  signature: "valid",
  sha256: "fixture",
  package_id: clock.id,
  asset_path: "",
  metadata: {},
});
async function screenshot(page: Page, name: string) {
  if (!process.env.METRICSPANEL_CATALOG_QA_DIR) return;
  const directory = path.resolve(process.env.METRICSPANEL_CATALOG_QA_DIR);
  if (directory === repoRoot || directory.startsWith(repoRoot + path.sep))
    throw new Error("Catalog screenshots must stay outside the repository");
  await mkdir(directory, { recursive: true });
  await page.screenshot({ path: path.join(directory, name), fullPage: false });
}
function watchConsole(page: Page, issues: string[], expectedStatus: number) {
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (!["warning", "error"].includes(entry.type())) return;
    const expectedFailure =
      entry.type() === "error" &&
      entry.location().url.includes("/api/v1/plugins/catalog") &&
      entry.text().includes(`status of ${expectedStatus}`);
    if (!expectedFailure) issues.push(entry.text());
  });
}

test("Plugin library filters the official catalog and submits an exact version after a signature failure", async ({
  page,
  endpoint,
}) => {
  const calls: string[] = [],
    installs: unknown[] = [],
    issues: string[] = [];
  let localVersion = "3.2.3",
    failInstall = true;
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  watchConsole(page, issues, 400);
  await page.route(
    /\/api\/v1\/(?:plugins|plugin-catalog)(?:[/?]|$)/,
    async (route) => {
      const url = new URL(route.request().url()),
        method = route.request().method();
      if (method === "GET" && url.pathname === "/api/v1/plugins")
        return route.fulfill({ json: [installedClock(localVersion)] });
      if (url.pathname === "/api/v1/plugins/catalog" && method === "GET") {
        calls.push(url.search);
        let items = [clock, legacy];
        if (url.searchParams.get("type"))
          items = items.filter(
            (item) => item.type === url.searchParams.get("type"),
          );
        if (url.searchParams.get("q"))
          items = items.filter((item) =>
            `${item.id} ${item.name}`
              .toLowerCase()
              .includes(url.searchParams.get("q")!.toLowerCase()),
          );
        return route.fulfill({ json: { items } });
      }
      if (url.pathname === `/api/v1/plugin-catalog/${clock.id}`)
        return route.fulfill({ json: clock });
      if (url.pathname === `/api/v1/plugin-catalog/${clock.id}/versions`)
        return route.fulfill({ json: { items: versions } });
      if (url.pathname === "/api/v1/plugins/catalog" && method === "POST") {
        const payload = route.request().postDataJSON();
        installs.push(payload);
        if (failInstall) {
          failInstall = false;
          return route.fulfill({
            status: 400,
            json: { error: "Plugin signature verification failed" },
          });
        }
        localVersion = payload.version;
        return route.fulfill({ json: installedClock(localVersion) });
      }
      return route.continue();
    },
  );
  await page.goto(endpoint + "/#plugins");
  await expect(page).toHaveTitle("MetricsPanel233 · Local observability");
  await expect(
    page.getByRole("heading", { name: "Plugins & datasources" }),
  ).toBeVisible();
  await expect(page.locator(".plugin-row")).toContainText("3.2.3");
  expect(calls).toHaveLength(0);
  const installedTab = page.getByRole("tab", {
    name: "Installed plugins",
    exact: true,
  });
  const libraryTab = page.getByRole("tab", {
    name: "Plugin library",
    exact: true,
  });
  await installedTab.focus();
  await installedTab.press("ArrowRight");
  await expect(libraryTab).toBeFocused();
  await expect(libraryTab).toHaveAttribute("aria-selected", "true");
  const library = page.getByRole("tabpanel", {
    name: "Plugin library",
    exact: true,
  });
  await expect(library.locator("tbody tr")).toHaveCount(2);
  await expect(
    library.getByText(legacy.description, { exact: true }),
  ).toBeVisible();
  await expect(library.locator("img")).toHaveCount(0);
  await library.getByLabel("Search plugins", { exact: true }).fill("Clock");
  expect(calls).toHaveLength(1);
  await library
    .getByRole("combobox", { name: "Type", exact: true })
    .selectOption("panel");
  await expect(library.locator("tbody tr")).toHaveCount(1);
  expect(calls.at(-1)).toBe("?type=panel");
  await library.getByRole("button", { name: "Search", exact: true }).click();
  await expect.poll(() => calls.at(-1)).toBe("?q=Clock&type=panel");
  await screenshot(page, "plugin-library-list-en-desktop.png");
  await library.getByRole("button", { name: "Details", exact: true }).click();
  const dialog = page.getByRole("dialog", {
    name: "Plugin details · Clock",
    exact: true,
  });
  await expect(
    dialog.getByRole("combobox", { name: "Version", exact: true }),
  ).toHaveValue("3.2.4");
  await expect(dialog).toContainText(">=10.4.0");
  await expect(dialog).toContainText(
    "Catalog metadata does not confirm runtime compatibility.",
  );
  await screenshot(page, "plugin-library-en-desktop.png");
  await dialog
    .getByRole("button", { name: "Update plugin", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toHaveText(
    "Plugin signature verification failed",
  );
  await expect(dialog).toBeVisible();
  await dialog
    .getByRole("button", { name: "Update plugin", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  expect(installs).toEqual([
    { id: clock.id, version: "3.2.4" },
    { id: clock.id, version: "3.2.4" },
  ]);
  await expect(library.locator("tbody tr")).toContainText("3.2.4");
  await libraryTab.focus();
  await libraryTab.press("Home");
  await expect(installedTab).toBeFocused();
  await expect(page.locator(".plugin-row")).toContainText("3.2.4");
  await expect(page.locator(".plugin-sources")).toBeVisible();
  await page
    .getByRole("button", { name: "Install plugin", exact: true })
    .click();
  const manual = page.getByRole("dialog", {
    name: "Install plugin",
    exact: true,
  });
  await expect(manual.getByLabel("Plugin ID", { exact: true })).toBeVisible();
  await expect(manual.getByLabel("Or upload a ZIP package")).toBeVisible();
  await manual.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.locator("vite-error-overlay")).toHaveCount(0);
  expect(issues).toEqual([]);
});

test("Plugin library reports empty and error states, rejects unsupported versions, and fits Chinese mobile", async ({
  page,
  endpoint,
}) => {
  const issues: string[] = [];
  let failList = true,
    writes = 0;
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "zh"),
  );
  watchConsole(page, issues, 502);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route(
    /\/api\/v1\/(?:plugins|plugin-catalog)(?:[/?]|$)/,
    async (route) => {
      const url = new URL(route.request().url()),
        method = route.request().method();
      if (method !== "GET") {
        writes++;
        return route.fulfill({
          status: 500,
          json: { error: "Unexpected write" },
        });
      }
      if (url.pathname === "/api/v1/plugins")
        return route.fulfill({ json: [] });
      if (url.pathname === "/api/v1/plugins/catalog") {
        if (failList) {
          failList = false;
          return route.fulfill({
            status: 502,
            json: { error: "Official catalog unavailable" },
          });
        }
        return route.fulfill({
          json: { items: url.searchParams.has("q") ? [] : [legacy, renderer] },
        });
      }
      if (url.pathname.endsWith("/versions"))
        return route.fulfill({
          json: {
            items: url.pathname.includes(renderer.id)
              ? versions.slice(1, 2)
              : versions.slice(3),
          },
        });
      if (url.pathname === `/api/v1/plugin-catalog/${legacy.id}`)
        return route.fulfill({ json: legacy });
      if (url.pathname === `/api/v1/plugin-catalog/${renderer.id}`)
        return route.fulfill({ json: renderer });
      return route.continue();
    },
  );
  await page.goto(endpoint + "/#plugins");
  await page.getByRole("tab", { name: "插件库", exact: true }).click();
  const library = page.getByRole("tabpanel", { name: "插件库", exact: true });
  await expect(library.getByRole("alert")).toHaveText(
    "Official catalog unavailable",
  );
  await library.getByRole("button", { name: "重试", exact: true }).click();
  await expect(library.locator("tbody tr")).toHaveCount(2);
  await library.getByLabel("搜索插件", { exact: true }).fill("missing");
  await library.getByRole("button", { name: "搜索", exact: true }).click();
  await expect(library).toContainText("没有符合筛选的插件");
  await library.getByLabel("搜索插件", { exact: true }).fill("");
  await library.getByRole("button", { name: "搜索", exact: true }).click();
  await library
    .getByRole("button", { name: "Legacy datasource", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "插件详情 · Legacy datasource",
    exact: true,
  });
  await expect(dialog).toContainText("已弃用");
  await expect(
    dialog.getByRole("combobox", { name: "版本", exact: true }),
  ).toHaveValue("2.0.0");
  await expect(dialog).toContainText("检测到 Angular 代码，当前不支持此版本。");
  await expect(
    dialog.getByRole("button", { name: "安装插件", exact: true }),
  ).toBeDisabled();
  await dialog
    .getByRole("combobox", { name: "版本", exact: true })
    .selectOption("1.0.0");
  await expect(dialog).toContainText("此版本没有适合服务器平台的安装包。");
  await expect(
    dialog.getByRole("button", { name: "安装插件", exact: true }),
  ).toBeDisabled();
  await expect
    .poll(() =>
      page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    )
    .toBe(true);
  await expect
    .poll(() =>
      dialog.evaluate((element) => element.scrollWidth <= element.clientWidth),
    )
    .toBe(true);
  await screenshot(page, "plugin-library-zh-mobile.png");
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await library
    .getByRole("button", { name: "Image renderer", exact: true })
    .click();
  const rendererDialog = page.getByRole("dialog", {
    name: "插件详情 · Image renderer",
    exact: true,
  });
  await expect(
    rendererDialog.getByRole("combobox", { name: "版本", exact: true }),
  ).toHaveValue("3.2.4");
  await expect(rendererDialog).toContainText("当前不支持 Renderer 安装包。");
  await expect(
    rendererDialog.getByRole("button", { name: "安装插件", exact: true }),
  ).toBeDisabled();
  await rendererDialog
    .getByRole("button", { name: "取消", exact: true })
    .click();
  expect(writes).toBe(0);
  await expect(page.locator("vite-error-overlay")).toHaveCount(0);
  expect(issues).toEqual([]);
});
