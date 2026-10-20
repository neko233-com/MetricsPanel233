import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-native-panels-e2e-"),
      );
      const service = spawn(
        process.env.METRICSPANEL_TEST_BINARY ||
          path.join(
            root,
            "bin",
            process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
          ),
        [
          "serve",
          "--listen",
          "127.0.0.1:0",
          "--db",
          path.join(temp, "control.db"),
        ],
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
      let endpoint = "",
        pending = "";
      service.stderr!.on("data", (chunk) => {
        pending += chunk.toString();
        for (;;) {
          const end = pending.indexOf("\n");
          if (end < 0) break;
          const line = pending.slice(0, end);
          pending = pending.slice(end + 1);
          try {
            const item = JSON.parse(line);
            if (item.address) endpoint = `http://${item.address}`;
          } catch {}
        }
      });
      try {
        await expect.poll(() => endpoint, { timeout: 15000 }).not.toBe("");
        await use(endpoint);
      } finally {
        if (service.exitCode === null) {
          const exited = new Promise<void>((resolve) =>
            service.once("exit", () => resolve()),
          );
          service.kill();
          await exited;
        }
        const resolved = path.resolve(temp);
        if (
          !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path
            .basename(resolved)
            .startsWith("metricspanel233-native-panels-e2e-")
        )
          throw new Error("Unsafe native panel fixture cleanup");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Native panel query, display edits and duplication retain raw settings", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const source = `{"id":"native-queries-pc","name":"Native queries","panels":[{"id":"one","title":"Orders","aggregation":"last","metric":"","unit":"","visualization":"stat","config":{"id":7,"type":"stat","opaque":1e10000,"options":{"opaque":9007199254740993},"gridPos":{"x":0,"y":0,"w":12,"h":8},"targets":[{"refId":"A","expr":"vector(233)","instant":true,"datasource":{"uid":"metricspanel","type":"prometheus"},"opaque":9007199254740993}]}}]}`;
  const create = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: source,
    headers: { "Content-Type": "application/json" },
  });
  expect(create.ok(), await create.text()).toBeTruthy();
  const saved = await create.json();
  await page.goto(endpoint + "/d/" + saved.id + "/native-queries");
  await expect(
    page.getByRole("heading", { name: "Native queries", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Orders", exact: true }),
  ).toContainText("233");
  await page
    .getByRole("button", { name: "Panel queries", exact: true })
    .click();
  const queries = page.getByRole("dialog", {
    name: "Panel queries",
    exact: true,
  });
  await queries.getByText("Advanced query JSON", { exact: true }).click();
  const rawQuery = queries.getByLabel("Query JSON", { exact: true });
  const query = JSON.parse(await rawQuery.inputValue());
  query.expr = "vector(234)";
  await rawQuery.fill(JSON.stringify(query, null, 2));
  await queries
    .getByRole("button", { name: "Save queries", exact: true })
    .click();
  await expect(queries).not.toBeVisible();
  await expect(
    page.getByRole("region", { name: "Orders", exact: true }),
  ).toContainText("234");
  await page.getByRole("button", { name: "Edit Orders", exact: true }).click();
  const edit = page.getByRole("dialog", { name: "Edit panel", exact: true });
  await edit.getByLabel("Panel title", { exact: true }).fill("Orders renamed");
  await edit.getByRole("button", { name: "Save panel", exact: true }).click();
  await expect(edit).not.toBeVisible();
  await expect(
    page.getByRole("region", { name: "Orders renamed", exact: true }),
  ).toContainText("234");
  await page
    .getByRole("button", { name: "Edit Orders renamed", exact: true })
    .click();
  await edit.getByLabel("Panel title", { exact: true }).fill("Orders copy");
  await edit
    .getByRole("button", { name: "Duplicate panel", exact: true })
    .click();
  await expect(edit).not.toBeVisible();
  await expect(
    page.getByRole("region", { name: "Orders copy", exact: true }),
  ).toContainText("234");
  const exported = await page.request.get(
    endpoint + "/api/v1/dashboard-exports/" + saved.id + "?format=native",
  );
  const raw = await exported.text();
  expect(raw).toContain("1e10000");
  expect(raw).toContain("9007199254740993");
  expect(raw).not.toContain("9007199254740992");
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Orders copy", exact: true }),
  ).toContainText("234");
  const qa = process.env.METRICSPANEL_NATIVE_PANEL_QA_DIR;
  if (qa) {
    await mkdir(qa, { recursive: true });
    await page.screenshot({
      path: path.join(qa, "native-panel-queries-desktop.png"),
      animations: "disabled",
    });
  }
  await page
    .getByRole("button", { name: "Remove Orders copy", exact: true })
    .click();
  await page
    .getByRole("dialog", { name: "Remove panel", exact: true })
    .getByRole("button", { name: "Remove panel", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "Orders copy", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("region", { name: "Orders renamed", exact: true }),
  ).toContainText("234");
  expect(errors).toEqual([]);
});

test("Legacy native PromQL can be changed in the shared query editor", async ({
  page,
  endpoint,
}) => {
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const create = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      id: "native-legacy-pc",
      name: "Native legacy",
      panels: [
        {
          id: "one",
          title: "Legacy",
          aggregation: "last",
          expr: "vector(233)",
          visualization: "stat",
          metric: "",
          unit: "",
        },
      ],
    },
  });
  expect(create.ok(), await create.text()).toBeTruthy();
  const saved = await create.json();
  await page.goto(endpoint + "/d/" + saved.id + "/native-legacy");
  await expect(
    page.getByRole("region", { name: "Legacy", exact: true }),
  ).toContainText("233");
  await page
    .getByRole("button", { name: "Panel queries", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Panel queries",
    exact: true,
  });
  await dialog.getByText("Advanced query JSON", { exact: true }).click();
  const field = dialog.getByLabel("Query JSON", { exact: true });
  const query = JSON.parse(await field.inputValue());
  expect(query.expr).toBe("vector(233)");
  query.expr = "vector(235)";
  await field.fill(JSON.stringify(query));
  await dialog
    .getByRole("button", { name: "Save queries", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Legacy", exact: true }),
  ).toContainText("235");
  await page.goto(endpoint + "/d/missing-dashboard/unknown");
  await expect(page.getByRole("alert")).toContainText("Dashboard not found.");
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Dashboards", exact: true }),
  ).toBeVisible();
});

test("Grafana table includes every row and supports paging and sorting", async ({
  page,
  endpoint,
}) => {
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const source = {
    name: "Table controls",
    panels: [
      {
        id: "table",
        title: "Full table",
        metric: "",
        aggregation: "last",
        unit: "",
        visualization: "table",
        config: {
          type: "table",
          gridPos: { x: 0, y: 0, w: 24, h: 14 },
          options: {
            enablePagination: true,
            sortBy: [{ displayName: "Number", desc: true }],
            footer: { show: true, reducer: ["sum"], fields: ["Number"] },
          },
          targets: [
            {
              refId: "Snapshot",
              queryType: "snapshot",
              datasource: { uid: "grafana", type: "grafana" },
              snapshot: [
                {
                  schema: {
                    refId: "A",
                    name: "Records",
                    fields: [
                      {
                        name: "Number",
                        type: "number",
                        config: { decimals: 0 },
                      },
                      { name: "Name", type: "string", config: {} },
                    ],
                  },
                  data: {
                    values: [
                      Array.from({ length: 1000 }, (_, i) => i + 1),
                      Array.from({ length: 1000 }, (_, i) => `Record ${i + 1}`),
                    ],
                  },
                },
              ],
            },
          ],
        },
      },
    ],
  };
  const response = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: source,
  });
  expect(response.ok(), await response.text()).toBeTruthy();
  const saved = await response.json();
  await page.goto(endpoint + "/d/" + saved.id + "/table-controls");
  const region = page.getByRole("region", { name: "Full table", exact: true });
  await expect(region.getByRole("table")).toBeVisible();
  await expect(region).toContainText("Record 1000");
  await expect(region).toContainText("500500");
  const sort = region.getByRole("button", {
    name: "Sort by column Number, descending",
    exact: true,
  });
  await sort.click();
  await region
    .getByRole("button", { name: "Sort by column Number", exact: true })
    .click();
  await expect(region).toContainText("Record 1");
  await region.getByRole("button", { name: /^\d+$/ }).last().click();
  await expect(region).toContainText("Record 1000");
  await expect(region).not.toContainText("Showing first 500 rows");
  await page.setViewportSize({ width: 390, height: 900 });
  await expect.poll(async () => region.evaluate((element) => {
    const scroller = element.querySelector(".native-data-table-scroll") as HTMLElement;
    return {
      documentOverflow: document.documentElement.scrollWidth - innerWidth,
      panelOverflow: element.scrollWidth - element.clientWidth,
      tableVisible: scroller.scrollHeight > 0,
    };
  })).toEqual({ documentOverflow: 0, panelOverflow: 0, tableVisible: true });
  const qa = process.env.METRICSPANEL_NATIVE_PANEL_QA_DIR;
  if (qa) {
    await mkdir(qa, { recursive: true });
    await page.screenshot({
      path: path.join(qa, "grafana-table-desktop.png"),
      animations: "disabled",
    });
  }
});

test("Annotation query edits preserve precise raw models in every dashboard format", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(60000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  for (const format of ["native", "classic", "v1", "v2"]) {
    const uid = `annotation-precision-${format}`;
    const query = `{"name":"First","enable":false,"opaque":1e10000,"counter":9007199254740993,"datasource":{"uid":"-- Grafana --","type":"grafana"},"target":{"type":"tags","tags":["maintenance"],"limit":100}}`;
    const other = `{"name":"Second","enable":false,"counter":9007199254740993,"datasource":{"uid":"-- Grafana --","type":"grafana"},"target":{"type":"tags","tags":[],"limit":100}}`;
    const classic = `{"uid":"${uid}","title":"Annotation precision ${format}","opaque":1e10000,"annotations":{"unknown":9007199254740993,"list":[${query},${other}]},"panels":[{"id":1,"type":"text","title":"Keep","gridPos":{"x":0,"y":0,"w":24,"h":8},"options":{"content":"Untouched panel","counter":9007199254740993}}]}`;
    const v2Query = `{"kind":"AnnotationQuery","opaque":1e10000,"counter":9007199254740993,"spec":{"name":"First","enable":false,"query":{"kind":"DataQuery","group":"grafana","datasource":{"name":"-- Grafana --"},"spec":{"type":"tags","tags":["maintenance"],"counter":9007199254740993}}}}`;
    const source =
      format === "native"
        ? `{"name":"Annotation precision ${format}","annotations":[${query},${other}],"panels":[{"id":"keep","title":"Keep","metric":"","unit":"","aggregation":"last","visualization":"text","config":{"type":"text","gridPos":{"x":0,"y":0,"w":24,"h":8},"options":{"content":"Untouched panel","counter":9007199254740993}}}]}`
        : format === "v1"
          ? `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"${uid}"},"spec":${classic}}`
          : format === "v2"
            ? `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"${uid}","opaque":1e10000},"spec":{"title":"Annotation precision ${format}","annotations":[${v2Query},{"kind":"AnnotationQuery","spec":{"name":"Second","enable":false,"query":{"kind":"DataQuery","group":"grafana","datasource":{"name":"-- Grafana --"},"spec":{"type":"tags","tags":[],"counter":9007199254740993}}}}],"elements":{"keep":{"kind":"Panel","spec":{"id":1,"title":"Keep","vizConfig":{"group":"text","spec":{"options":{"content":"Untouched panel","counter":9007199254740993}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":24,"height":8,"element":{"name":"keep"}}}]}}}}`
            : classic;
    const response = await page.request.post(
      endpoint +
        (format === "native" ? "/api/v1/dashboards" : "/api/v1/import/grafana"),
      {
        data: source,
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    const result = await response.json();
    const saved = result.dashboard || result;
    await page.goto(
      endpoint +
        "/d/" +
        (format === "native" ? saved.id : uid) +
        "/annotations?refresh=",
    );
    await page
      .getByRole("button", { name: "Annotation queries", exact: true })
      .click();
    const dialog = page.getByRole("dialog", {
      name: "Annotation queries",
      exact: true,
    });
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("First renamed");
    await dialog
      .getByRole("button", { name: "Move query down", exact: true })
      .click();
    const patch = page.waitForResponse(
      (response) =>
        response.request().method() === "PATCH" &&
        response.url().endsWith(`/dashboards/${saved.id}/annotations`),
    );
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    expect((await patch).status()).toBe(200);
    await expect(dialog).not.toBeVisible();
    const exported = await page.request.get(
      endpoint +
        `/api/v1/dashboard-exports/${saved.id}?format=${format === "native" ? "native" : "grafana"}`,
    );
    expect(exported.ok(), await exported.text()).toBeTruthy();
    const raw = await exported.text();
    expect(raw).toContain("1e10000");
    expect(raw).toContain("9007199254740993");
    expect(raw).not.toContain("9007199254740992");
    expect(raw).toContain("Untouched panel");
    expect(raw.indexOf('"Second"')).toBeLessThan(
      raw.indexOf('"First renamed"'),
    );
    await page.reload();
    await page
      .getByRole("button", { name: "Annotation queries", exact: true })
      .click();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query First renamed",
        exact: true,
      })
      .click();
    await expect(dialog.getByLabel("Query name", { exact: true })).toHaveValue(
      "First renamed",
    );
    await dialog
      .getByRole("button", { name: "Close dialog", exact: true })
      .click();
  }
  expect(errors).toEqual([]);
});

test("Stat, gauge and bar gauge render through first-party registry paths", async ({ page, endpoint }) => {
  test.setTimeout(60000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const start = Date.UTC(2026, 9, 10);
  const snapshot = (title: string, value: number) => [{
    schema: {
      refId: "A",
      name: title,
      fields: [
        { name: "Time", type: "time", config: {} },
        { name: "Number", type: "number", config: { decimals: 0, min: 0, max: 200 } },
      ],
    },
    data: { values: [[start], [value]] },
  }];
  const panel = (id: number, title: string, type: "stat" | "gauge" | "bargauge", value: number) => ({
    id: String(id),
    title,
    metric: "",
    unit: "",
    aggregation: "last",
    visualization: type,
    config: {
      id,
      type,
      gridPos: { x: (id - 1) * 8, y: 0, w: 8, h: 8 },
      options: { reduceOptions: { calcs: ["lastNotNull"] }, orientation: "auto" },
      targets: [{
        refId: "Snapshot",
        queryType: "snapshot",
        datasource: { uid: "grafana", type: "grafana" },
        snapshot: snapshot(title, value),
      }],
    },
  });
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      name: "Value panels",
      panels: [
        panel(1, "Stat value", "stat", 75),
        panel(2, "Gauge value", "gauge", 75),
        panel(3, "Bar gauge value", "bargauge", 50),
      ],
    },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const saved = await created.json();
  await page.goto(endpoint + `/d/${saved.id}/value-panels?from=${start}&to=${start + 60000}&timezone=utc`);
  const stat = page.getByRole("region", { name: "Stat value", exact: true });
  await expect(stat).toHaveClass(/grafana-stat/);
  await expect(stat.locator(".grafana-value strong")).toHaveText("75");
  const gauge = page.getByRole("region", { name: "Gauge value", exact: true });
  const meter = gauge.getByRole("meter");
  await expect(meter).toHaveAttribute("aria-valuenow", "75");
  await expect(meter).toHaveAttribute("aria-valuemin", "0");
  await expect(meter).toHaveAttribute("aria-valuemax", "200");
  await expect(gauge.locator(".grafana-value strong")).toHaveText("75");
  const bar = page.getByRole("region", { name: "Bar gauge value", exact: true });
  const progress = bar.locator("progress");
  await expect(progress).toHaveAttribute("max", "1");
  expect(await progress.evaluate((element) => (element as HTMLProgressElement).value)).toBeCloseTo(0.25, 5);
  await expect(bar.locator(".grafana-value strong")).toHaveText("50");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
});

test("Logs renderer virtualizes ten thousand rows with search and label filters", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const start = Date.UTC(2026, 9, 10);
  const times = Array.from({ length: 10000 }, (_, index) => start + index * 1000);
  const lines = Array.from({ length: 10000 }, (_, index) =>
    index === 2500 ? "needle request failed" : `request ${index} ok`,
  );
  const snapshot = (from: number, to: number, level: string) => ({
    schema: {
      refId: "A",
      name: "Logs",
      fields: [
        { name: "Time", type: "time", config: {} },
        {
          name: "Line",
          type: "string",
          config: {},
          labels: { app: "api", level },
        },
      ],
    },
    data: { values: [times.slice(from, to), lines.slice(from, to)] },
  });
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      name: "Logs dashboard",
      panels: [{
        id: "logs",
        title: "Service logs",
        metric: "",
        unit: "",
        aggregation: "last",
        visualization: "logs",
        config: {
          id: 1,
          type: "logs",
          gridPos: { x: 0, y: 0, w: 24, h: 18 },
          options: {},
          targets: [{
            refId: "Snapshot",
            queryType: "snapshot",
            datasource: { uid: "grafana", type: "grafana" },
            snapshot: [
              snapshot(0, 5000, "error"),
              snapshot(5000, 10000, "info"),
            ],
          }],
        },
      }],
    },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const saved = await created.json();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(
    endpoint + `/d/${saved.id}/logs-dashboard?from=${start}&to=${start + 10000000}&timezone=utc`,
  );
  const panel = page.getByRole("region", { name: "Service logs", exact: true });
  await expect(panel).toHaveAttribute("data-log-count", "10000");
  const viewport = panel.getByRole("log", { name: "Log lines", exact: true });
  await expect(viewport).toBeVisible();
  expect(await panel.locator(".native-logs-row").count()).toBeLessThan(60);
  expect(
    await viewport.evaluate((element) => element.scrollHeight),
  ).toBeGreaterThan(200000);
  const search = panel.getByRole("searchbox", { name: "Search logs", exact: true });
  await search.fill("needle");
  await expect(panel).toHaveAttribute("data-log-count", "1");
  await expect(panel.locator(".native-logs-row")).toContainText(
    "needle request failed",
  );
  await search.fill("");
  await panel.getByLabel("Label key", { exact: true }).selectOption("level");
  await panel.getByLabel("Label value", { exact: true }).selectOption("error");
  await expect(panel).toHaveAttribute("data-log-count", "5000");
  await panel.getByLabel("Label value", { exact: true }).selectOption("info");
  await expect(panel).toHaveAttribute("data-log-count", "5000");
  await panel.getByLabel("Label key", { exact: true }).selectOption("");
  await expect(panel).toHaveAttribute("data-log-count", "10000");
  await search.fill("missing line");
  await expect(panel).toHaveAttribute("data-log-count", "0");
  await expect(panel).toContainText("No logs match the current filters");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
});
