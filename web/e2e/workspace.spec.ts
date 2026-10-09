import { test as base, expect } from "@playwright/test";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, rm, readFile } from "node:fs/promises";
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
        path.join(os.tmpdir(), "metricspanel233-e2e-"),
      );
      const root = repoRoot,
        binary =
          process.env.METRICSPANEL_TEST_BINARY ||
          path.join(
            root,
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
          "--allow-unsigned-plugin",
          "metricspanel-sdk-datasource",
        ],
        {
          cwd: root,
          env: {
            ...process.env,
            METRICSPANEL_TOKEN: "",
            METRICSPANEL_STORAGE: "sqlite",
          },
        },
      );
      let endpoint = "",
        logs = "";
      server.stderr.on("data", (chunk) => {
        logs += chunk.toString();
        for (const line of chunk.toString().split("\n")) {
          try {
            const item = JSON.parse(line);
            if (item.address) endpoint = `http://${item.address}`;
          } catch {
            /* an incomplete JSON line arrives in the next chunk */
          }
        }
      });
      try {
        await expect
          .poll(() => endpoint, { timeout: 15000, message: logs })
          .not.toBe("");
        await use(endpoint);
      } finally {
        const exited = new Promise<void>((resolve) =>
          server.once("exit", () => resolve()),
        );
        if (server.exitCode === null) {
          server.kill();
          await exited;
        }
        const resolvedTemp = path.resolve(temp);
        if (
          !resolvedTemp.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path.basename(resolvedTemp).startsWith("metricspanel233-e2e-")
        )
          throw new Error(
            "Refusing cleanup outside the e2e temporary directory",
          );
        await rm(resolvedTemp, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Official frontend DataSourceWithBackend queries a real Go SDK subprocess", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(120000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-sdk-e2e-"),
  );
  const root = repoRoot,
    binary = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "fixture.zip");
  const run = promisify(execFile),
    errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  try {
    await run(
      "go",
      ["build", "-o", binary, "./internal/plugins/testdata/sdk-backend"],
      { cwd: root, windowsHide: true },
    );
    await run(binary, ["--package", archive], { windowsHide: true });
    const installed = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(installed.ok(), await installed.text()).toBeTruthy();
    const source = await page.request.post(endpoint + "/api/datasources", {
      data: {
        uid: "frontend-sdk",
        name: "Frontend SDK",
        type: "metricspanel-sdk-datasource",
        secureJsonData: { apiKey: "test-secret-233" },
      },
    });
    expect(source.ok(), await source.text()).toBeTruthy();
    const dashboard = await page.request.post(endpoint + "/api/dashboards/db", {
      data: {
        dashboard: {
          uid: "frontend-sdk-dashboard",
          title: "SDK bridge",
          panels: [
            {
              id: 1,
              type: "stat",
              title: "SDK value",
              gridPos: { x: 0, y: 0, w: 24, h: 8 },
              targets: [
                {
                  refId: "A",
                  value: 233,
                  datasource: {
                    uid: "frontend-sdk",
                    type: "metricspanel-sdk-datasource",
                  },
                },
              ],
              options: {
                reduceOptions: { calcs: ["lastNotNull"], values: false },
              },
              fieldConfig: { defaults: {}, overrides: [] },
            },
          ],
        },
        overwrite: true,
      },
    });
    expect(dashboard.ok(), await dashboard.text()).toBeTruthy();
    await page.goto(endpoint + "/d/frontend-sdk-dashboard/sdk");
    await expect(page.locator(".grafana-value strong")).toContainText("233", {
      timeout: 20000,
    });
    await page.reload();
    await expect(page.locator(".grafana-value strong")).toContainText("233");
    const liveDashboard = {
      uid: "frontend-live-dashboard",
      title: "SDK Live bridge",
      panels: [1, 2].map((id) => ({
        id,
        type: "stat",
        title: `Live counter ${id}`,
        gridPos: { x: (id - 1) * 12, y: 0, w: 12, h: 8 },
        targets: [
          {
            refId: "A",
            value: 233,
            live: true,
            directLive: id === 2,
            datasource: {
              uid: "frontend-sdk",
              type: "metricspanel-sdk-datasource",
            },
          },
          ...(id === 1
            ? [
                {
                  refId: "B",
                  value: 777,
                  datasource: {
                    uid: "frontend-sdk",
                    type: "metricspanel-sdk-datasource",
                  },
                },
              ]
            : []),
        ],
        options: { reduceOptions: { calcs: ["lastNotNull"], values: false } },
        fieldConfig: { defaults: { decimals: 0 }, overrides: [] },
      })),
    };
    const liveSaved = await page.request.post(endpoint + "/api/dashboards/db", {
      data: { dashboard: liveDashboard, overwrite: true },
    });
    expect(liveSaved.ok(), await liveSaved.text()).toBeTruthy();
    const streamStats = async () =>
      (
        await page.request.get(
          endpoint + "/api/datasources/uid/frontend-sdk/resources/stream-stats",
        )
      ).json();
    const channels = async () =>
      (await page.request.get(endpoint + "/api/live/channels")).json();
    const staticQueriesBeforeLive = (await streamStats()).static_queries;
    await page.goto(endpoint + "/d/frontend-live-dashboard/live");
    const liveValues = page.locator(".grafana-value strong");
    await expect(liveValues).toHaveCount(3);
    // Pass the app's five-second refresh boundary without restarting RunStream.
    await expect
      .poll(
        async () =>
          (await liveValues.allTextContents())
            .map((value) => Number(value.replaceAll(",", "")))
            .every((value) => value >= 310),
        { timeout: 20000 },
      )
      .toBeTruthy();
    await expect(page.locator(".grafana-panel .status")).toHaveText([
      "Live",
      "Live",
    ]);
    await expect
      .poll(async () => {
        const state = await channels();
        return [
          state.connections,
          state.channels.length,
          state.channels[0]?.subscribers,
        ];
      })
      .toEqual([1, 1, 1]);
    expect(await streamStats()).toMatchObject({ started: 1, active: 1 });
    expect((await streamStats()).static_queries).toBeGreaterThanOrEqual(
      staticQueriesBeforeLive + 2,
    );
    await expect(
      page
        .getByRole("region", { name: "Live counter 1", exact: true })
        .getByText("777", { exact: true }),
    ).toBeVisible();
    // A changed datasource cancels the old context and the browser resubscribes.
    const updated = await page.request.put(
      endpoint + "/api/datasources/uid/frontend-sdk",
      {
        data: {
          uid: "frontend-sdk",
          name: "Updated SDK",
          type: "metricspanel-sdk-datasource",
        },
      },
    );
    expect(updated.ok(), await updated.text()).toBeTruthy();
    await expect
      .poll(streamStats)
      .toMatchObject({ started: 2, active: 1, cancelled: 1 });
    await expect
      .poll(async () =>
        (await liveValues.allTextContents()).every(
          (value) => Number(value.replaceAll(",", "")) > 240,
        ),
      )
      .toBeTruthy();
    await page.getByRole("button", { name: "Overview", exact: true }).click();
    await expect
      .poll(async () => {
        const state = await channels();
        return [state.connections, state.channels.length];
      })
      .toEqual([0, 0]);
    await expect.poll(streamStats).toMatchObject({ active: 0, cancelled: 2 });
    await page.goto(endpoint + "/d/frontend-live-dashboard/live");
    await expect.poll(streamStats).toMatchObject({ started: 3, active: 1 });
    await page.request.put(
      endpoint + "/api/v1/plugins/metricspanel-sdk-datasource",
      { data: { enabled: false } },
    );
    await expect(page.locator(".grafana-panel .form-error")).toHaveCount(2);
    await expect.poll(async () => (await channels()).channels.length).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview");
    await page.request.delete(
      endpoint + "/api/dashboards/uid/frontend-live-dashboard",
    );
    await page.request.delete(
      endpoint + "/api/dashboards/uid/frontend-sdk-dashboard",
    );
    await page.request.delete(endpoint + "/api/datasources/uid/frontend-sdk");
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-sdk-datasource",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-sdk-e2e-")
    )
      throw new Error("Unsafe SDK test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("Plugin management and official signed Clock render from its unchanged AMD package", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const raw = await readFile(
    path.join(
      repoRoot,
      "internal/plugins/testdata/grafana-clock-panel-3.2.4.zip",
    ),
  );
  const installed = await page.request.post(
    endpoint + "/api/v1/plugins/install",
    { data: raw, headers: { "Content-Type": "application/zip" } },
  );
  expect(installed.ok(), await installed.text()).toBeTruthy();
  expect((await installed.json()).signature).toBe("grafana");
  const saved = await page.request.post(endpoint + "/api/dashboards/db", {
    data: {
      dashboard: {
        uid: "official-clock",
        title: "Official Clock",
        schemaVersion: 41,
        panels: [
          {
            id: 1,
            title: "Signed clock",
            type: "grafana-clock-panel",
            pluginVersion: "3.2.4",
            gridPos: { x: 0, y: 0, w: 24, h: 10 },
            targets: [],
            options: {
              mode: "time",
              clockType: "24 hour",
              timeSettings: { fontSize: "48px", fontWeight: "normal" },
              dateSettings: {
                showDate: true,
                dateFormat: "YYYY-MM-DD",
                fontSize: "20px",
                fontWeight: "normal",
              },
              timezone: "utc",
            },
            fieldConfig: { defaults: {}, overrides: [] },
          },
        ],
      },
      overwrite: true,
    },
  });
  expect(saved.ok(), await saved.text()).toBeTruthy();
  await page.goto(endpoint + "/#plugins");
  await expect(
    page.getByRole("heading", { name: "Plugins & datasources", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".plugin-row")).toContainText("3.2.4");
  await page
    .getByRole("button", { name: "Add datasource", exact: true })
    .click();
  await page
    .getByLabel("Datasource name", { exact: true })
    .fill("Browser datasource");
  await page.getByLabel("URL", { exact: true }).fill(endpoint + "/prometheus");
  await page
    .getByRole("button", { name: "Save datasource", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator(".plugin-sources")).toContainText(
    "Browser datasource",
  );
  await page.reload();
  await expect(page.locator(".plugin-sources")).toContainText(
    "Browser datasource",
  );
  await page
    .getByRole("button", { name: "Delete Browser datasource", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await expect(page.locator(".plugin-sources")).not.toContainText(
    "Browser datasource",
  );
  await page
    .getByRole("button", { name: "Switch language", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "插件与数据源", exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await page.goto(endpoint + "/d/official-clock/clock");
  const content = page.locator(".grafana-plugin-content");
  await expect(content).toContainText(/\d{2}:\d{2}:\d{2}/, { timeout: 30000 });
  const before = await content.innerText();
  await expect
    .poll(() => content.innerText(), { timeout: 5000 })
    .not.toBe(before);
  await page.reload();
  await expect(content).toContainText(/\d{2}:\d{2}:\d{2}/);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(content).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
  expect(
    (
      await page.request.delete(endpoint + "/api/dashboards/uid/official-clock")
    ).ok(),
  ).toBeTruthy();
  expect(
    (
      await page.request.delete(
        endpoint + "/api/v1/plugins/grafana-clock-panel",
      )
    ).ok(),
  ).toBeTruthy();
});

test("Persistent alert rules, evaluation, pause, edits and bilingual mobile UI", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(endpoint + "/#alerts");
  await page
    .getByRole("button", { name: "Create alert", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Rule name", exact: true })
    .fill("E2E runtime alert");
  await page
    .getByRole("textbox", { name: "PromQL condition", exact: true })
    .fill("metricspanel_memory_bytes > bool 0");
  await page.getByLabel("Evaluate every (s)").fill("86400");
  await page.getByRole("button", { name: "Save rule", exact: true }).click();
  const row = page
    .locator(".alert-rule")
    .filter({ has: page.locator("strong", { hasText: "E2E runtime alert" }) });
  await expect(row).toHaveCount(1);
  await row
    .getByRole("button", { name: "Evaluate E2E runtime alert", exact: true })
    .click();
  await expect(row.locator(".alert-rule-status")).toContainText("Firing");
  await row.locator(".alert-rule-title").click();
  await expect(row.locator(".alert-detail")).toContainText("metricspanel");
  await row
    .getByRole("button", { name: "Pause E2E runtime alert", exact: true })
    .click();
  await expect(row.locator(".alert-rule-status")).toContainText("Paused");
  await page.reload();
  await expect(row.locator(".alert-rule-status")).toContainText("Paused");
  await row
    .getByRole("button", { name: "Edit E2E runtime alert", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "PromQL condition", exact: true })
    .fill("metricspanel_memory_bytes < bool 0");
  await page.getByRole("button", { name: "Save rule", exact: true }).click();
  await expect(row.locator("code")).toContainText("< bool 0");
  await row
    .getByRole("button", { name: "Resume E2E runtime alert", exact: true })
    .click();
  await expect(
    row.getByRole("button", {
      name: "Evaluate E2E runtime alert",
      exact: true,
    }),
  ).toBeEnabled();
  await row
    .getByRole("button", { name: "Evaluate E2E runtime alert", exact: true })
    .click();
  await expect(row.locator(".alert-rule-status")).toContainText("Normal");
  await expect(page.locator(".alert-history")).toContainText("Firing");
  await page
    .getByRole("button", { name: "Switch language", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "告警", exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
  ).toBe(false);
  await row
    .getByRole("button", { name: "删除 E2E runtime alert", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "删除", exact: true })
    .click();
  await expect(row).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("Persistent pattern capture, nearest windows, language switch and mobile layout", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const now = Date.now();
  const samples = [];
  for (let i = 0; i < 64; i++)
    for (const job of ["go", "mysql"])
      samples.push({
        name: "vector_e2e",
        labels: { job },
        timestamp: now - 14 * 60000 + i * 13000,
        value: job === "go" ? i : i * 20 + 233,
      });
  expect(
    (
      await page.request.post(`${endpoint}/api/v1/ingest`, {
        data: { samples },
      })
    ).ok(),
  ).toBe(true);
  await page.goto(endpoint);
  await page.getByRole("button", { name: "Explore", exact: true }).click();
  await page
    .getByRole("combobox", { name: "Metric", exact: true })
    .selectOption("vector_e2e");
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("15m");
  await page.getByRole("button", { name: "Run query", exact: true }).click();
  await page
    .getByRole("button", { name: "Save current window", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Saved windows: 2");
  await expect(
    page.getByLabel("Saved reference", { exact: true }).locator("option"),
  ).toHaveCount(3);
  await page
    .getByRole("button", { name: "Find similar windows", exact: true })
    .click();
  await expect(
    page.getByRole("cell", { name: "0.0000", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Window comparison", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".form-error")).toHaveCount(0);
  await page
    .getByRole("button", { name: "Switch language", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "波形分析", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "距离", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "波形分析", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByLabel("参考窗口", { exact: true }).locator("option"),
  ).toHaveCount(3);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
  ).toBe(false);
  expect(errors).toEqual([]);
});

test("Grafana resource layout, SDK transforms, instant tables, units, repeat and sanitization", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const now = Date.now();
  const ingest = await page.request.post(`${endpoint}/api/v1/ingest`, {
    data: {
      samples: [
        {
          name: "sdk_ratio",
          labels: { job: "api.1" },
          value: 0.25,
          timestamp: now,
        },
        {
          name: "sdk_ratio",
          labels: { job: "api+2" },
          value: 0.75,
          timestamp: now,
        },
      ],
    },
  });
  expect(ingest.ok()).toBe(true);
  await page.goto(endpoint);
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  const panel = {
    type: "stat",
    targets: [
      { refId: "A", expr: 'sdk_ratio{job=~"${job:regex}"}', instant: true },
    ],
    fieldConfig: {
      defaults: { unit: "percentunit", decimals: 0 },
      overrides: [],
    },
  };
  const source = {
    apiVersion: "dashboard.grafana.app/v1beta1",
    kind: "Dashboard",
    metadata: { name: "sdk" },
    spec: {
      title: "SDK compatibility",
      templating: {
        list: [
          {
            name: "job",
            type: "custom",
            query: "api.1,api+2",
            multi: true,
            includeAll: true,
            current: { value: ["$__all"] },
          },
        ],
      },
      panels: [
        {
          ...panel,
          id: 1,
          title: "Services",
          type: "table",
          gridPos: { x: 0, y: 0, w: 16, h: 9 },
          targets: [
            {
              refId: "A",
              expr: 'sdk_ratio{job=~"${job:regex}"}',
              instant: true,
              format: "table",
            },
          ],
          transformations: [
            {
              id: "organize",
              options: {
                excludeByName: { Time: true, __name__: true },
                renameByName: { "Value #A": "Usage", job: "Service" },
              },
            },
            {
              id: "sortBy",
              options: { sort: [{ field: "Service", desc: false }] },
            },
          ],
        },
        {
          ...panel,
          id: 2,
          title: "Total",
          type: "gauge",
          gridPos: { x: 16, y: 0, w: 8, h: 9 },
          targets: [{ refId: "A", expr: "sum(sdk_ratio)", instant: true }],
          fieldConfig: {
            defaults: { unit: "percentunit", decimals: 0, min: 0, max: 1 },
            overrides: [],
          },
        },
        {
          ...panel,
          id: 3,
          title: "Per service",
          gridPos: { x: 0, y: 9, w: 12, h: 6 },
          repeat: "job",
          repeatDirection: "h",
          maxPerRow: 2,
        },
        {
          id: 4,
          title: "Notes",
          type: "text",
          gridPos: { x: 0, y: 15, w: 24, h: 6 },
          options: {
            mode: "html",
            content:
              '<p>Safe template content</p><img src="x" onerror="alert(1)"><script>alert(1)</script>',
          },
        },
      ],
    },
  };
  await page.locator('input[type="file"]').setInputFiles({
    name: "sdk-resource.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(source)),
  });
  await expect(
    page.getByRole("heading", { name: "SDK compatibility", exact: true }),
  ).toBeVisible();
  const table = page.getByRole("region", { name: "Services", exact: true });
  await expect(
    table.getByRole("columnheader", { name: "Service", exact: true }),
  ).toBeVisible();
  await expect(
    table.getByRole("columnheader", { name: "Usage", exact: true }),
  ).toBeVisible();
  await expect(
    table.getByRole("cell", { name: "25%", exact: true }),
  ).toBeVisible();
  await expect(
    table.getByRole("cell", { name: "75%", exact: true }),
  ).toBeVisible();
  await expect(
    table.getByRole("columnheader", { name: "Time", exact: true }),
  ).toHaveCount(0);
  const gauge = page.getByRole("region", { name: "Total", exact: true });
  await expect(gauge.getByRole("meter")).toHaveAttribute("aria-valuenow", "1");
  await expect(gauge.getByText("100%", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Per service", exact: true }),
  ).toHaveCount(2);
  const tableBox = await table.boundingBox(),
    gaugeBox = await gauge.boundingBox();
  expect(gaugeBox!.x).toBeGreaterThan(tableBox!.x);
  expect(tableBox!.width).toBeGreaterThan(gaugeBox!.width);
  await expect(
    page.getByText("Safe template content", { exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(".grafana-text script, .grafana-text [onerror]"),
  ).toHaveCount(0);
  await page.getByLabel("job", { exact: true }).selectOption(["api.1"]);
  await expect(
    table.getByRole("cell", { name: "api+2", exact: true }),
  ).toHaveCount(0);
  await expect(
    table.getByRole("cell", { name: "api.1", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Per service", exact: true }),
  ).toHaveCount(1);
  await expect(page.locator(".form-error")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
  ).toBe(false);
  expect(errors).toEqual([]);
});

test("English and Chinese, panels, PromQL, dashboards and collector forms", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(endpoint);
  if (
    await page
      .getByRole("heading", { name: "系统概览", exact: true })
      .isVisible()
  )
    await page.getByRole("button", { name: "Switch language" }).click();
  await expect(
    page.getByRole("heading", { name: "System overview", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Memory usage", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Switch language" }).click();
  await expect(
    page.getByRole("heading", { name: "系统概览", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "添加面板", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Switch language" }).click();
  await page.getByRole("button", { name: "Add panel", exact: true }).click();
  await page
    .getByLabel("Panel title", { exact: true })
    .fill("Persistent uptime");
  await page
    .getByLabel("Metric", { exact: true })
    .fill("metricspanel_uptime_seconds");
  await page.getByRole("button", { name: "Save panel", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Persistent uptime", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Persistent uptime", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Explore", exact: true }).click();
  await page
    .getByLabel("PromQL expression", { exact: true })
    .fill("sum(metricspanel_goroutines)");
  await page.getByRole("button", { name: "Run query", exact: true }).click();
  await expect(
    page.getByText("sum(metricspanel_goroutines)", { exact: true }),
  ).toBeVisible();
  await expect(page.locator(".chart-panel .form-error")).toHaveCount(0);
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await page
    .getByRole("button", { name: "New dashboard", exact: true })
    .click();
  await page
    .getByLabel("Dashboard name", { exact: true })
    .fill("Backend operations");
  await page
    .getByRole("button", { name: "Create dashboard", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Backend operations", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Collectors", exact: true }).click();
  await page
    .getByRole("button", { name: "Add collector", exact: true })
    .first()
    .click();
  await page.getByLabel("Collector name", { exact: true }).fill("Self test");
  await page
    .getByLabel("Metrics endpoint", { exact: true })
    .fill(`${endpoint}/metrics`);
  await page
    .getByRole("button", { name: "Save collector", exact: true })
    .click();
  await expect(page.getByText("Self test", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Scrape Self test", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Scrape completed");
  await page.getByRole("button", { name: "Agent CLI", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "JSON in. JSON out.", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("Grafana template import, variables and mobile layout", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(endpoint);
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await page.locator("input[type=file]").setInputFiles({
    name: "grafana.json",
    mimeType: "application/json",
    buffer: Buffer.from(
      JSON.stringify({
        title: "Go Grafana template",
        panels: [
          {
            type: "timeseries",
            title: "Go query",
            targets: [{ expr: 'metricspanel_goroutines{job="$job"}' }],
          },
        ],
        templating: {
          list: [
            {
              name: "job",
              type: "query",
              query: "label_values(metricspanel_goroutines,job)",
              current: { value: "metricspanel" },
            },
          ],
        },
      }),
    ),
  });
  await expect(
    page.getByRole("heading", { name: "Go Grafana template", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("job", { exact: true })).toHaveValue(
    "metricspanel",
  );
  await expect(
    page.getByRole("heading", { name: "Go query", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".form-error")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("button", { name: "Open navigation", exact: true }),
  ).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
  await page
    .getByRole("button", { name: "Open navigation", exact: true })
    .click();
  await page.getByRole("button", { name: "Overview", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "System overview", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});
