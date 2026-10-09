import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-e2e-"),
      );
      const root = path.resolve(".."),
        binary = path.join(
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
