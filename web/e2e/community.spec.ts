import { test, expect } from "@playwright/test";
import { spawn, execFile } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
let endpoint = "",
  service: ReturnType<typeof spawn>,
  temp = "";
test.beforeAll(async () => {
  temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-community-e2e-"),
  );
  service = spawn(
    process.env.METRICSPANEL_TEST_BINARY ||
      path.join(
        root,
        "bin",
        process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
      ),
    ["serve", "--listen", "127.0.0.1:0", "--db", path.join(temp, "control.db")],
    {
      cwd: root,
      windowsHide: true,
      env: {
        ...process.env,
        METRICSPANEL_TOKEN: "",
        METRICSPANEL_STORAGE: "sqlite",
      },
    },
  );
  let pending = "";
  service.stderr!.on("data", (chunk) => {
    pending += chunk.toString();
    let lineEnd: number;
    while ((lineEnd = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, lineEnd);
      pending = pending.slice(lineEnd + 1);
      try {
        const entry = JSON.parse(line);
        if (entry.address) endpoint = `http://${entry.address}`;
      } catch {}
    }
  });
  await expect.poll(() => endpoint, { timeout: 15000 }).not.toBe("");
});
test.afterAll(async () => {
  if (service && service.exitCode === null) {
    const exited = new Promise<void>((resolve) =>
      service.once("exit", () => resolve()),
    );
    service.kill();
    await exited;
  }
  if (temp) {
    const target = path.resolve(temp);
    if (
      !target.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(target).startsWith("metricspanel233-community-e2e-")
    )
      throw new Error("Unsafe community test cleanup");
    await rm(target, { recursive: true, force: true, maxRetries: 3 });
  }
});
const resource = {
  uid: "portable-inputs",
  title: "Portable inputs",
  schemaVersion: 42,
  opaque: { future: 233 },
  opaqueConstant: "${PREFIX}",
  __inputs: [
    {
      name: "DS_PROM",
      label: "Monitoring source",
      type: "datasource",
      pluginId: "prometheus",
    },
    { name: "PREFIX", label: "Metric prefix", type: "constant", value: "up" },
  ],
  panels: [
    {
      id: 1,
      type: "stat",
      title: "Bound input",
      datasource: { type: "prometheus", uid: "${DS_PROM}" },
      targets: [
        { refId: "A", expr: "vector(233)", instant: true, opaque: "retain" },
      ],
      fieldConfig: { defaults: {}, overrides: [] },
      options: {},
      gridPos: { x: 0, y: 0, w: 12, h: 8 },
    },
  ],
};
test("Portable JSON import previews inputs, binds local datasource and retains opaque exported fields", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) errors.push(entry.text());
  });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(endpoint + "/#dashboards");
  await expect(page).toHaveTitle("MetricsPanel233 · Local observability");
  await page
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  let dialog = page.getByRole("dialog", {
    name: "Import dashboard",
    exact: true,
  });
  await dialog.getByRole("tab", { name: "JSON", exact: true }).click();
  await dialog
    .getByLabel("Dashboard JSON", { exact: true })
    .fill(JSON.stringify(resource));
  await dialog
    .getByRole("button", { name: "Load dashboard", exact: true })
    .click();
  await expect(
    dialog.getByRole("combobox", { name: "Monitoring source", exact: true }),
  ).toHaveValue("metricspanel");
  const before = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  expect(before.some((d: any) => d.name === "Portable inputs")).toBeFalsy();
  await dialog
    .getByLabel("Dashboard name", { exact: true })
    .fill("Company metrics");
  await dialog
    .getByLabel("Dashboard UID (optional)", { exact: true })
    .fill("company-import");
  await dialog.getByLabel("Metric prefix", { exact: true }).fill("");
  const binding = dialog.getByRole("combobox", {
    name: "Monitoring source",
    exact: true,
  });
  await binding.selectOption("");
  await expect(binding).toHaveValue("");
  await dialog
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  await expect(dialog).toBeVisible();
  await binding.selectOption("metricspanel");
  if (process.env.METRICSPANEL_CATALOG_QA_DIR)
    await page.screenshot({
      path: path.join(
        process.env.METRICSPANEL_CATALOG_QA_DIR,
        "dashboard-import-inputs-en.png",
      ),
      animations: "disabled",
    });
  await dialog
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Company metrics", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Bound input", exact: true }),
  ).toContainText("233");
  let items = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  const saved = items.find((d: any) => d.name === "Company metrics");
  expect(saved.grafana.opaque).toEqual({ future: 233 });
  expect(saved.grafana.opaqueConstant).toBe("");
  expect(saved.grafana.__inputs[1].value).toBe("");
  expect(saved.grafana.uid).toBe("company-import");
  expect(saved.grafana.panels[0].datasource.uid).toBe("metricspanel");
  expect(saved.grafana.panels[0].targets[0].opaque).toBe("retain");
  await page.goto(endpoint + "/#dashboards");
  await page.locator('input[type="file"]').setInputFiles({
    name: "portable.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(resource)),
  });
  dialog = page.getByRole("dialog", { name: "Import dashboard", exact: true });
  await expect(
    dialog.getByRole("combobox", { name: "Monitoring source", exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  items = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  expect(items).toHaveLength(before.length + 1);
  expect(errors).toEqual([]);
});
test("Pasted and uploaded Grafana JSON retain unsafe integers in saved source and raw exports", async ({
  page,
}) => {
  test.setTimeout(120000);
  const issues: string[] = [],
    bodies: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    const expectedExportFailure =
      entry.type() === "error" &&
      entry.location().url.includes("/api/v1/dashboard-exports/") &&
      entry.text().includes("status of 409");
    if (["error", "warning"].includes(entry.type()) && !expectedExportFailure)
      issues.push(entry.text());
  });
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      /\/api\/v1\/import\/grafana(?:-preview|-bound)?$/.test(
        new URL(request.url()).pathname,
      )
    )
      bodies.push(request.postData() || "");
  });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const binary =
    process.env.METRICSPANEL_TEST_BINARY ||
    path.join(
      root,
      "bin",
      process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
    );
  const cases = [
    { route: "paste", uid: "exact-integer-paste", bound: true },
    { route: "dialog-file", uid: "exact-integer-dialog-file", bound: true },
    { route: "original-file", uid: "exact-integer-original-file", bound: true },
    { route: "original-file", uid: "exact-integer-file-quick", bound: false },
  ];
  const assertExact = (text: string) => {
    expect(text).toMatch(/"counter"\s*:\s*9007199254740993\s*[,}]/);
    expect(text).toMatch(/"digits"\s*:\s*"9007199254740993"/);
    expect(text).toMatch(/"description"\s*:\s*"number 9007199254740993"/);
    expect(text).not.toMatch(/"counter"\s*:\s*9007199254740992\s*[,}]/);
  };
  for (const item of cases) {
    const name = item.bound
      ? `Exact "${item.route}" \\ binding`
      : "Exact quick file";
    const panels = resource.panels.map((panel) => ({
      ...panel,
      datasource: {
        type: "prometheus",
        uid: item.bound ? "${DS_PROM}" : "metricspanel",
      },
    }));
    // Keep the unsafe integer as a literal JSON token from its creation onward.
    const text = `{
      "uid":${JSON.stringify(item.uid)},"title":${JSON.stringify(name)},"schemaVersion":42,
      "opaque":{"counter":9007199254740993,"digits":"9007199254740993","description":"number 9007199254740993"},
      "__inputs":${JSON.stringify(item.bound ? resource.__inputs : [])},
      "panels":${JSON.stringify(panels)}
    }`;
    const start = bodies.length;
    await page.goto(endpoint + "/#dashboards");
    if (item.route === "original-file") {
      await page.locator('input[type="file"]').setInputFiles({
        name: "exact.json",
        mimeType: "application/json",
        buffer: Buffer.from(text),
      });
    } else {
      await page
        .getByRole("button", { name: "Import dashboard", exact: true })
        .click();
      const dialog = page.getByRole("dialog", {
        name: "Import dashboard",
        exact: true,
      });
      await dialog.getByRole("tab", { name: "JSON", exact: true }).click();
      if (item.route === "paste") {
        await dialog.getByLabel("Dashboard JSON", { exact: true }).fill(text);
        await dialog
          .getByRole("button", { name: "Load dashboard", exact: true })
          .click();
      } else {
        await dialog
          .getByLabel("Upload JSON file", { exact: true })
          .setInputFiles({
            name: "exact.json",
            mimeType: "application/json",
            buffer: Buffer.from(text),
          });
      }
    }
    if (item.bound) {
      const dialog = page.getByRole("dialog", {
        name: "Import dashboard",
        exact: true,
      });
      await expect(
        dialog.getByRole("combobox", {
          name: "Monitoring source",
          exact: true,
        }),
      ).toHaveValue("metricspanel");
      await dialog
        .getByLabel("Dashboard UID (optional)", { exact: true })
        .fill(item.uid);
      await dialog
        .getByRole("button", { name: "Import dashboard", exact: true })
        .click();
    }
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    const transmitted = bodies.slice(start);
    expect(transmitted.length).toBeGreaterThanOrEqual(2);
    for (const body of transmitted) {
      assertExact(body);
      expect(body).toContain(text);
    }
    if (!item.bound) expect(transmitted.at(-1)).toBe(text);
    const response = await page.request.get(endpoint + "/api/v1/dashboards");
    expect(response.ok()).toBeTruthy();
    const raw = await response.text();
    assertExact(raw);
    const saved = JSON.parse(raw).find(
      (dashboard: any) => dashboard.name === name,
    );
    expect(saved).toBeDefined();
    expect(saved.grafana.uid).toBe(item.uid);
    const classic = await page.request.get(
      `${endpoint}/api/dashboards/uid/${item.uid}`,
    );
    expect(classic.ok()).toBeTruthy();
    assertExact(await classic.text());
    const exported = await promisify(execFile)(
      binary,
      [
        "dashboards",
        "export",
        "--id",
        saved.id,
        "--format",
        "grafana",
        "--server",
        endpoint,
      ],
      { windowsHide: true, env: { ...process.env, METRICSPANEL_TOKEN: "" } },
    );
    assertExact(exported.stdout);
    await page.evaluate(() =>
      sessionStorage.setItem("metricspanel-token", "download-token-proof"),
    );
    if (item.route === "paste") {
      await page.route(
        /\/api\/v1\/dashboard-exports\/[^?]+\?format=grafana$/,
        (route) =>
          route.fulfill({
            status: 409,
            json: { error: { message: "Export temporarily unavailable" } },
          }),
        { times: 1 },
      );
      await page
        .getByRole("button", { name: "Export JSON", exact: true })
        .click();
      await expect(page.getByRole("alert")).toHaveText(
        "Export temporarily unavailable",
      );
      await expect(
        page.getByRole("button", { name: "Export JSON", exact: true }),
      ).toBeEnabled();
    }
    const templateRequest = page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname ===
        `/api/v1/dashboard-exports/${saved.id}`,
    );
    const templateDownload = page.waitForEvent("download");
    await page
      .getByRole("button", { name: "Export JSON", exact: true })
      .click();
    const templateArtifact = await templateDownload;
    expect(templateArtifact.suggestedFilename()).toBe(
      `${saved.id}-grafana.json`,
    );
    const templateAPI = await templateRequest;
    expect(new URL(templateAPI.url()).searchParams.get("format")).toBe(
      "grafana",
    );
    expect(templateAPI.headers().authorization).toBe(
      "Bearer download-token-proof",
    );
    const templatePath = path.join(temp, `${item.uid}-grafana.json`);
    await templateArtifact.saveAs(templatePath);
    assertExact(await readFile(templatePath, "utf8"));
    await page.goto(endpoint + "/#dashboards");
    const nativeRequest = page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname ===
        `/api/v1/dashboard-exports/${saved.id}`,
    );
    const nativeDownload = page.waitForEvent("download");
    await page
      .getByRole("button", { name: `Export ${name}`, exact: true })
      .click();
    const nativeArtifact = await nativeDownload;
    expect(nativeArtifact.suggestedFilename()).toBe(`${saved.id}.json`);
    const nativeAPI = await nativeRequest;
    expect(new URL(nativeAPI.url()).searchParams.get("format")).toBe("native");
    expect(nativeAPI.headers().authorization).toBe(
      "Bearer download-token-proof",
    );
    const nativePath = path.join(temp, `${item.uid}-native.json`);
    await nativeArtifact.saveAs(nativePath);
    assertExact(await readFile(nativePath, "utf8"));
    await page.evaluate(() => sessionStorage.removeItem("metricspanel-token"));
  }
  expect(issues).toEqual([]);
});
test("Imported datasource UID prometheus queries its external source rather than the local alias", async ({
  page,
}) => {
  let requests = 0;
  const upstream = createServer((request, response) => {
    requests++;
    response.setHeader("Content-Type", "application/json");
    const time = Math.floor(Date.now() / 1000);
    if (request.url?.includes("/label/"))
      response.end(
        JSON.stringify({ status: "success", data: ["external-host"] }),
      );
    else if (request.url?.includes("query_range"))
      response.end(
        JSON.stringify({
          status: "success",
          data: {
            resultType: "matrix",
            result: [
              { metric: { source: "external" }, values: [[time, "777"]] },
            ],
          },
        }),
      );
    else
      response.end(
        JSON.stringify({
          status: "success",
          data: {
            resultType: "vector",
            result: [{ metric: { source: "external" }, value: [time, "777"] }],
          },
        }),
      );
  });
  await new Promise<void>((resolve) =>
    upstream.listen(0, "127.0.0.1", resolve),
  );
  try {
    const address = upstream.address() as { port: number };
    const source = await page.request.post(endpoint + "/api/datasources", {
      data: {
        uid: "prometheus",
        name: "Production Prometheus",
        type: "prometheus",
        url: `http://127.0.0.1:${address.port}`,
      },
    });
    expect(source.ok()).toBeTruthy();
    const imported = await page.request.post(
      endpoint + "/api/v1/import/grafana-bound",
      {
        data: {
          dashboard: {
            ...resource,
            templating: {
              list: [
                {
                  name: "remote_host",
                  label: "Remote host",
                  type: "query",
                  query: "label_values(up, instance)",
                  datasource: { uid: "${DS_PROM}", type: "prometheus" },
                  current: { value: "" },
                  options: [],
                },
              ],
            },
          },
          name: "External source",
          inputs: { DS_PROM: "prometheus", PREFIX: "remote" },
        },
      },
    );
    expect(imported.ok()).toBeTruthy();
    const { dashboard } = await imported.json();
    await page.addInitScript(() =>
      localStorage.setItem("metricspanel-locale", "en"),
    );
    await page.goto(`${endpoint}/d/${dashboard.grafana.uid}/external-source`);
    await expect(
      page.getByRole("region", { name: "Bound input", exact: true }),
    ).toContainText("777");
    await expect(
      page.getByRole("combobox", { name: "remote_host", exact: true }),
    ).toHaveValue("external-host");
    expect(requests).toBeGreaterThan(0);
  } finally {
    await new Promise<void>((resolve, reject) =>
      upstream.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
test("Community import resolves URL and revisions, sends pinned bindings and cancels Chinese mobile preview", async ({
  page,
}) => {
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  let readCount = 0,
    postCount = 0;
  await page.route("**/api/v1/dashboards/community/1860**", async (route) => {
    readCount++;
    const revision =
      Number(new URL(route.request().url()).searchParams.get("revision")) || 45;
    await route.fulfill({
      json: {
        id: 1860,
        revision,
        title: "Community fixture",
        description: "Fixture for catalog interaction",
        url: "https://grafana.com/grafana/dashboards/1860/",
        revisions: [45, 42],
        dashboard: resource,
        inputs: [
          {
            name: "DS_PROM",
            label: "Monitoring source",
            type: "datasource",
            plugin_id: "prometheus",
            required: true,
          },
        ],
      },
    });
  });
  await page.route("**/api/v1/import/grafana-community", async (route) => {
    postCount++;
    expect(route.request().postDataJSON()).toEqual({
      id: 1860,
      revision: 42,
      name: "Pinned community",
      inputs: { DS_PROM: "metricspanel" },
    });
    // Transport is mocked; the real bound-import route verifies persistence and rendering.
    const response = await page.request.post(
      endpoint + "/api/v1/import/grafana-bound",
      {
        data: {
          dashboard: resource,
          name: "Pinned community",
          inputs: { DS_PROM: "metricspanel", PREFIX: "up" },
        },
      },
    );
    await route.fulfill({ response });
  });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(endpoint + "/#dashboards");
  await page
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  let dialog = page.getByRole("dialog", {
    name: "Import dashboard",
    exact: true,
  });
  await dialog
    .getByLabel("Grafana.com URL or ID", { exact: true })
    .fill("https://grafana.com/grafana/dashboards/1860-node-exporter-full/");
  await dialog
    .getByRole("button", { name: "Load dashboard", exact: true })
    .click();
  await expect(
    dialog.getByRole("combobox", { name: "Community revision", exact: true }),
  ).toHaveValue("45");
  await dialog
    .getByRole("combobox", { name: "Community revision", exact: true })
    .selectOption("42");
  await expect(
    dialog.getByRole("combobox", { name: "Community revision", exact: true }),
  ).toHaveValue("42");
  await dialog
    .getByLabel("Dashboard name", { exact: true })
    .fill("Pinned community");
  await dialog
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Pinned community", exact: true }),
  ).toBeVisible();
  expect(postCount).toBe(1);
  expect(readCount).toBe(2);
  await page.goto(endpoint + "/#dashboards");
  await page
    .getByRole("button", { name: "Switch language", exact: true })
    .click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "导入仪表盘", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "导入仪表盘", exact: true });
  await dialog
    .getByLabel("Grafana.com 地址或 ID", { exact: true })
    .fill("1860");
  await dialog.getByRole("button", { name: "加载仪表盘", exact: true }).click();
  await expect(
    dialog.getByRole("combobox", { name: "社区版本", exact: true }),
  ).toHaveValue("45");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
  ).toBeFalsy();
  if (process.env.METRICSPANEL_CATALOG_QA_DIR)
    await page.screenshot({
      path: path.join(
        process.env.METRICSPANEL_CATALOG_QA_DIR,
        "dashboard-community-zh-mobile.png",
      ),
      animations: "disabled",
    });
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(postCount).toBe(1);
  expect(issues).toEqual([]);
});
