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
        { cwd: root, env: { ...process.env, METRICSPANEL_TOKEN: "" } },
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
  await page
    .locator("input[type=file]")
    .setInputFiles({
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
