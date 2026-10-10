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
          "metricspanel-sdk-datasource,metricspanel-sdk-app,metricspanel-ext-provider-app,metricspanel-ext-provider-two-app,metricspanel-ext-consumer-app,metricspanel-events-app",
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

test("Dashboard defaults, URL windows, date math and SDK zoom query the selected timestamps", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  await page.emulateMedia({ reducedMotion: "reduce" });
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-time-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "events.zip");
  const run = promisify(execFile),
    errors: string[] = [];
  const queries: { from: string; to: string }[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/ds/query")
      queries.push(request.postDataJSON());
  });
  const end = Date.now() - 60000,
    start = end - 120000;
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(fixture, ["--package-extension-events", archive], {
      windowsHide: true,
    });
    const installed = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(installed.ok(), await installed.text()).toBeTruthy();
    const seeded = await page.request.post(endpoint + "/api/v1/ingest", {
      data: {
        samples: [
          { name: "time_fixture_value", value: 10, timestamp: start + 10000 },
          { name: "time_fixture_value", value: 20, timestamp: end - 30000 },
          { name: "time_fixture_value", value: 99, timestamp: end + 30000 },
        ],
      },
    });
    expect(seeded.ok(), await seeded.text()).toBeTruthy();
    const saved = await page.request.post(endpoint + "/api/dashboards/db", {
      data: {
        overwrite: true,
        dashboard: {
          uid: "time-dashboard",
          title: "Time dashboard",
          timezone: "utc",
          time: { from: String(start), to: String(end) },
          templating: {
            list: [
              {
                name: "site",
                type: "custom",
                query: "node-a,node-b",
                current: { text: "node-a", value: "node-a" },
              },
            ],
          },
          panels: [
            {
              id: 1,
              title: "Time SDK panel",
              type: "metricspanel-events-panel",
              gridPos: { x: 0, y: 0, w: 12, h: 10 },
              targets: [
                { refId: "A", expr: "time_fixture_value", instant: true },
              ],
            },
            {
              id: 2,
              title: "Time native plot",
              type: "timeseries",
              gridPos: { x: 12, y: 0, w: 12, h: 10 },
              targets: [{ refId: "A", expr: "time_fixture_value" }],
            },
          ],
        },
      },
    });
    expect(saved.ok(), await saved.text()).toBeTruthy();
    await page.goto(endpoint + "/d/time-dashboard/time?var-site=node-b");
    const panel = page.getByRole("region", {
      name: "Time SDK panel",
      exact: true,
    });
    await expect(panel).toContainText("Panel value: 20");
    await expect(panel).toContainText(`Panel window: ${start} / ${end} / utc`);
    await expect(panel).toContainText(
      `Panel variables: ${start} / ${end} / 120000`,
    );
    expect(
      queries.some(
        (query) => Number(query.from) === start && Number(query.to) === end,
      ),
    ).toBeTruthy();
    await panel
      .getByRole("button", { name: "SDK zoom window", exact: true })
      .click();
    await expect(panel).toContainText(
      `Panel window: ${start + 20000} / ${end - 20000} / utc`,
    );
    let params = new URL(await page.url()).searchParams;
    expect(Number(params.get("from"))).toBe(start + 20000);
    expect(Number(params.get("to"))).toBe(end - 20000);
    expect(params.get("var-site")).toBe("node-b");
    await page.reload();
    await expect(panel).toContainText(
      `Panel window: ${start + 20000} / ${end - 20000} / utc`,
    );
    await expect(panel).toContainText("Panel value: 20");
    await page
      .getByRole("button", { name: "Choose time range", exact: true })
      .click();
    let dialog = page.getByRole("dialog");
    await dialog.getByLabel("From", { exact: true }).fill(String(start));
    await dialog.getByLabel("To", { exact: true }).fill(String(end + 60000));
    await dialog.getByLabel("Time zone", { exact: true }).fill("Asia/Shanghai");
    await dialog
      .getByRole("button", { name: "Apply time range", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    await expect(panel).toContainText("Panel value: 99");
    await expect(panel).toContainText(
      `Panel window: ${start} / ${end + 60000} / Asia/Shanghai`,
    );
    await page
      .getByRole("button", { name: "Refresh metrics", exact: true })
      .click();
    await expect(panel).toContainText(
      `Panel variables: ${start} / ${end + 60000} / 180000`,
    );
    await page.screenshot({
      path: testInfo.outputPath("time-range-desktop.png"),
      animations: "disabled",
    });
    const alternate = `/d/time-dashboard/time?from=${start}&to=${end}&timezone=utc&var-site=node-a`;
    await page.evaluate(
      (url) => history.pushState(history.state, "", url),
      alternate,
    );
    await page.goBack();
    await page.goForward();
    await expect(
      page.getByRole("combobox", { name: "site", exact: true }),
    ).toHaveValue("node-a");
    await expect(panel).toContainText(`Panel window: ${start} / ${end} / utc`);
    await page.goto(
      endpoint +
        `/d/time-dashboard/time?time=${end}&time.window=10000&timezone=utc`,
    );
    await expect(panel).toContainText(
      `Panel window: ${end - 5000} / ${end + 5000} / utc`,
    );
    await page.goto(
      endpoint +
        `/d/time-dashboard/time?from=${start}&to=${end + 60000}&timezone=utc`,
    );
    const plot = page
      .getByRole("region", { name: "Time native plot", exact: true })
      .locator("svg");
    await expect(plot.locator("polyline")).toHaveCount(1);
    const box = await plot.boundingBox();
    if (!box) throw new Error("Native plot was not laid out");
    await page.mouse.move(box.x + box.width * 0.4, box.y + box.height * 0.4);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * 0.7, box.y + box.height * 0.4, {
      steps: 5,
    });
    await page.mouse.up();
    await expect
      .poll(async () =>
        Number(new URL(await page.url()).searchParams.get("from")),
      )
      .toBeGreaterThan(start);
    params = new URL(await page.url()).searchParams;
    expect(Number(params.get("to"))).toBeLessThan(end + 60000);
    await page
      .getByRole("button", { name: "Choose time range", exact: true })
      .click();
    dialog = page.getByRole("dialog");
    await dialog.getByLabel("From", { exact: true }).fill("now-32d");
    await dialog.getByLabel("To", { exact: true }).fill("now");
    await dialog
      .getByRole("button", { name: "Apply time range", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toContainText("at most 31 days");
    await dialog.getByLabel("From", { exact: true }).fill("now-1h/h");
    await dialog.getByLabel("To", { exact: true }).fill("now/h");
    const hour = Math.floor(Date.now() / 3600000) * 3600000;
    await dialog
      .getByRole("button", { name: "Apply time range", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    await expect(panel).toContainText(
      `Panel window: ${hour - 3600000} / ${hour + 3599999} / utc`,
    );
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("button", { name: "选择时间范围", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText("起始时间");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("time-range-mobile.png"),
      animations: "disabled",
    });
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 1536, height: 1024 });
    const timing = (
      await (
        await page.request.get(endpoint + "/api/dashboards/uid/time-dashboard")
      ).json()
    ).dashboard;
    timing.time = { from: "now-6h", to: "now" };
    timing.refresh = "";
    timing.templating.list.push(
      {
        name: "window",
        type: "custom",
        query: "15m,30m,bad",
        current: { value: "15m" },
      },
      {
        name: "shift",
        type: "custom",
        query: "1h,2h",
        current: { value: "1h" },
      },
    );
    Object.assign(timing.panels[0], {
      timeFrom: "$window",
      timeShift: "$shift",
    });
    Object.assign(timing.panels[1], {
      timeFrom: "30m",
      timeShift: "2h",
      hideTimeOverride: true,
    });
    timing.panels[1].targets[0].refId = "N";
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: timing, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    const before = Date.now();
    expect(
      (
        await page.request.post(endpoint + "/api/v1/ingest", {
          data: {
            samples: [
              {
                name: "time_fixture_value",
                value: 123,
                timestamp: before - 62 * 60000,
              },
              {
                name: "time_fixture_value",
                value: 449,
                timestamp: before - 122 * 60000,
              },
            ],
          },
        })
      ).ok(),
    ).toBeTruthy();
    await page.goto(endpoint + "/d/time-dashboard/time?var-site=node-b");
    await expect(panel).toContainText("Panel value: 123");
    await expect(panel).toContainText("Relative time: 15m · Time shift: 1h");
    const window = /Panel window: (\d+) \/ (\d+) \/ utc/.exec(
      await panel.innerText(),
    )!;
    expect(Number(window[2]) - Number(window[1])).toBe(900000);
    expect(Number(window[2])).toBeGreaterThanOrEqual(before - 3600000);
    expect(Number(window[2])).toBeLessThanOrEqual(Date.now() - 3600000);
    await expect(panel).toContainText(
      `Panel variables: ${window[1]} / ${window[2]} / 900000`,
    );
    expect(
      queries.some(
        (query) =>
          Number(query.from) === Number(window[1]) &&
          Number(query.to) === Number(window[2]),
      ),
    ).toBeTruthy();
    await expect(
      page
        .getByRole("region", { name: "Time native plot", exact: true })
        .locator(".panel-time-info"),
    ).toHaveCount(0);
    await page
      .getByRole("combobox", { name: "shift", exact: true })
      .selectOption("2h");
    await expect(panel).toContainText("Panel value: 449");
    await expect(panel).toContainText("Time shift: 2h");
    await page.goto(
      `${endpoint}/d/time-dashboard/time?from=${before - 120000}&to=${before}&timezone=utc&var-shift=2h`,
    );
    await expect(panel).toContainText(
      `Panel window: ${before - 7320000} / ${before - 7200000} / utc`,
    );
    await expect(panel).toContainText(
      `Panel variables: ${before - 7320000} / ${before - 7200000} / 120000`,
    );
    await expect(panel.locator(".panel-time-info")).toHaveText(
      "Time shift: 2h",
    );
    expect(
      queries.some(
        (query) =>
          Number(query.from) === before - 7320000 &&
          Number(query.to) === before - 7200000,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("panel-time-overrides.png"),
      animations: "disabled",
    });
    await page.goto(endpoint + "/d/time-dashboard/time?var-window=bad");
    await expect(panel.getByRole("alert")).toContainText(
      "Invalid panel relative time",
    );
    expect(errors).toEqual([]);
  } finally {
    if (!page.isClosed())
      await page.goto(endpoint + "/#overview").catch(() => {});
    await page.request.delete(endpoint + "/api/dashboards/uid/time-dashboard");
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-events-app",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-time-e2e-")
    )
      throw new Error("Unsafe time test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("V2 refresh defaults, URL overrides and bilingual controls schedule and stop real panel queries", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let requests = 0;
  page.on("request", (request) => {
    if (
      new URL(request.url()).pathname !== "/api/ds/query" ||
      request.method() !== "POST"
    )
      return;
    if (
      request
        .postDataJSON()
        ?.queries?.some(
          (query: { expr?: string }) => query.expr === "vector(233)",
        )
    )
      requests++;
  });
  const source = {
    apiVersion: "dashboard.grafana.app/v2beta1",
    kind: "Dashboard",
    metadata: { name: "refresh-v2" },
    spec: {
      title: "V2 refresh proof",
      timeSettings: {
        from: "now-1h",
        to: "now",
        timezone: "utc",
        autoRefresh: "7s",
        autoRefreshIntervals: ["7s", "1m"],
      },
      elements: {
        proof: {
          kind: "Panel",
          spec: {
            id: 1,
            title: "Refresh proof",
            data: {
              kind: "QueryGroup",
              spec: {
                queryOptions: { timeFrom: "15m", timeShift: "1h" },
                queries: [
                  {
                    kind: "PanelQuery",
                    spec: {
                      refId: "A",
                      query: {
                        kind: "DataQuery",
                        group: "prometheus",
                        spec: { expr: "vector(233)", instant: true },
                      },
                    },
                  },
                ],
              },
            },
            vizConfig: { kind: "VizConfig", group: "stat", spec: {} },
          },
        },
      },
      layout: {
        kind: "GridLayout",
        spec: {
          items: [
            {
              kind: "GridLayoutItem",
              spec: {
                x: 0,
                y: 0,
                width: 24,
                height: 10,
                element: { kind: "ElementReference", name: "proof" },
              },
            },
          ],
        },
      },
    },
  };
  const imported = await page.request.post(
    endpoint + "/api/v1/import/grafana",
    { data: source },
  );
  expect(imported.ok(), await imported.text()).toBeTruthy();
  const saved = (await imported.json()).dashboard;
  try {
    await page.goto(endpoint + "/d/refresh-v2/refresh");
    const panel = page.getByRole("region", {
      name: "Refresh proof",
      exact: true,
    });
    await expect(panel).toContainText("233");
    await expect.poll(() => requests).toBeGreaterThan(0);
    await expect(panel).toContainText("Relative time: 15m · Time shift: 1h");
    const picker = page.getByRole("combobox", {
      name: "Auto refresh",
      exact: true,
    });
    await expect(picker).toHaveValue("7s");
    await expect(picker.locator("option")).toHaveText([
      "Off",
      "Auto",
      "7s",
      "1m",
    ]);
    await picker.selectOption("");
    const offCount = requests;
    // Observe beyond the former unconditional five-second polling boundary.
    await page.waitForTimeout(6200);
    expect(requests).toBe(offCount);
    await page
      .getByRole("button", { name: "Refresh metrics", exact: true })
      .click();
    await expect.poll(() => requests).toBeGreaterThan(offCount);
    await page.reload();
    await expect(panel).toContainText("233");
    await expect(picker).toHaveValue("");
    expect(new URL(page.url()).searchParams.get("refresh")).toBe("");
    await picker.selectOption("7s");
    const timedCount = requests;
    await expect
      .poll(() => requests, { timeout: 11000 })
      .toBeGreaterThan(timedCount);
    await picker.selectOption("auto");
    expect(new URL(page.url()).searchParams.get("refresh")).toBe("auto");
    const autoCount = requests;
    await expect
      .poll(() => requests, { timeout: 9000 })
      .toBeGreaterThan(autoCount);
    await picker.selectOption("");
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(
      page.getByRole("combobox", { name: "自动刷新", exact: true }),
    ).toHaveValue("");
    expect(
      (await page
        .getByRole("combobox", { name: "自动刷新", exact: true })
        .boundingBox())!.width,
    ).toBeGreaterThanOrEqual(150);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("refresh-mobile.png"),
      animations: "disabled",
    });
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 1536, height: 1024 });
    await picker.selectOption("7s");
    await page.getByRole("button", { name: "Dashboards", exact: true }).click();
    const departedCount = requests;
    await page.waitForTimeout(7400);
    expect(requests).toBe(departedCount);
    await page.goto(endpoint + "/d/refresh-v2/refresh?refresh=0s");
    await expect(page.getByRole("alert")).toContainText(
      "Invalid refresh interval",
    );
    await expect(panel).toContainText("233");
    expect(errors).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview").catch(() => {});
    expect(
      (
        await page.request.delete(endpoint + "/api/v1/dashboards/" + saved.id)
      ).ok(),
    ).toBeTruthy();
  }
});

test("SDK application events refresh real queries, notify ranges and preserve sibling panel subscriptions", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  await page.emulateMedia({ reducedMotion: "reduce" });
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-events-e2e-"),
  );
  const fixture = path.join(
    temp,
    process.platform === "win32" ? "fixture.exe" : "fixture",
  );
  const archive = path.join(temp, "events.zip"),
    run = promisify(execFile);
  const errors: string[] = [];
  let statsRequests = 0,
    queries = 0;
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/stats") statsRequests++;
    if (path === "/api/ds/query") queries++;
  });
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(fixture, ["--package-extension-events", archive], {
      windowsHide: true,
    });
    const repeatedArchive = path.join(temp, "events-repeat.zip");
    await run(fixture, ["--package-extension-events", repeatedArchive], {
      windowsHide: true,
    });
    expect(
      (await readFile(archive)).equals(await readFile(repeatedArchive)),
    ).toBeTruthy();
    const installed = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(installed.ok(), await installed.text()).toBeTruthy();
    const seed = await page.request.post(endpoint + "/api/v1/ingest", {
      data: {
        samples: [
          {
            name: "events_fixture_value",
            value: 233,
            timestamp: Date.now() - 10000,
          },
        ],
      },
    });
    expect(seed.ok(), await seed.text()).toBeTruthy();
    const saved = await page.request.post(endpoint + "/api/dashboards/db", {
      data: {
        dashboard: {
          uid: "events-dashboard",
          title: "Events dashboard",
          panels: [1, 2].map((id) => ({
            id,
            title: "Events panel " + id,
            type: "metricspanel-events-panel",
            gridPos: { x: (id - 1) * 12, y: 0, w: 12, h: 8 },
            targets: [
              { refId: "A", expr: "events_fixture_value", instant: true },
            ],
          })),
        },
        overwrite: true,
      },
    });
    expect(saved.ok(), await saved.text()).toBeTruthy();
    await page.clock.install();
    await page.goto(endpoint + "/d/events-dashboard/events");
    const global = page.getByRole("region", {
      name: "SDK global events",
      exact: true,
    });
    const first = page.getByRole("region", {
      name: "Events panel 1",
      exact: true,
    });
    const second = page.getByRole("region", {
      name: "Events panel 2",
      exact: true,
    });
    await expect(first).toContainText("Panel value: 233");
    await expect(second).toContainText("Panel value: 233");
    await expect(global).toContainText("App refreshes:");
    // Freeze only browser timers to separate explicit SDK events from the native five-second poll.
    await page.clock.pauseAt(new Date());
    const readCount = async (region: typeof global, prefix: string) =>
      Number(
        (await region.getByText(new RegExp("^" + prefix)).innerText()).match(
          /: (\d+)/,
        )![1],
      );
    const appCount = await readCount(global, "App refreshes:");
    const firstCount = await readCount(first, "Panel refreshes:");
    const secondCount = await readCount(second, "Panel refreshes:");
    const timestamp = await page.evaluate(() => Date.now() - 100);
    const changed = await page.request.post(endpoint + "/api/v1/ingest", {
      data: {
        samples: [{ name: "events_fixture_value", value: 234, timestamp }],
      },
    });
    expect(changed.ok(), await changed.text()).toBeTruthy();
    const beforeStats = statsRequests,
      beforeQueries = queries;
    await global
      .getByRole("button", { name: "SDK global refresh", exact: true })
      .click();
    await expect(
      global.getByText("App refreshes: " + (appCount + 1), { exact: true }),
    ).toBeVisible();
    await expect(
      first.getByText("Panel refreshes: " + (firstCount + 1), { exact: true }),
    ).toBeVisible();
    await expect(
      second.getByText("Panel refreshes: " + (secondCount + 1), {
        exact: true,
      }),
    ).toBeVisible();
    await expect
      .poll(async () => {
        await page.clock.runFor(100);
        return first.innerText();
      })
      .toContain("Panel value: 234");
    await expect(second).toContainText("Panel value: 234");
    expect(statsRequests).toBeGreaterThan(beforeStats);
    expect(queries).toBeGreaterThan(beforeQueries);
    await first
      .getByRole("button", { name: "SDK panel refresh", exact: true })
      .click();
    await expect(
      global.getByText("App refreshes: " + (appCount + 2), { exact: true }),
    ).toBeVisible();
    await expect(
      first.getByText("Panel refreshes: " + (firstCount + 2), { exact: true }),
    ).toBeVisible();
    await expect(
      second.getByText("Panel refreshes: " + (secondCount + 2), {
        exact: true,
      }),
    ).toBeVisible();
    await global
      .getByRole("button", { name: "SDK legacy refresh", exact: true })
      .click();
    await expect(
      global.getByText("App refreshes: " + (appCount + 3), { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Time range", exact: true })
      .selectOption("1h");
    await expect(global).toContainText(
      "App ranges: 1 / now-1h / now / 3600000",
    );
    await expect(first).toContainText("Panel ranges: 1");
    await expect(second).toContainText("Panel ranges: 1");
    await global
      .getByRole("button", { name: "Publish custom SDK event", exact: true })
      .click();
    await expect(global).toContainText("Custom events: 1 / legacy: 2");
    await global
      .getByRole("button", { name: "Stop custom events", exact: true })
      .click();
    await global
      .getByRole("button", { name: "Publish custom SDK event", exact: true })
      .click();
    await expect(global).toContainText("Custom events: 1 / legacy: 2");
    await global
      .getByRole("button", { name: "Resume custom events", exact: true })
      .click();
    await global
      .getByRole("button", { name: "Publish custom SDK event", exact: true })
      .click();
    await expect(global).toContainText("Custom events: 2 / legacy: 4");
    for (const [kind, role] of [
      ["Success", "status"],
      ["Warning", "alert"],
      ["Error", "alert"],
      ["Info", "status"],
    ] as const) {
      await global
        .getByRole("button", {
          name: "SDK " + kind + " notification",
          exact: true,
        })
        .click();
      const toast = page.locator(".toast");
      await expect(toast).toHaveAttribute("role", role);
      await expect(toast).toContainText(
        "SDK " +
          kind +
          ": " +
          (kind === "Error" ? "233 failure" : "233 detail"),
      );
      await expect(toast).toHaveClass("toast " + kind.toLowerCase());
      await page.clock.runFor(250);
      await toast
        .getByRole("button", { name: "Dismiss notification", exact: true })
        .click();
      await expect(toast).toHaveCount(0);
    }
    const burstStats = statsRequests;
    await global
      .getByRole("button", { name: "SDK refresh burst", exact: true })
      .click();
    await expect(
      global.getByText("App refreshes: " + (appCount + 28), { exact: true }),
    ).toBeVisible();
    await expect(
      first.getByText("Panel refreshes: " + (firstCount + 28), { exact: true }),
    ).toBeVisible();
    await expect.poll(() => statsRequests - burstStats).toBeGreaterThan(0);
    expect(statsRequests - burstStats).toBeLessThanOrEqual(2);
    await global
      .getByRole("button", { name: "SDK Warning notification", exact: true })
      .click();
    await page.clock.runFor(250);
    await page.screenshot({
      path: testInfo.outputPath("sdk-app-events-desktop.png"),
      animations: "disabled",
    });
    // Unmount both local panel buses, then mount them again. The global bus survives.
    await page.getByRole("button", { name: "Dashboards", exact: true }).click();
    await expect(first).toHaveCount(0);
    await global
      .getByRole("button", { name: "SDK global refresh", exact: true })
      .click();
    await expect(
      global.getByText("App refreshes: " + (appCount + 29), { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Events dashboard", exact: true })
      .click();
    await expect(first).toContainText("Panel refreshes: 0");
    await global
      .getByRole("button", { name: "SDK global refresh", exact: true })
      .click();
    await expect(first).toContainText("Panel refreshes: 1");
    await expect(second).toContainText("Panel refreshes: 1");
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect
      .poll(async () => (await global.boundingBox())?.x ?? -1)
      .toBeGreaterThanOrEqual(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await global
      .getByRole("button", { name: "SDK Error notification", exact: true })
      .click();
    await page.clock.runFor(250);
    await page.screenshot({
      path: testInfo.outputPath("sdk-app-events-mobile.png"),
      animations: "disabled",
    });
    expect(errors).toEqual([]);
  } finally {
    await page.clock.resume().catch(() => {});
    if (!page.isClosed())
      await page.goto(endpoint + "/#overview").catch(() => {});
    await page.request.delete(
      endpoint + "/api/dashboards/uid/events-dashboard",
    );
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-events-app",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-events-e2e-")
    )
      throw new Error("Unsafe events test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("AppPlugin root, configuration pages, encrypted backend context and pinned navigation", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-app-e2e-"),
  );
  const binary = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "app.zip"),
    run = promisify(execFile),
    errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await run(
      "go",
      ["build", "-o", binary, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(binary, ["--package-app", archive], { windowsHide: true });
    const installed = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(installed.ok(), await installed.text()).toBeTruthy();
    await page.goto(endpoint + "/#plugins");
    const app = page
      .locator(".plugin-row")
      .filter({ hasText: "SDK App Fixture" });
    await app.getByRole("button", { name: "Configure", exact: true }).click();
    await page
      .getByLabel("Pin application in navigation", { exact: true })
      .check();
    await page
      .getByLabel("Application JSON settings", { exact: true })
      .fill('{"label":"Operations"}');
    await page
      .getByLabel("Application secret settings", { exact: true })
      .fill('{"apiKey":"app-secret-233"}');
    await page
      .getByRole("button", { name: "Save application", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(
      page
        .getByRole("navigation", { name: "Main navigation" })
        .getByRole("link", { name: "SDK App Fixture", exact: true }),
    ).toBeVisible();
    const meta = await page.request.get(
      endpoint + "/api/plugins/metricspanel-sdk-app/settings",
    );
    expect(await meta.text()).not.toContain("app-secret-233");
    await app
      .getByRole("link", { name: "Open application", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "SDK App Fixture", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Backend configured: yes", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Backend label: Operations", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("link", { name: "Open SDK details", exact: true })
      .click();
    await expect(
      page.getByText("Current path: /a/metricspanel-sdk-app/details", {
        exact: true,
      }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByText("Backend configured: yes", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "SDK settings", exact: true })
      .click();
    await page
      .getByLabel("SDK label", { exact: true })
      .fill("Updated operations");
    await page
      .getByRole("button", { name: "Save SDK configuration", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText("SDK settings saved");
    await page
      .getByRole("button", { name: "Application", exact: true })
      .click();
    await expect(
      page.getByText("Backend label: Updated operations", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Init calls: 1", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await expect(
      page.getByText("Grafana 应用插件", { exact: true }),
    ).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.request.put(endpoint + "/api/v1/plugins/metricspanel-sdk-app", {
      data: { enabled: false },
    });
    await expect(page.getByRole("alert")).toContainText("Plugin is disabled");
    const child = await page.request.get(
      endpoint + "/public/plugins/metricspanel-sdk-datasource/module.js",
    );
    expect(child.status()).toBe(403);
    expect(errors).toEqual([]);
  } finally {
    if (!page.isClosed())
      await page.goto(endpoint + "/#overview").catch(() => {});
    await fetch(endpoint + "/api/v1/plugins/metricspanel-sdk-app", {
      method: "DELETE",
    });
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-app-e2e-")
    )
      throw new Error("Unsafe app test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("SDK UI extensions autoload providers, isolate context, observe updates and revoke stale registrations", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-extensions-e2e-"),
  );
  const binary = path.join(
    temp,
    process.platform === "win32" ? "fixture.exe" : "fixture",
  );
  const run = promisify(execFile),
    errors: string[] = [];
  const provider = "metricspanel-ext-provider-app",
    other = "metricspanel-ext-provider-two-app",
    consumer = "metricspanel-ext-consumer-app";
  page.on("pageerror", (error) => errors.push(error.message));
  const install = async (mode: string) => {
    const archive = path.join(temp, mode + ".zip");
    await run(binary, ["--package-extension-" + mode, archive], {
      windowsHide: true,
    });
    const response = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
  };
  try {
    await run(
      "go",
      ["build", "-o", binary, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await install("provider");
    await install("provider-two");
    await install("consumer");
    for (const [id, label] of [
      [provider, "Operations"],
      [other, "Other operations"],
    ]) {
      const response = await page.request.put(
        endpoint + "/api/v1/plugins/" + id + "/app-settings",
        { data: { version: 0, jsonData: { label } } },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    // The provider's own root is never opened: the consumer and core points load it.
    await page.goto(endpoint + "/a/" + consumer + "/");
    await expect(
      page.getByText("Link count: 2", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Unrestricted link count: 4", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Observable link count: 2", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Observable component count: 2", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Component count: 2", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Function count: 2", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Open 233 / readonly true", exact: true }),
    ).toHaveCount(2);
    await expect(
      page.getByText("Shared context value: 233", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Invalid external link", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Invalid async configure", exact: true }),
    ).toHaveCount(0);
    const badge = page.getByRole("group", {
      name: provider + " exposed",
      exact: true,
    });
    await expect(badge).toContainText(provider + " / Operations / v1");
    await badge
      .getByRole("button", { name: "Badge clicks: 0", exact: true })
      .click();
    // The five-second provider refresh must preserve component state.
    const settings = await (
      await page.request.get(
        endpoint + "/api/v1/plugins/" + provider + "/app-settings",
      )
    ).json();
    const updated = await page.request.put(
      endpoint + "/api/v1/plugins/" + provider + "/app-settings",
      {
        data: {
          version: settings.version,
          jsonData: { label: "Updated operations" },
        },
      },
    );
    expect(updated.ok(), await updated.text()).toBeTruthy();
    await expect(badge).toContainText("Updated operations", { timeout: 15000 });
    await expect(
      badge.getByRole("button", { name: "Badge clicks: 1", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Hide SDK links", exact: true })
      .click();
    await expect(
      page.getByRole("link", { name: "Open 233 / readonly true", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Second visible link", exact: true }),
    ).toHaveCount(2);
    await page
      .getByRole("button", { name: "Show SDK links", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Run extension function", exact: true })
      .click();
    await expect(
      page.getByText("Function result: 234", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Run async function", exact: true })
      .click();
    await expect(
      page.getByText("Function result: 1233", { exact: true }),
    ).toBeVisible();
    await page.request.put(endpoint + "/api/v1/plugins/" + provider, {
      data: { enabled: false },
    });
    await expect(page.getByText("Link count: 1", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await expect(badge).toHaveCount(0);
    await expect(
      page.getByText("Observable link count: 1", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Observable component count: 1", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Run retained function", exact: true })
      .click();
    await expect(
      page.getByText(
        "Function result: Extension provider is disabled or replaced",
        { exact: true },
      ),
    ).toBeVisible();
    await page.request.put(endpoint + "/api/v1/plugins/" + provider, {
      data: { enabled: true },
    });
    await expect(badge).toContainText("Updated operations", { timeout: 15000 });
    await install("provider-v2");
    await expect(badge).toContainText("Updated operations / v2", {
      timeout: 15000,
    });
    await expect(
      badge.getByRole("button", { name: "Badge clicks: 0", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Component count: 2", { exact: true }),
    ).toBeVisible();
    const seeded = await page.request.post(endpoint + "/api/v1/ingest", {
      data: { samples: [{ name: "extension_fixture_value", value: 233 }] },
    });
    expect(seeded.ok(), await seeded.text()).toBeTruthy();
    const dashboard = {
      uid: "extension-dashboard",
      title: "Extension dashboard",
      tags: ["sdk-extensions"],
      panels: [
        {
          id: 23,
          title: "Extension panel",
          type: "stat",
          gridPos: { x: 0, y: 0, w: 24, h: 8 },
          targets: [
            { refId: "A", expr: "extension_fixture_value", instant: true },
          ],
        },
      ],
    };
    const saved = await page.request.post(endpoint + "/api/dashboards/db", {
      data: { dashboard, overwrite: true },
    });
    expect(saved.ok(), await saved.text()).toBeTruthy();
    await page.goto(endpoint + "/d/extension-dashboard/extensions");
    await expect(page.locator(".grafana-value strong")).toHaveText("233");
    await page
      .getByRole("button", {
        name: "Panel actions Extension panel",
        exact: true,
      })
      .click();
    await page
      .getByRole("menuitem", {
        name: /Inspect SDK panel/,
      })
      .first()
      .click();
    const modal = page.getByRole("dialog");
    await expect(modal).toContainText(
      "Panel context: 23 / Extension panel / extension-dashboard / stat",
    );
    await expect(modal).toContainText("Query refs: A");
    await expect(modal).toContainText("Dashboard tags: sdk-extensions");
    await expect(modal).toContainText("Data frames: 1");
    await page.screenshot({
      path: testInfo.outputPath("sdk-extension-modal.png"),
    });
    await modal
      .getByRole("button", { name: "Dismiss SDK modal", exact: true })
      .click();
    await expect(modal).toHaveCount(0);
    await page
      .getByRole("button", { name: "SDK sidebar", exact: true })
      .first()
      .click();
    const sidebar = page.getByRole("complementary", {
      name: "SDK details",
      exact: true,
    });
    await expect(sidebar).toContainText("Provider sidebar");
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("sdk-extension-sidebar-mobile.png"),
    });
    await page
      .getByRole("button", { name: "Close sidebar", exact: true })
      .click();
    await expect(sidebar).toHaveCount(0);
    expect(errors).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview");
    await page.request.delete(
      endpoint + "/api/dashboards/uid/extension-dashboard",
    );
    for (const id of [consumer, provider, other])
      await page.request.delete(endpoint + "/api/v1/plugins/" + id);
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-extensions-e2e-")
    )
      throw new Error("Unsafe extension test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
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
          refresh: "5s",
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
    const chunkStats = async () =>
      (
        await page.request.get(
          endpoint +
            "/api/datasources/uid/frontend-sdk/resources/chunked-stats",
        )
      ).json();
    const chunkTarget = {
      refId: "A",
      value: 233,
      directChunks: true,
      fragmentUTF8: true,
      datasource: { uid: "frontend-sdk", type: "metricspanel-sdk-datasource" },
    };
    const chunksSaved = await page.request.post(
      endpoint + "/api/dashboards/db",
      {
        data: {
          overwrite: true,
          dashboard: {
            uid: "frontend-chunks-dashboard",
            refresh: "5s",
            title: "SDK chunked queries",
            panels: [
              {
                id: 1,
                type: "stat",
                title: "Progressive sums",
                gridPos: { x: 0, y: 0, w: 12, h: 8 },
                targets: [
                  { ...chunkTarget, chunks: 8, delayMs: 1000, secondary: true },
                  { ...chunkTarget, refId: "B", fail: true },
                ],
                options: { reduceOptions: { calcs: ["sum"], values: false } },
                fieldConfig: { defaults: { decimals: 0 }, overrides: [] },
              },
              {
                id: 2,
                type: "table",
                title: "Unicode append",
                gridPos: { x: 12, y: 0, w: 12, h: 8 },
                targets: [
                  { ...chunkTarget, refId: "T", chunks: 3, delayMs: 400 },
                ],
                options: {},
                fieldConfig: { defaults: {}, overrides: [] },
              },
            ],
          },
        },
      },
    );
    expect(chunksSaved.ok(), await chunksSaved.text()).toBeTruthy();
    await page.goto(endpoint + "/d/frontend-chunks-dashboard/chunks");
    const sums = page.getByRole("region", {
      name: "Progressive sums",
      exact: true,
    });
    await expect(sums.locator(".grafana-value strong")).toHaveText([
      "233",
      "466",
    ]);
    expect((await chunkStats()).active).toBeGreaterThan(0);
    const table = page.getByRole("region", {
      name: "Unicode append",
      exact: true,
    });
    await expect(table.getByText("上海🌍", { exact: true })).toHaveCount(3);
    // Seven seconds crosses the app refresh boundary; no extra finite request is allowed.
    await expect(sums.locator(".grafana-value strong")).toHaveText(
      ["1892", "3756"],
      { timeout: 15000 },
    );
    await expect(sums.getByRole("alert")).toContainText(
      "B: fixture chunked query failed",
    );
    await expect
      .poll(chunkStats)
      .toMatchObject({ ref_A: 1, ref_B: 1, active: 0 });
    const cancelSaved = await page.request.post(
      endpoint + "/api/dashboards/db",
      {
        data: {
          overwrite: true,
          dashboard: {
            uid: "frontend-chunks-cancel",
            title: "Cancel chunk query",
            panels: [
              {
                id: 1,
                type: "stat",
                title: "Long chunk query",
                gridPos: { x: 0, y: 0, w: 24, h: 8 },
                targets: [{ ...chunkTarget, chunks: 1000, delayMs: 100 }],
                options: {
                  reduceOptions: { calcs: ["lastNotNull"], values: false },
                },
                fieldConfig: { defaults: {}, overrides: [] },
              },
            ],
          },
        },
      },
    );
    expect(cancelSaved.ok(), await cancelSaved.text()).toBeTruthy();
    const cancelledBefore = (await chunkStats()).cancelled;
    await page.goto(endpoint + "/d/frontend-chunks-cancel/cancel");
    await expect.poll(chunkStats).toMatchObject({ active: 1 });
    await page.getByRole("button", { name: "Overview", exact: true }).click();
    await expect
      .poll(chunkStats)
      .toMatchObject({ active: 0, cancelled: cancelledBefore + 1 });
    const liveDashboard = {
      uid: "frontend-live-dashboard",
      refresh: "5s",
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
      endpoint + "/api/dashboards/uid/frontend-chunks-dashboard",
    );
    await page.request.delete(
      endpoint + "/api/dashboards/uid/frontend-chunks-cancel",
    );
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
  await expect(
    page.getByRole("heading", {
      name: "Stream datasource queries",
      exact: true,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Switch language" }).click();
  await expect(
    page.getByRole("heading", { name: "分块查询数据源", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Switch language" }).click();
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
