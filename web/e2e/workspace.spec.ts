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
          "metricspanel-sdk-datasource,metricspanel-sdk-app,metricspanel-ext-provider-app,metricspanel-ext-provider-two-app,metricspanel-ext-consumer-app,metricspanel-events-app,metricspanel-core-app",
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

test("Expression graphs execute hidden multi-source inputs, preserve templates and resolve both SDK services", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  const temp = await mkdtemp(
      path.join(os.tmpdir(), "metricspanel233-expression-e2e-"),
    ),
    run = promisify(execFile);
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    sourceZip = path.join(temp, "source.zip"),
    probeZip = path.join(temp, "probe.zip");
  const ids: string[] = [],
    issues: string[] = [],
    requests: any[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["warning", "error"].includes(entry.type())) issues.push(entry.text());
  });
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().includes("/api/ds/query")
    ) {
      try {
        requests.push(request.postDataJSON());
      } catch {}
    }
  });
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, timeout: 60000 },
    );
    await run(fixture, ["--package", sourceZip], { cwd: repoRoot });
    await run(fixture, ["--package-extension-core", probeZip], {
      cwd: repoRoot,
    });
    for (const file of [sourceZip, probeZip]) {
      const installed = await page.request.post(
        endpoint + "/api/v1/plugins/install",
        {
          data: await readFile(file),
          headers: { "Content-Type": "application/zip" },
        },
      );
      expect(installed.ok(), await installed.text()).toBeTruthy();
    }
    expect(
      (
        await page.request.post(endpoint + "/api/datasources", {
          data: {
            uid: "expression-sdk",
            name: "Expression SDK",
            type: "metricspanel-sdk-datasource",
            secureJsonData: { apiKey: "test-secret-233" },
          },
        })
      ).ok(),
    ).toBeTruthy();
    const graph = [
      {
        refId: "C",
        datasource: { uid: "__expr__", type: "__expr__" },
        type: "math",
        expression: "$A * $D",
        unknown: 233,
      },
      {
        refId: "A",
        hide: true,
        datasource: { uid: "$source", type: "prometheus" },
        expr: "vector($base)",
        instant: true,
      },
      {
        refId: "B",
        hide: true,
        datasource: {
          uid: "expression-sdk",
          type: "metricspanel-sdk-datasource",
        },
        value: 2,
      },
      {
        refId: "D",
        hide: true,
        datasource: { uid: "-100", type: "__expr__" },
        type: "reduce",
        expression: "B",
        reducer: "mean",
      },
    ];
    const classic: any = {
      uid: "expression-classic",
      title: "Expression template",
      time: { from: "1970-01-01T00:00:01Z", to: "1970-01-01T00:00:04Z" },
      timezone: "utc",
      refresh: "",
      templating: {
        list: [
          {
            name: "base",
            type: "constant",
            query: "233",
            current: { value: "233" },
          },
          {
            name: "source",
            type: "datasource",
            query: "prometheus",
            current: { value: "metricspanel" },
          },
        ],
      },
      panels: [
        {
          id: 1,
          title: "Expression total",
          type: "stat",
          targets: graph,
          timeCompare: "1s",
          gridPos: { x: 0, y: 0, w: 12, h: 8 },
        },
        {
          id: 2,
          title: "Expression threshold",
          type: "stat",
          targets: [
            ...graph.map((q) => ({ ...q, hide: true })),
            {
              refId: "T",
              datasource: { uid: "__expr__" },
              type: "threshold",
              expression: "C",
              conditions: [{ evaluator: { type: "gt", params: [400] } }],
            },
          ],
          gridPos: { x: 12, y: 0, w: 12, h: 8 },
        },
        {
          id: 3,
          title: "Expression resample",
          type: "table",
          targets: [
            {
              refId: "A",
              hide: true,
              datasource: { uid: "grafana" },
              queryType: "randomWalk",
              startValue: 1,
              spread: 0,
            },
            {
              refId: "R",
              datasource: { uid: "__expr__" },
              type: "resample",
              expression: "A",
              window: "1s",
              downsampler: "mean",
              upsampler: "pad",
            },
          ],
          gridPos: { x: 0, y: 8, w: 12, h: 8 },
        },
        {
          id: 4,
          title: "Expression SDK",
          type: "metricspanel-core-app",
          targets: [],
          gridPos: { x: 12, y: 8, w: 12, h: 12 },
        },
      ],
    };
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: classic },
        })
      ).ok(),
    ).toBeTruthy();
    const all = await (
      await page.request.get(endpoint + "/api/v1/dashboards")
    ).json();
    ids.push(all.find((d: any) => d.grafana?.uid === classic.uid).id);
    await page.goto(endpoint + "/d/expression-classic/expression");
    const total = page.getByRole("region", {
      name: "Expression total",
      exact: true,
    });
    await expect(total).toContainText("466");
    await expect(total).not.toContainText("233");
    await expect(
      page.getByRole("region", { name: "Expression threshold", exact: true }),
    ).toContainText("1");
    await expect(
      page
        .getByRole("region", { name: "Expression resample", exact: true })
        .locator("tbody tr"),
    ).toHaveCount(4);
    await expect
      .poll(() =>
        requests.some((request) =>
          request.queries?.some(
            (q: any) =>
              q.refId === "C-compare" &&
              q.expression.includes("A-compare") &&
              q.expression.includes("D-compare"),
          ),
        ),
      )
      .toBe(true);
    expect(
      requests.some(
        (request) =>
          request.queries?.some(
            (q: any) => q.refId === "A" && q.hide && q.expr === "vector(233)",
          ) &&
          request.queries.some(
            (q: any) =>
              q.refId === "B" && q.datasource.uid === "expression-sdk",
          ),
      ),
    ).toBe(true);
    const probe = page.getByRole("region", {
      name: "Core SDK probe",
      exact: true,
    });
    await probe
      .getByRole("button", { name: "Inspect expression SDK", exact: true })
      .click();
    const readProbe = async () =>
      JSON.parse(
        (await probe.getByText(/^Core discovery:/).innerText()).replace(
          "Core discovery: ",
          "",
        ),
      );
    await expect
      .poll(async () => {
        try {
          return (await readProbe()).expression;
        } catch {
          return false;
        }
      })
      .toBe(true);
    const sdk = await readProbe();
    expect(sdk.same).toBe(true);
    expect(sdk.uid).toBe("__expr__");
    expect(sdk.readOnly).toBe(true);
    expect(sdk.reserved).toBe(true);
    expect(sdk.newQuery.datasource.uid).toBe("__expr__");
    expect(sdk.frames).toEqual([{ refId: "C", value: 466 }]);
    expect(sdk.error).toBe("Bad");
    await page.screenshot({
      path: testInfo.outputPath("expressions-desktop.png"),
      animations: "disabled",
    });
    const v1 = {
      apiVersion: "dashboard.grafana.app/v1beta1",
      metadata: { name: "expression-v1", unknown: 233 },
      spec: {
        ...classic,
        uid: undefined,
        title: "Expression V1",
        panels: [{ ...classic.panels[0], timeCompare: "" }],
      },
    };
    const v2 = {
      apiVersion: "dashboard.grafana.app/v2beta1",
      metadata: { name: "expression-v2", unknown: 233 },
      spec: {
        title: "Expression V2",
        timeSettings: {
          from: classic.time.from,
          to: classic.time.to,
          timezone: "utc",
          autoRefresh: "",
        },
        elements: {
          total: {
            kind: "Panel",
            spec: {
              id: 1,
              title: "Expression V2 total",
              data: {
                kind: "QueryGroup",
                spec: {
                  queries: [
                    {
                      kind: "PanelQuery",
                      spec: {
                        refId: "A",
                        hidden: true,
                        query: {
                          kind: "DataQuery",
                          group: "prometheus",
                          spec: {
                            expr: "vector(233)",
                            instant: true,
                            datasource: { uid: "metricspanel" },
                          },
                        },
                      },
                    },
                    {
                      kind: "PanelQuery",
                      spec: {
                        refId: "C",
                        query: {
                          kind: "DataQuery",
                          group: "__expr__",
                          spec: {
                            type: "math",
                            expression: "$A*2",
                            unknown: 233,
                          },
                        },
                      },
                    },
                  ],
                },
              },
              vizConfig: {
                kind: "VizConfig",
                group: "stat",
                spec: {
                  options: {},
                  fieldConfig: { defaults: {}, overrides: [] },
                },
              },
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
                  height: 8,
                  element: { kind: "ElementReference", name: "total" },
                },
              },
            ],
          },
        },
      },
    };
    for (const [resource, uid, label] of [
      [v1, "expression-v1", "Expression total"],
      [v2, "expression-v2", "Expression V2 total"],
    ] as const) {
      const imported = await page.request.post(
        endpoint + "/api/v1/import/grafana",
        { data: resource },
      );
      expect(imported.ok(), await imported.text()).toBeTruthy();
      const saved = (await imported.json()).dashboard;
      ids.push(saved.id);
      const persisted = (
        await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
      ).find((dashboard: any) => dashboard.id === saved.id);
      expect(persisted.grafana).toEqual(JSON.parse(JSON.stringify(resource)));
      await page.goto(endpoint + "/d/" + uid + "/expression");
      await expect(
        page.getByRole("region", { name: label, exact: true }),
      ).toContainText("466");
    }
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("expressions-mobile.png"),
      animations: "disabled",
    });
    expect(issues).toEqual([]);
  } finally {
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-expression-e2e-")
    )
      throw new Error("Unsafe expression fixture cleanup");
    try {
      await page.goto("about:blank", { timeout: 5000 }).catch(() => {});
      for (const id of ids)
        await fetch(endpoint + "/api/v1/dashboards/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
      await fetch(endpoint + "/api/datasources/uid/expression-sdk", {
        method: "DELETE",
        signal: AbortSignal.timeout(10000),
      });
      for (const id of ["metricspanel-core-app", "metricspanel-sdk-datasource"])
        await fetch(endpoint + "/api/v1/plugins/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
    } finally {
      await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
    }
  }
});

test("Builtin Grafana queries render real SDK frames, discover both service generations and cancel measurements", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
      path.join(os.tmpdir(), "metricspanel233-core-grafana-e2e-"),
    ),
    run = promisify(execFile);
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    coreArchive = path.join(temp, "core.zip"),
    dsArchive = path.join(temp, "source.zip");
  const ids: string[] = [],
    annotationIds: number[] = [],
    errors: string[] = [],
    warnings: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type()))
      warnings.push(entry.text());
  });
  const snapshot = {
    schema: {
      refId: "Original",
      name: "Saved values",
      fields: [{ name: "Value", type: "number", config: {} }],
    },
    data: { values: [[233, 234]] },
  };
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, timeout: 60000 },
    );
    await run(fixture, ["--package-extension-core", coreArchive], {
      cwd: repoRoot,
    });
    await run(fixture, ["--package", dsArchive], { cwd: repoRoot });
    for (const file of [coreArchive, dsArchive]) {
      const response = await page.request.post(
        endpoint + "/api/v1/plugins/install",
        {
          data: await readFile(file),
          headers: { "Content-Type": "application/zip" },
        },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    expect(
      (
        await page.request.post(endpoint + "/api/datasources", {
          data: {
            uid: "core-live",
            name: "Core measurements",
            type: "metricspanel-sdk-datasource",
            secureJsonData: { apiKey: "test-secret-233" },
          },
        })
      ).ok(),
    ).toBeTruthy();
    const annotationResponse = await page.request.post(
      endpoint + "/api/annotations",
      {
        data: {
          time: Date.now(),
          text: "Core SDK native event",
          tags: ["core-sdk-233"],
        },
      },
    );
    expect(
      annotationResponse.ok(),
      await annotationResponse.text(),
    ).toBeTruthy();
    annotationIds.push((await annotationResponse.json()).id);
    const model = {
      uid: "core-grafana",
      title: "Core Grafana queries",
      time: { from: "2026-10-05T08:00:00Z", to: "2026-10-05T18:00:00Z" },
      timezone: "utc",
      refresh: "",
      templating: {
        list: [
          {
            name: "source",
            type: "datasource",
            query: "grafana",
            current: { value: "grafana" },
          },
        ],
      },
      panels: [
        {
          id: 1,
          title: "Core random",
          type: "stat",
          datasource: { type: "grafana", uid: "$source" },
          targets: [
            {
              refId: "R",
              queryType: "randomWalk",
              startValue: 233,
              spread: 0,
              noise: 0,
            },
          ],
          gridPos: { x: 0, y: 0, w: 12, h: 8 },
        },
        {
          id: 2,
          title: "Core snapshot",
          type: "table",
          datasource: { type: "grafana" },
          targets: [
            { refId: "S", queryType: "snapshot", snapshot: [snapshot] },
          ],
          gridPos: { x: 12, y: 0, w: 12, h: 8 },
        },
        {
          id: 3,
          title: "Core periods",
          type: "table",
          datasource: "-- Grafana --",
          targets: [
            {
              refId: "T",
              queryType: "timeRegions",
              timeRegion: { from: "09:00", to: "17:00", timezone: "utc" },
            },
          ],
          gridPos: { x: 0, y: 8, w: 12, h: 8 },
        },
        {
          id: 4,
          title: "Core mixed",
          type: "table",
          targets: [
            {
              refId: "R",
              datasource: { uid: "grafana", type: "grafana" },
              queryType: "randomWalk",
              startValue: 42,
              spread: 0,
            },
            {
              refId: "P",
              datasource: { uid: "metricspanel" },
              expr: "vector(300)",
              instant: true,
              format: "table",
            },
          ],
          gridPos: { x: 12, y: 8, w: 12, h: 8 },
        },
        {
          id: 5,
          title: "Core SDK runtime",
          type: "metricspanel-core-app",
          targets: [],
          gridPos: { x: 0, y: 16, w: 24, h: 10 },
        },
      ],
    };
    const imported = await page.request.post(endpoint + "/api/dashboards/db", {
      data: { dashboard: model },
    });
    expect(imported.ok(), await imported.text()).toBeTruthy();
    const saved = (
      await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
    ).find((dashboard: any) => dashboard.grafana?.uid === model.uid);
    ids.push(saved.id);
    await page.goto(endpoint + "/d/core-grafana/core");
    const random = page.getByRole("region", {
        name: "Core random",
        exact: true,
      }),
      snap = page.getByRole("region", { name: "Core snapshot", exact: true }),
      periods = page.getByRole("region", { name: "Core periods", exact: true }),
      mixed = page.getByRole("region", { name: "Core mixed", exact: true });
    await expect(random).toContainText("233");
    await expect(snap).toContainText("234");
    await expect(periods.locator("tbody tr")).toHaveCount(1);
    await expect(mixed).toContainText("300");
    await expect(mixed).toContainText("42");
    await expect(page.getByLabel("source", { exact: true })).toHaveValue(
      "grafana",
    );
    const probe = page.getByRole("region", {
      name: "Core SDK probe",
      exact: true,
    });
    await expect(probe).toBeVisible();
    await probe
      .getByRole("button", { name: "Inspect core source", exact: true })
      .click();
    const parse = async (prefix: string) =>
      JSON.parse(
        (await probe.getByText(new RegExp("^" + prefix)).innerText()).slice(
          prefix.length,
        ),
      );
    await expect
      .poll(async () => {
        try {
          return (await parse("Core discovery: ")).same;
        } catch {
          return false;
        }
      })
      .toBe(true);
    const discovery = await parse("Core discovery: ");
    expect(discovery.uid).toBe("grafana");
    expect(discovery.id).toBe(-1);
    expect(discovery.default).toBe("metricspanel");
    expect(discovery.query.queryType).toBe("randomWalk");
    expect(discovery.modernList).toContain("grafana");
    expect(discovery.legacyList).toContain("grafana");
    expect(discovery.variableList).toContain("${source}");
    expect(discovery.annotations).toHaveLength(1);
    expect(discovery.annotations[0].length).toBe(1);
    expect(
      discovery.annotations[0].fields.find(
        (field: any) => field.name === "text",
      ).values,
    ).toEqual(["Core SDK native event"]);
    expect(
      discovery.annotations[0].fields.find(
        (field: any) => field.name === "created",
      ).type,
    ).toBe("number");
    await expect(probe.getByText(/^Core files:/)).toContainText("index.html");
    await probe
      .getByRole("button", { name: "Register runtime sources", exact: true })
      .click();
    await expect
      .poll(async () => {
        try {
          return (await parse("Core discovery: ")).runtime;
        } catch {
          return false;
        }
      })
      .toBe(true);
    expect((await parse("Core discovery: ")).duplicate).toBe(true);
    await probe
      .getByRole("button", { name: "Start core measurements", exact: true })
      .click();
    await expect
      .poll(async () => {
        const frames = await parse("Core frames: ");
        return Object.values(frames).some(
          (chunk: any) =>
            chunk.state === "Streaming" &&
            chunk.frames.some(
              (frame: any) => frame.refId === "Stream" && frame.value >= 233,
            ),
        );
      })
      .toBe(true);
    const streamed = await parse("Core frames: ");
    expect(streamed.Static.frames[0].refId).toBe("Static");
    expect(streamed.Static.frames[0].value).toBe(233);
    const stats = async () =>
      await (
        await page.request.get(
          endpoint + "/api/datasources/uid/core-live/resources/stream-stats",
        )
      ).json();
    await expect.poll(async () => (await stats()).active).toBe(1);
    await probe
      .getByRole("button", { name: "Stop core measurements", exact: true })
      .click();
    await expect.poll(async () => (await stats()).active).toBe(0);
    await page.screenshot({
      path: testInfo.outputPath("core-grafana-desktop.png"),
      animations: "disabled",
    });
    // V2 type-only group references resolve the builtin even without a UID.
    const v2 = {
      apiVersion: "dashboard.grafana.app/v2beta1",
      metadata: { name: "core-v2", unknown: 233 },
      spec: {
        title: "Core V2",
        timeSettings: {
          from: model.time.from,
          to: model.time.to,
          timezone: "utc",
          autoRefresh: "",
        },
        elements: {
          proof: {
            kind: "Panel",
            spec: {
              id: 1,
              title: "Core V2 snapshot",
              data: {
                kind: "QueryGroup",
                spec: {
                  queries: [
                    {
                      kind: "PanelQuery",
                      spec: {
                        refId: "S",
                        query: {
                          kind: "DataQuery",
                          group: "grafana",
                          spec: {
                            queryType: "snapshot",
                            snapshot: [snapshot],
                            unknown: 233,
                          },
                        },
                      },
                    },
                  ],
                },
              },
              vizConfig: { kind: "VizConfig", group: "table", spec: {} },
            },
          },
        },
      },
    };
    const importedV2 = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      { data: v2 },
    );
    expect(importedV2.ok(), await importedV2.text()).toBeTruthy();
    ids.push((await importedV2.json()).dashboard.id);
    await page.goto(endpoint + "/d/core-v2/core");
    await expect(
      page.getByRole("region", { name: "Core V2 snapshot", exact: true }),
    ).toContainText("234");
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("core-grafana-mobile.png"),
      animations: "disabled",
    });
    await page.goto(endpoint + "/#plugins");
    const builtin = page
      .locator(".plugin-sources tbody tr")
      .filter({ hasText: "-- Grafana --" });
    await expect(builtin).toHaveCount(1);
    await expect(
      builtin.getByRole("button", { name: /编辑|删除/ }),
    ).toHaveCount(0);
    expect(errors).toEqual([]);
    expect(warnings).toEqual([]);
  } finally {
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-core-grafana-e2e-")
    )
      throw new Error("Unsafe core fixture cleanup");
    try {
      await page.goto("about:blank", { timeout: 5000 }).catch(() => {});
      for (const id of annotationIds)
        await fetch(endpoint + "/api/annotations/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
      for (const id of ids)
        await fetch(endpoint + "/api/v1/dashboards/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
      await fetch(endpoint + "/api/datasources/uid/core-live", {
        method: "DELETE",
        signal: AbortSignal.timeout(10000),
      });
      for (const id of ["metricspanel-core-app", "metricspanel-sdk-datasource"])
        await fetch(endpoint + "/api/v1/plugins/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
    } finally {
      await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
    }
  }
});

test("Recurring time regions render SDK frames and native bands, preserve all dashboard formats and never store generated events", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-time-regions-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "events.zip"),
    run = promisify(execFile);
  const ids: string[] = [],
    errors: string[] = [],
    consoleProblems: string[] = [],
    annotationWrites: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type()))
      consoleProblems.push(entry.text());
  });
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      /\/api\/annotations(?:\/|\?|$)/.test(request.url())
    )
      annotationWrites.push(request.url());
  });
  const before = await (
    await page.request.get(endpoint + "/api/annotations?limit=1000")
  ).json();
  const start = Date.parse("2026-10-05T08:00Z"),
    end = Date.parse("2026-10-06T18:00Z");
  const region = (name: string, timeRegion: any, extra = {}) => ({
    name,
    enable: true,
    datasource: { type: "grafana", uid: "-- Grafana --" },
    iconColor: "#ffb357",
    target: { queryType: "timeRegions", timeRegion, unknownTarget: 233 },
    ...extra,
  });
  const office = region("Office hours", {
    from: "09:00",
    to: "17:00",
    timezone: "utc",
    unknownSchedule: 233,
  });
  const source = {
    uid: "regions-classic",
    title: "Recurring regions",
    timezone: "utc",
    time: { from: String(start), to: String(end) },
    refresh: "",
    unknownTemplate: 233,
    annotations: {
      unknownContainer: 233,
      list: [
        office,
        region(
          "Afternoon Cron",
          {
            mode: "cron",
            cronExpr: "0 13 * * MON-FRI",
            duration: "1h",
            timezone: "utc",
          },
          { filter: { ids: [2] }, iconColor: "#b794f4" },
        ),
        region("Broken schedule", {
          mode: "cron",
          cronExpr: "invalid",
          duration: "8h",
          timezone: "utc",
        }),
        region(
          "Disabled",
          { mode: "cron", cronExpr: "invalid", duration: "8h" },
          { enable: false },
        ),
        region(
          "Excluded",
          { from: "09:00", to: "17:00" },
          { filter: { ids: [99] } },
        ),
      ],
    },
    panels: [
      {
        id: 1,
        title: "Regions SDK",
        type: "metricspanel-events-panel",
        targets: [{ refId: "A", expr: "vector(233)", instant: true }],
        gridPos: { x: 0, y: 0, w: 12, h: 12 },
      },
      {
        id: 2,
        title: "Regions curve",
        type: "timeseries",
        targets: [{ refId: "A", expr: "vector(233)" }],
        gridPos: { x: 12, y: 0, w: 12, h: 12 },
      },
    ],
  };
  const open = async () => {
    await page
      .getByRole("button", { name: "Annotation queries", exact: true })
      .click();
    return page.getByRole("dialog", {
      name: "Annotation queries",
      exact: true,
    });
  };
  const saved = async (uid: string) =>
    (
      await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
    ).find(
      (item: any) =>
        item.grafana?.metadata?.name === uid ||
        item.grafana?.uid === uid ||
        item.id === uid,
    );
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, timeout: 60000 },
    );
    await run(fixture, ["--package-extension-events", archive], {
      cwd: repoRoot,
      timeout: 15000,
    });
    const installed = await page.request.post(
      endpoint + "/api/v1/plugins/install",
      {
        data: await readFile(archive),
        headers: { "Content-Type": "application/zip" },
      },
    );
    expect(installed.ok(), await installed.text()).toBeTruthy();
    const imported = await page.request.post(endpoint + "/api/dashboards/db", {
      data: { dashboard: source },
    });
    expect(imported.ok(), await imported.text()).toBeTruthy();
    ids.push((await saved(source.uid)).id);
    await page.goto(endpoint + "/d/regions-classic/regions");
    const sdk = page.getByRole("region", { name: "Regions SDK", exact: true }),
      plot = page.getByRole("region", { name: "Regions curve", exact: true });
    await expect(sdk).toContainText("Panel annotations: 2");
    await expect(sdk).toContainText("Panel value: 233");
    await expect(plot.locator(".annotation-marker rect")).toHaveCount(4);
    await expect(plot).toContainText("Broken schedule:");
    await expect(plot).not.toContainText("Disabled:");
    await sdk.getByText("Annotation frame inspection", { exact: true }).click();
    const frameRows = JSON.parse(
      (await sdk.getByText(/^Annotation frames:/).innerText()).replace(
        "Annotation frames: ",
        "",
      ),
    );
    expect(frameRows).toEqual([
      {
        time: Date.parse("2026-10-05T09:00Z"),
        timeEnd: Date.parse("2026-10-05T17:00Z"),
        text: "Office hours",
      },
      {
        time: Date.parse("2026-10-06T09:00Z"),
        timeEnd: Date.parse("2026-10-06T17:00Z"),
        text: "Office hours",
      },
    ]);
    expect(
      await plot
        .locator(".annotation-marker")
        .evaluateAll((nodes) =>
          nodes.every((node) =>
            node
              .getAttribute("data-annotation-key")
              ?.startsWith("time-region:"),
          ),
        ),
    ).toBeTruthy();
    await plot
      .getByRole("button", { name: "Annotations", exact: true })
      .click();
    const list = page.getByRole("dialog", { name: "Annotations", exact: true });
    await expect(list.locator(".annotation-list > div")).toHaveCount(4);
    await expect(
      list.getByRole("button", { name: "Edit annotation", exact: true }),
    ).toHaveCount(0);
    await expect(
      list.getByRole("button", { name: "Delete annotation", exact: true }),
    ).toHaveCount(0);
    await list.getByRole("button", { name: "Close", exact: true }).click();
    let dialog = await open();
    await expect(
      dialog.getByLabel("Annotation scope", { exact: true }),
    ).toHaveValue("timeRegions");
    await expect(
      dialog.getByLabel("Region start time", { exact: true }),
    ).toHaveValue("09:00");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("2 events found");
    await dialog.getByLabel("Advanced Cron", { exact: true }).check();
    await expect(
      dialog.getByLabel("Cron expression", { exact: true }),
    ).toHaveValue("0 9 * * *");
    await dialog
      .getByLabel("Cron expression", { exact: true })
      .fill("0 10 * * MON-FRI");
    await dialog.getByLabel("Region duration", { exact: true }).fill("6h");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("2 events found");
    await page.screenshot({
      path: testInfo.outputPath("time-regions-editor-desktop.png"),
      animations: "disabled",
    });
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    const edited = (await saved(source.uid)).grafana;
    expect(edited.panels).toEqual(source.panels);
    expect(edited.unknownTemplate).toBe(233);
    expect(edited.annotations.unknownContainer).toBe(233);
    expect(edited.annotations.list[0].target.timeRegion).toMatchObject({
      mode: "cron",
      cronExpr: "0 10 * * MON-FRI",
      duration: "6h",
      unknownSchedule: 233,
    });
    await page.reload();
    await expect(plot.locator(".annotation-marker rect")).toHaveCount(4);
    await expect(sdk).toContainText("Panel value: 233");
    await expect(plot.locator("polyline")).toHaveCount(1);
    await expect(plot.locator(".chart-empty")).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath("time-regions-chart-desktop.png"),
      animations: "disabled",
    });
    // URL range changes recompute generated events, including partial overlaps.
    await page.goto(
      endpoint +
        `/d/regions-classic/regions?from=${Date.parse("2026-10-05T12:00Z")}&to=${Date.parse("2026-10-05T14:00Z")}`,
    );
    await expect(plot.locator(".annotation-marker rect")).toHaveCount(2);
    await expect(sdk).toContainText("Panel annotations: 1");
    const v1 = {
      apiVersion: "dashboard.grafana.app/v1beta1",
      kind: "Dashboard",
      metadata: { name: "regions-v1", unknown: 233 },
      spec: {
        ...source,
        uid: "regions-v1",
        title: "Regions V1",
        annotations: { list: [office] },
      },
    };
    const v2 = {
      apiVersion: "dashboard.grafana.app/v2beta1",
      kind: "Dashboard",
      metadata: { name: "regions-v2", unknown: 233 },
      spec: {
        title: "Regions V2",
        timeSettings: {
          from: String(start),
          to: String(end),
          timezone: "utc",
          autoRefresh: "",
        },
        annotations: [
          {
            kind: "AnnotationQuery",
            unknownResource: 233,
            spec: {
              name: "V2 regions",
              enable: true,
              legacyOptions: { unknownLegacy: 233 },
              query: {
                kind: "DataQuery",
                group: "grafana",
                version: "v0",
                datasource: { name: "-- Grafana --", unknownRef: 233 },
                spec: office.target,
              },
            },
          },
        ],
        elements: {
          curve: {
            kind: "Panel",
            spec: {
              id: 2,
              title: "V2 region curve",
              data: {
                kind: "QueryGroup",
                spec: {
                  queries: [
                    {
                      kind: "PanelQuery",
                      spec: {
                        refId: "A",
                        query: {
                          kind: "DataQuery",
                          group: "prometheus",
                          spec: { expr: "vector(233)" },
                        },
                      },
                    },
                  ],
                },
              },
              vizConfig: { kind: "VizConfig", group: "timeseries", spec: {} },
            },
          },
        },
      },
    };
    for (const resource of [v1, v2]) {
      const imported = await page.request.post(
        endpoint + "/api/v1/import/grafana",
        { data: resource },
      );
      expect(imported.ok(), await imported.text()).toBeTruthy();
      ids.push((await imported.json()).dashboard.id);
      await page.goto(endpoint + `/d/${resource.metadata.name}/regions`);
      await expect(page.locator(".annotation-marker rect")).toHaveCount(2);
      dialog = await open();
      await dialog
        .getByLabel("Region time zone", { exact: true })
        .fill("Asia/Shanghai");
      await dialog
        .getByRole("button", { name: "Save annotation queries", exact: true })
        .click();
      await expect(dialog).toHaveCount(0);
      const raw = (await saved(resource.metadata.name)).grafana;
      expect(raw.metadata).toEqual(resource.metadata);
      if (resource === v2) {
        expect(raw.spec.elements).toEqual(v2.spec.elements);
        const q = raw.spec.annotations[0];
        expect(q.unknownResource).toBe(233);
        expect(q.spec.legacyOptions.unknownLegacy).toBe(233);
        expect(q.spec.query.datasource).toEqual(
          v2.spec.annotations[0].spec.query.datasource,
        );
        expect(q.spec.query.spec.timeRegion.timezone).toBe("Asia/Shanghai");
        expect(q.spec.query.spec.unknownTarget).toBe(233);
      } else
        expect(raw.spec.annotations.list[0].target.timeRegion.timezone).toBe(
          "Asia/Shanghai",
        );
    }
    const native = await page.request.post(endpoint + "/api/v1/dashboards", {
      data: {
        name: "Native regions",
        panels: [
          {
            id: "curve",
            title: "Native region curve",
            metric: "metricspanel_memory_bytes",
            aggregation: "last",
            unit: "bytes",
          },
        ],
      },
    });
    expect(native.ok()).toBeTruthy();
    const nativeID = (await native.json()).id;
    ids.push(nativeID);
    await page.goto(endpoint + "/#dashboards");
    await page
      .getByRole("button", { name: "Native regions", exact: true })
      .click();
    dialog = await open();
    await dialog
      .getByRole("button", { name: "Add annotation query", exact: true })
      .click();
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("Daily coverage");
    await dialog
      .getByLabel("Annotation scope", { exact: true })
      .selectOption("timeRegions");
    await dialog.getByLabel("Region time zone", { exact: true }).fill("utc");
    await dialog.getByLabel("Start weekday", { exact: true }).selectOption("1");
    await dialog.getByLabel("End weekday", { exact: true }).selectOption("2");
    await dialog.getByLabel("Start weekday", { exact: true }).selectOption("");
    await expect(dialog.getByLabel("End weekday", { exact: true })).toHaveValue(
      "",
    );
    await dialog.getByLabel("Region start time", { exact: true }).fill("00:00");
    await dialog.getByLabel("Region end time", { exact: true }).fill("23:59");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Daily coverage");
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    expect((await saved(nativeID)).grafana).toBeNull();
    expect((await saved(nativeID)).annotations[1].target.timeRegion).toEqual({
      timezone: "utc",
      from: "00:00",
      to: "23:59",
    });
    await page.reload();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "注释查询", exact: true }).click();
    const mobile = page.getByRole("dialog", { name: "注释查询", exact: true });
    await mobile
      .getByRole("button", { name: "编辑注释查询 Daily coverage", exact: true })
      .click();
    await expect(mobile.getByLabel("注释范围", { exact: true })).toHaveValue(
      "timeRegions",
    );
    await expect(
      mobile.getByLabel("区域开始时间", { exact: true }),
    ).toHaveValue("00:00");
    expect(
      await mobile.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("time-regions-editor-mobile.png"),
      animations: "disabled",
    });
    await mobile
      .getByRole("button", { name: "关闭对话框", exact: true })
      .click();
    expect(errors).toEqual([]);
    expect(consoleProblems).toEqual([]);
    expect(annotationWrites).toEqual([]);
    expect(
      await (
        await page.request.get(endpoint + "/api/annotations?limit=1000")
      ).json(),
    ).toEqual(before);
  } finally {
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-time-regions-e2e-")
    )
      throw new Error("Unsafe time region fixture cleanup");
    try {
      await page.goto("about:blank", { timeout: 5000 }).catch(() => {});
      // A timed-out Playwright context can already be disposed. Cleanup still
      // targets the owned worker service before its endpoint fixture shuts down.
      for (const id of ids)
        await fetch(endpoint + "/api/v1/dashboards/" + id, {
          method: "DELETE",
          signal: AbortSignal.timeout(10000),
        });
      await fetch(endpoint + "/api/v1/plugins/metricspanel-events-app", {
        method: "DELETE",
        signal: AbortSignal.timeout(10000),
      });
    } finally {
      await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
    }
  }
});

test("Annotation query editors use public SDK components, preserve template resources and save native configuration", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-annotation-editor-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "annotations.zip"),
    run = promisify(execFile);
  const dsUIDs = ["editor-standard", "editor-custom", "editor-legacy"],
    dashboardUIDs = ["editor-proof", "editor-v1", "editor-v2"];
  const errors: string[] = [],
    consoleProblems: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type()))
      consoleProblems.push(entry.text());
  });
  const end = Date.now() - 60000,
    start = end - 120000;
  const mappings = {
    time: { value: "When" },
    timeEnd: { value: "End" },
    text: { value: "Detail" },
    tags: { value: "Tags" },
    id: { value: "EventKey" },
  };
  const source = {
    uid: dashboardUIDs[0],
    title: "Annotation editor proof",
    timezone: "utc",
    time: { from: String(start), to: String(end) },
    refresh: "",
    unknownTemplate: { value: 233 },
    templating: {
      list: [
        {
          name: "service",
          type: "custom",
          query: "api,db",
          current: { value: "api" },
        },
      ],
    },
    annotations: {
      unknownContainer: "retained",
      list: [
        { name: "Native", builtIn: 1, enable: true },
        {
          name: "Standard",
          datasource: { uid: dsUIDs[0], type: "metricspanel-sdk-datasource" },
          target: { annotationText: "Original $service", unknownTarget: 233 },
          mappings,
          enable: true,
          pluginExtension: { unknown: 233 },
        },
        {
          name: "Custom",
          datasource: { uid: dsUIDs[1], type: "metricspanel-sdk-datasource" },
          target: {},
          enable: true,
        },
        {
          name: "Legacy",
          datasource: dsUIDs[2],
          query: "$service",
          enable: true,
        },
      ] as any[],
    },
    panels: [
      {
        id: 2,
        title: "Editable annotation curve",
        type: "timeseries",
        targets: [{ refId: "A", expr: "vector(7)" }],
        gridPos: { x: 0, y: 0, w: 24, h: 10 },
      },
    ],
  };
  let nativeID = "",
    noteID = 0;
  const exported = async (uid: string) => {
    const dashboards = await (
      await page.request.get(endpoint + "/api/v1/dashboards")
    ).json();
    const saved = dashboards.find((item: any) => {
      const raw = item.grafana;
      return (
        raw &&
        (raw.metadata?.name === uid ||
          (raw.dashboard || raw.spec || raw).uid === uid ||
          item.id === uid)
      );
    });
    expect(saved, "Original template exists in native storage").toBeTruthy();
    return saved.grafana;
  };
  const open = async () => {
    await page
      .getByRole("button", { name: "Annotation queries", exact: true })
      .click();
    return page.getByRole("dialog", {
      name: "Annotation queries",
      exact: true,
    });
  };
  const importResource = async (resource: unknown) => {
    const response = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      { data: resource },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
  };
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(fixture, ["--package-annotations", archive], {
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
    for (const [index, uid] of dsUIDs.entries()) {
      const saved = await page.request.post(endpoint + "/api/datasources", {
        data: {
          uid,
          name: uid,
          type: "metricspanel-sdk-datasource",
          jsonData: { annotationMode: ["standard", "custom", "legacy"][index] },
          secureJsonData: { apiKey: "test-secret-233" },
        },
      });
      expect(saved.ok(), await saved.text()).toBeTruthy();
    }
    await importResource({ dashboard: source, meta: { unknownEnvelope: 233 } });
    await page.goto(endpoint + "/d/editor-proof/annotations");
    expect(await page.title()).toContain("MetricsPanel233");
    await expect(
      page.getByRole("heading", { name: source.title, exact: true }),
    ).toBeVisible();
    const beforeCancel = await exported(dashboardUIDs[0]);
    let dialog = await open();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Standard",
        exact: true,
      })
      .click();
    await expect(dialog).toContainText("Editor datasource: editor-standard");
    await expect(dialog).toContainText(`Editor range: ${start} / ${end}`);
    await dialog
      .getByLabel("SDK annotation text", { exact: true })
      .fill("Cancelled edit");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await exported(dashboardUIDs[0])).toEqual(beforeCancel);

    dialog = await open();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Standard",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("SDK annotation text", { exact: true })
      .fill("Edited $service / $__range_ms");
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("Standard edited");
    await dialog.getByText("Event field mappings", { exact: true }).click();
    await dialog
      .getByLabel("Mapping value text", { exact: true })
      .fill("DETAIL");
    await dialog
      .getByRole("button", { name: "Run from SDK editor", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Edited api / 120000");
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Custom",
        exact: true,
      })
      .click();
    await expect(dialog).toContainText(
      "Custom editor datasource: editor-custom",
    );
    await expect(
      dialog.getByLabel("SDK annotation text", { exact: true }),
    ).toHaveCount(0);
    await dialog
      .getByLabel("Custom annotation text", { exact: true })
      .fill("Custom edited $service");
    await dialog
      .getByRole("button", { name: "Run from custom SDK editor", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Custom Custom edited api");
    await expect(dialog).toContainText("Editor frames: 1");
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Legacy",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("Legacy annotation expression", { exact: true })
      .fill("legacy-$service");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Legacy editor-proof legacy-api");
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    const savedClassic = await exported(dashboardUIDs[0]);
    const classic = savedClassic.dashboard;
    expect(savedClassic.meta).toEqual({ unknownEnvelope: 233 });
    expect(classic.panels).toEqual(source.panels);
    expect(classic.templating).toEqual(source.templating);
    expect(classic.unknownTemplate).toEqual(source.unknownTemplate);
    expect(classic.annotations.unknownContainer).toBe("retained");
    expect(classic.annotations.list[1].target.unknownTarget).toBe(233);
    expect(classic.annotations.list[1].target.annotationText).toBe(
      "Edited $service / $__range_ms",
    );
    expect(classic.annotations.list[2].customEditorFlag).toBe("preserved");
    expect(classic.annotations.list[3].query).toBe("legacy-$service");
    await page.reload();
    const curve = page.getByRole("region", {
      name: source.panels[0].title,
      exact: true,
    });
    await expect(curve.locator(".annotation-marker")).toHaveCount(3);
    await expect(curve.locator(".annotation-marker title")).toContainText([
      "Edited api / 120000",
      "Custom Custom edited api",
      "Legacy editor-proof legacy-api",
    ]);

    dialog = await open();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Standard edited",
        exact: true,
      })
      .click();
    await dialog.getByText("Advanced query JSON", { exact: true }).click();
    await dialog.getByLabel("Query JSON", { exact: true }).fill("{");
    await expect(
      dialog.getByRole("button", {
        name: "Save annotation queries",
        exact: true,
      }),
    ).toBeDisabled();
    const delayed = structuredClone(classic.annotations.list[1]);
    delayed.target.annotationDelayMS = 1500;
    await dialog
      .getByLabel("Query JSON", { exact: true })
      .fill(JSON.stringify(delayed));
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    const stats = async () =>
      await (
        await page.request.get(
          endpoint +
            `/api/datasources/uid/${dsUIDs[0]}/resources/annotation-stats`,
        )
      ).json();
    await expect.poll(async () => (await stats()).active).toBeGreaterThan(0);
    const cancelled = (await stats()).cancelled;
    await dialog
      .getByRole("button", { name: "Stop query", exact: true })
      .click();
    await expect
      .poll(async () => (await stats()).cancelled)
      .toBeGreaterThan(cancelled);
    const streamed = structuredClone(classic.annotations.list[1]);
    streamed.target.annotationFrontendStream = true;
    await dialog
      .getByLabel("Query JSON", { exact: true })
      .fill(JSON.stringify(streamed));
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Edited api / 120000");
    await expect(dialog).toContainText("Editor streams: 1 / Streaming");
    await dialog
      .getByRole("button", { name: "Stop query", exact: true })
      .click();
    await expect(dialog).toContainText("Editor streams: 0 / Streaming");
    await expect(
      dialog.getByRole("button", { name: "Stop query", exact: true }),
    ).toHaveCount(0);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();

    const v1 = {
      apiVersion: "dashboard.grafana.app/v1beta1",
      kind: "Dashboard",
      metadata: { name: dashboardUIDs[1], unknown: 233 },
      spec: {
        ...source,
        uid: dashboardUIDs[1],
        title: "V1 annotation editor",
        annotations: { list: [source.annotations.list[1]] },
      },
    };
    const v2 = {
      apiVersion: "dashboard.grafana.app/v2beta1",
      kind: "Dashboard",
      metadata: { name: dashboardUIDs[2], unknown: 233 },
      spec: {
        title: "V2 annotation editor",
        timeSettings: {
          from: String(start),
          to: String(end),
          timezone: "utc",
          autoRefresh: "",
        },
        annotations: [
          {
            kind: "AnnotationQuery",
            unknownResource: 233,
            spec: {
              name: "V2 query",
              enable: true,
              iconColor: "#5ac8de",
              unknownSpec: 233,
              legacyOptions: { mappings, custom: "retained" },
              query: {
                kind: "DataQuery",
                group: "metricspanel-sdk-datasource",
                version: "v0",
                datasource: { name: dsUIDs[0], unknown: 233 },
                spec: { annotationText: "V2 original" },
              },
            },
          },
        ],
        elements: {
          curve: {
            kind: "Panel",
            spec: {
              id: 2,
              title: "V2 editor curve",
              data: {
                kind: "QueryGroup",
                spec: {
                  queries: [
                    {
                      kind: "PanelQuery",
                      spec: {
                        refId: "A",
                        query: {
                          kind: "DataQuery",
                          group: "prometheus",
                          spec: { expr: "vector(7)" },
                        },
                      },
                    },
                  ],
                },
              },
              vizConfig: { kind: "VizConfig", group: "timeseries", spec: {} },
            },
          },
        },
      },
    };
    for (const [index, resource] of [v1, v2].entries()) {
      await importResource(resource);
      await page.goto(endpoint + `/d/${dashboardUIDs[index + 1]}/annotations`);
      dialog = await open();
      await dialog
        .getByLabel("SDK annotation text", { exact: true })
        .fill(index ? "V2 edited" : "V1 edited");
      await dialog
        .getByRole("button", { name: "Save annotation queries", exact: true })
        .click();
      await expect(dialog).toHaveCount(0);
      const result = await exported(dashboardUIDs[index + 1]);
      expect(result.metadata).toEqual(resource.metadata);
      if (index) {
        expect(result.spec.elements).toEqual(v2.spec.elements);
        const annotation = result.spec.annotations[0];
        expect(annotation.unknownResource).toBe(233);
        expect(annotation.spec.unknownSpec).toBe(233);
        expect(annotation.spec.legacyOptions.custom).toBe("retained");
        expect(annotation.spec.query.datasource).toEqual(
          v2.spec.annotations[0].spec.query.datasource,
        );
        expect(annotation.spec.query.spec.annotationText).toBe("V2 edited");
      } else {
        expect(result.spec.panels).toEqual(v1.spec.panels);
        expect(result.spec.annotations.list[0].target.annotationText).toBe(
          "V1 edited",
        );
      }
      await page.reload();
      await expect(page.locator(".annotation-marker title")).toContainText(
        index ? "V2 edited" : "V1 edited",
      );
    }
    const native = await page.request.post(endpoint + "/api/v1/dashboards", {
      data: {
        name: "Native editor",
        panels: [
          {
            id: "native",
            title: "Native annotation curve",
            metric: "metricspanel_memory_bytes",
            aggregation: "last",
            unit: "bytes",
          },
        ],
      },
    });
    expect(native.ok()).toBeTruthy();
    nativeID = (await native.json()).id;
    const note = await page.request.post(endpoint + "/api/annotations", {
      data: {
        dashboardUID: nativeID,
        time: Date.now(),
        text: "Native tagged deployment",
        tags: ["deploy", "prod"],
      },
    });
    expect(note.ok()).toBeTruthy();
    noteID = (await note.json()).id;
    await page.goto(endpoint + `/#dashboards`);
    await page
      .getByRole("button", { name: "Native editor", exact: true })
      .click();
    dialog = await open();
    await dialog
      .getByRole("button", { name: "Add annotation query", exact: true })
      .click();
    await dialog.getByLabel("Query name", { exact: true }).fill("Deploy tags");
    await dialog
      .getByLabel("Tags (comma separated)", { exact: true })
      .fill("deploy, prod");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Native tagged deployment");
    await dialog
      .getByRole("button", { name: "Add annotation query", exact: true })
      .click();
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("Prometheus events");
    await dialog
      .getByLabel("Annotation datasource", { exact: true })
      .selectOption("metricspanel");
    await dialog
      .getByLabel("PromQL expression", { exact: true })
      .fill("vector(12)");
    await dialog.getByText("Event field mappings", { exact: true }).click();
    await dialog
      .getByLabel("Mapping source text", { exact: true })
      .selectOption("text");
    await dialog
      .getByLabel("Mapping value text", { exact: true })
      .fill("Prometheus annotation");
    await dialog
      .getByRole("button", { name: "Test annotation query", exact: true })
      .click();
    await expect(
      dialog.getByRole("region", {
        name: "Annotation query result",
        exact: true,
      }),
    ).toContainText("Prometheus annotation");
    await dialog.getByLabel("Enabled", { exact: true }).uncheck();
    await dialog
      .getByRole("button", { name: "Add annotation query", exact: true })
      .click();
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("Disposable query");
    await dialog
      .getByRole("button", { name: "Move query up", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Move query down", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Delete annotation query", exact: true })
      .click();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Deploy tags",
        exact: true,
      })
      .click();
    await page.setViewportSize({ width: 1536, height: 1024 });
    await page.screenshot({
      path: testInfo.outputPath("annotation-query-editor-desktop.png"),
    });
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    const nativeSaved = (
      await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
    ).find((item: any) => item.id === nativeID);
    expect(nativeSaved.grafana).toBeNull();
    expect(nativeSaved.annotations).toHaveLength(3);
    expect(nativeSaved.annotations[1].target.tags).toEqual(["deploy", "prod"]);
    expect(nativeSaved.annotations[2].target.expr).toBe("vector(12)");
    expect(nativeSaved.annotations[2].enable).toBe(false);
    await page.reload();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "注释查询", exact: true }).click();
    const mobile = page.getByRole("dialog", { name: "注释查询", exact: true });
    await mobile
      .getByRole("button", { name: "编辑注释查询 Deploy tags", exact: true })
      .click();
    await expect(
      mobile.getByLabel("标签（逗号分隔）", { exact: true }),
    ).toHaveValue("deploy, prod");
    expect(
      await mobile.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("annotation-query-editor-mobile.png"),
    });
    await mobile.getByRole("button", { name: "取消", exact: true }).click();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    dialog = await open();
    await dialog
      .getByRole("button", {
        name: "Edit annotation query Deploy tags",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("Query name", { exact: true })
      .fill("Unsaved old draft");
    const concurrent = await page.request.put(
      endpoint + "/api/v1/dashboards/" + nativeID,
      { data: { ...nativeSaved, name: "Concurrent native editor" } },
    );
    expect(concurrent.ok()).toBeTruthy();
    await dialog
      .getByRole("button", { name: "Save annotation queries", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toContainText(
      "Dashboard changed. Close and reopen this editor.",
    );
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    const latestNative = (
      await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
    ).find((item: any) => item.id === nativeID);
    expect(latestNative.name).toBe("Concurrent native editor");
    expect(latestNative.annotations).toEqual(nativeSaved.annotations);
    await expect(page.locator("vite-error-overlay")).toHaveCount(0);
    expect(errors).toEqual([]);
    expect(consoleProblems).toEqual([]);
  } catch (error) {
    await page
      .screenshot({
        path: testInfo.outputPath("annotation-query-editor-failure.png"),
      })
      .catch(() => {});
    throw new Error(
      String(error) +
        "\n" +
        (await page.locator("body").innerText()).slice(-5000) +
        "\nConsole: " +
        JSON.stringify({ errors, consoleProblems }),
    );
  } finally {
    await page.goto(endpoint + "/#overview", { timeout: 5000 }).catch(() => {});
    if (noteID)
      await page.request.delete(endpoint + "/api/annotations/" + noteID);
    if (nativeID)
      await page.request.delete(endpoint + "/api/v1/dashboards/" + nativeID);
    for (const uid of dashboardUIDs)
      await page.request.delete(endpoint + "/api/dashboards/uid/" + uid);
    for (const uid of dsUIDs)
      await page.request.delete(endpoint + "/api/datasources/uid/" + uid);
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-sdk-datasource",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path
        .basename(resolved)
        .startsWith("metricspanel233-annotation-editor-e2e-")
    )
      throw new Error("Unsafe annotation editor fixture cleanup");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("Plugin annotation SDK processors and legacy queries render isolated events and cancel backend work", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-plugin-annotations-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "annotations.zip"),
    eventsArchive = path.join(temp, "events.zip"),
    run = promisify(execFile);
  const dsUIDs = [
      "annotation-standard",
      "annotation-custom",
      "annotation-legacy",
    ],
    errors: string[] = [],
    consoleProblems: string[] = [],
    extraUIDs: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type()))
      consoleProblems.push(entry.text());
  });
  const end = Date.now() - 60000,
    start = end - 120000;
  const mappings = {
    time: { value: "WHEN" },
    timeEnd: { value: "End" },
    text: { value: "DETAIL" },
    tags: { value: "Tags" },
    id: { value: "EventKey" },
  };
  const source = {
    uid: "plugin-annotation-proof",
    title: "Plugin annotation proof",
    timezone: "utc",
    time: { from: String(start), to: String(end) },
    refresh: "",
    templating: {
      list: [
        {
          name: "service",
          label: "Service",
          type: "custom",
          query: "api,db",
          current: { value: "api" },
        },
      ],
    },
    annotations: {
      list: [
        { name: "Native", builtIn: 1, enable: true },
        {
          name: "Standard",
          datasource: { uid: "$sourceDS", type: "metricspanel-sdk-datasource" },
          target: { annotationText: "SDK $service / $__range_ms" },
          mappings,
          iconColor: "#d3baff",
        },
        {
          name: "Custom",
          datasource: { uid: dsUIDs[1], type: "metricspanel-sdk-datasource" },
          target: {},
          iconColor: "#f6c85f",
        },
        {
          name: "Legacy",
          datasource: dsUIDs[2],
          query: "$service",
          iconColor: "#53c7aa",
        },
        {
          name: "Skipped",
          datasource: dsUIDs[0],
          target: { skip: true },
          mappings,
        },
        { name: "Broken", datasource: "missing-annotation-source", target: {} },
        {
          name: "Failed",
          datasource: dsUIDs[0],
          target: { fail: true },
          mappings,
        },
      ] as any[],
    },
    panels: [
      {
        id: 1,
        title: "Plugin annotation SDK",
        type: "metricspanel-events-panel",
        targets: [{ refId: "A", expr: "vector(33)", instant: true }],
        gridPos: { x: 0, y: 0, w: 12, h: 12 },
      },
      {
        id: 2,
        title: "Plugin annotated curve",
        type: "timeseries",
        targets: [{ refId: "A", expr: "vector(33)" }],
        gridPos: { x: 12, y: 0, w: 12, h: 12 },
      },
    ],
  };
  source.templating.list.push({
    name: "sourceDS",
    label: "Source",
    type: "custom",
    query: dsUIDs[0],
    current: { value: dsUIDs[0] },
  });
  const save = async () => {
    const response = await page.request.post(endpoint + "/api/dashboards/db", {
      data: { dashboard: source, overwrite: true },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
  };
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(fixture, ["--package-annotations", archive], {
      windowsHide: true,
    });
    await run(fixture, ["--package-extension-events", eventsArchive], {
      windowsHide: true,
    });
    for (const file of [archive, eventsArchive]) {
      const response = await page.request.post(
        endpoint + "/api/v1/plugins/install",
        {
          data: await readFile(file),
          headers: { "Content-Type": "application/zip" },
        },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    for (const [index, uid] of dsUIDs.entries()) {
      const response = await page.request.post(endpoint + "/api/datasources", {
        data: {
          uid,
          name: uid,
          type: "metricspanel-sdk-datasource",
          jsonData: { annotationMode: ["standard", "custom", "legacy"][index] },
          secureJsonData: { apiKey: "test-secret-233" },
        },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    await save();
    const native = await page.request.post(endpoint + "/api/annotations", {
      data: {
        dashboardUID: source.uid,
        time: start + 30000,
        text: "Native note",
        tags: ["native"],
      },
    });
    expect(native.ok()).toBeTruthy();
    const nativeID = (await native.json()).id;
    await page.goto(endpoint + "/d/plugin-annotation-proof/annotations");
    expect(await page.title()).toContain("MetricsPanel233");
    expect(new URL(page.url()).pathname).toBe(
      "/d/plugin-annotation-proof/annotations",
    );
    await expect(page.locator("vite-error-overlay")).toHaveCount(0);
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    const sdk = page.getByRole("region", {
        name: "Plugin annotation SDK",
        exact: true,
      }),
      plot = page.getByRole("region", {
        name: "Plugin annotated curve",
        exact: true,
      });
    await expect(sdk).toContainText("Panel value: 33");
    await expect(sdk).toContainText("Panel annotations: 4");
    const annotationStats = async () =>
      (
        await page.request.get(
          endpoint +
            `/api/datasources/uid/${dsUIDs[0]}/resources/annotation-stats`,
        )
      ).json();
    const beforeRefresh = (await annotationStats()).started;
    await sdk
      .getByRole("button", { name: "SDK panel refresh", exact: true })
      .click();
    await expect
      .poll(async () => (await annotationStats()).started)
      .toBeGreaterThan(beforeRefresh);
    await expect(sdk).toContainText("Panel annotations: 4");
    await expect(sdk).toContainText(
      "Panel annotation titles: Custom annotation",
    );
    await expect(plot.locator(".annotation-marker")).toHaveCount(4);
    await expect(
      plot.locator(
        `.annotation-marker[data-annotation-key="native:${nativeID}"]`,
      ),
    ).toHaveCount(1);
    await expect(
      plot.locator('.annotation-marker[data-annotation-id="1"]'),
    ).toHaveCount(nativeID === 1 ? 4 : 3);
    await expect(plot.getByRole("alert")).toContainText("Broken");
    await expect(plot.getByRole("alert")).toContainText("Failed");
    await expect(
      plot
        .locator(".annotation-marker title")
        .filter({ hasText: "SDK api / 120000" }),
    ).toHaveCount(2);
    const exported = await (
      await page.request.get(endpoint + "/api/dashboards/uid/" + source.uid)
    ).json();
    expect(exported.dashboard.annotations.list[1].target).toEqual(
      source.annotations.list[1].target,
    );
    source.annotations.list = source.annotations.list.filter(
      (query) => !["Broken", "Failed"].includes(query.name),
    );
    await save();
    await page.reload();
    await expect(sdk).toContainText("Panel annotations: 4");
    await expect(plot.getByRole("alert")).toHaveCount(0);
    await plot
      .getByRole("button", { name: "Annotations", exact: true })
      .click();
    const dialog = page.getByRole("dialog", {
      name: "Annotations",
      exact: true,
    });
    await expect(dialog.locator(".annotation-list > div")).toHaveCount(4);
    await expect(
      dialog.getByRole("button", { name: "Edit annotation", exact: true }),
    ).toHaveCount(1);
    await page.screenshot({
      path: testInfo.outputPath("plugin-annotations-desktop.png"),
      animations: "disabled",
    });
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await page.goto(
      endpoint + "/d/plugin-annotation-proof/annotations?var-service=db",
    );
    await expect(
      plot
        .locator(".annotation-marker title")
        .filter({ hasText: "SDK db / 120000" }),
    ).toHaveCount(2);
    await expect(
      plot
        .locator(".annotation-marker title")
        .filter({ hasText: "Legacy plugin-annotation-proof db" }),
    ).toHaveCount(1);
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("group", { name: "Plugin annotated curve", exact: true })
      .scrollIntoViewIfNeeded();
    await expect(plot.locator(".annotation-marker")).toHaveCount(4);
    await plot.getByRole("button", { name: "注释", exact: true }).click();
    await expect(
      page.getByRole("dialog", { name: "注释", exact: true }),
    ).toContainText("注释内容");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("plugin-annotations-mobile.png"),
      animations: "disabled",
    });
    await page.getByRole("button", { name: "关闭", exact: true }).click();
    await page.setViewportSize({ width: 1440, height: 1000 });
    const v1UID = "plugin-annotation-v1",
      v2UID = "plugin-annotation-v2";
    extraUIDs.push(v1UID, v2UID);
    const resources = [
      {
        apiVersion: "dashboard.grafana.app/v1beta1",
        kind: "Dashboard",
        metadata: { name: v1UID },
        spec: {
          ...source,
          uid: v1UID,
          annotations: { list: [source.annotations.list[1]] },
        },
      },
      {
        apiVersion: "dashboard.grafana.app/v2beta1",
        kind: "Dashboard",
        metadata: { name: v2UID },
        spec: {
          title: "V2 plugin annotation proof",
          timeSettings: {
            from: String(start),
            to: String(end),
            timezone: "utc",
            autoRefresh: "",
          },
          annotations: [
            {
              kind: "AnnotationQuery",
              spec: {
                name: "V2 SDK",
                enable: true,
                legacyOptions: { mappings },
                query: {
                  kind: "DataQuery",
                  group: "metricspanel-sdk-datasource",
                  datasource: { name: dsUIDs[0] },
                  spec: { annotationText: "V2 mapped" },
                },
              },
            },
          ],
          elements: {
            proof: {
              kind: "Panel",
              spec: {
                id: 2,
                title: "V2 plugin curve",
                data: {
                  kind: "QueryGroup",
                  spec: {
                    queries: [
                      {
                        kind: "PanelQuery",
                        spec: {
                          refId: "A",
                          query: {
                            kind: "DataQuery",
                            group: "prometheus",
                            spec: { expr: "vector(33)" },
                          },
                        },
                      },
                    ],
                  },
                },
                vizConfig: { kind: "VizConfig", group: "timeseries", spec: {} },
              },
            },
          },
        },
      },
    ];
    for (const [index, resource] of resources.entries()) {
      const response = await page.request.post(
        endpoint + "/api/v1/import/grafana",
        { data: resource },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      await page.goto(endpoint + "/d/" + extraUIDs[index] + "/annotations");
      const curve = page.getByRole("region", {
        name: index === 0 ? "Plugin annotated curve" : "V2 plugin curve",
        exact: true,
      });
      await expect(curve.locator(".annotation-marker")).toHaveCount(1);
      await expect(curve.locator(".annotation-marker title")).toContainText(
        index === 0 ? "SDK api / 120000" : "V2 mapped",
      );
    }
    source.annotations.list = [
      source.annotations.list[0],
      {
        name: "Slow",
        datasource: dsUIDs[0],
        target: { annotationDelayMS: 1500 },
        mappings,
        filter: { ids: [2] },
      },
    ];
    await save();
    await page.goto(endpoint + "/d/plugin-annotation-proof/annotations");
    const stats = async () =>
      (
        await page.request.get(
          endpoint +
            `/api/datasources/uid/${dsUIDs[0]}/resources/annotation-stats`,
        )
      ).json();
    await expect.poll(async () => (await stats()).active).toBeGreaterThan(0);
    await page.goto(endpoint + "/#overview");
    await expect.poll(async () => (await stats()).cancelled).toBeGreaterThan(0);
    await expect.poll(async () => (await stats()).active).toBe(0);
    expect(errors).toEqual([]);
    expect(
      consoleProblems.filter(
        (message) =>
          !/fixture query failed|Datasource not found: missing-annotation-source/.test(
            message,
          ),
      ),
    ).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview", { timeout: 5000 }).catch(() => {});
    for (const uid of dsUIDs)
      await page.request
        .delete(endpoint + "/api/datasources/uid/" + uid, { timeout: 5000 })
        .catch(() => {});
    const annotations = await page.request
      .get(endpoint + "/api/annotations?dashboardUID=" + source.uid, {
        timeout: 5000,
      })
      .then((response) => response.json())
      .catch(() => []);
    for (const annotation of annotations)
      await page.request
        .delete(endpoint + "/api/annotations/" + annotation.id, {
          timeout: 5000,
        })
        .catch(() => {});
    await page.request
      .delete(endpoint + "/api/dashboards/uid/" + source.uid, { timeout: 5000 })
      .catch(() => {});
    for (const uid of extraUIDs)
      await page.request
        .delete(endpoint + "/api/dashboards/uid/" + uid, { timeout: 5000 })
        .catch(() => {});
    for (const id of ["metricspanel-sdk-datasource", "metricspanel-events-app"])
      await page.request
        .delete(endpoint + "/api/v1/plugins/" + id, { timeout: 5000 })
        .catch(() => {});
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path
        .basename(resolved)
        .startsWith("metricspanel233-plugin-annotations-e2e-")
    )
      throw new Error("Unsafe plugin annotation fixture cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});

test("Durable annotations query template filters, reach SDK frames and support bilingual chart editing", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-annotations-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "events.zip"),
    run = promisify(execFile);
  const ids: number[] = [],
    ruleUIDs: string[] = [],
    errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
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
    expect(
      (
        await page.request.post(endpoint + "/api/v1/plugins/install", {
          data: await readFile(archive),
          headers: { "Content-Type": "application/zip" },
        })
      ).ok(),
    ).toBeTruthy();
    const source = {
      uid: "annotation-dashboard",
      title: "Annotation proof",
      timezone: "utc",
      time: { from: String(start), to: String(end) },
      refresh: "",
      templating: {
        list: [
          {
            name: "releaseTag",
            type: "custom",
            query: "release",
            current: { value: "release" },
          },
        ],
      },
      annotations: {
        list: [
          {
            builtIn: 1,
            name: "Dashboard notes",
            type: "dashboard",
            enable: true,
            hide: true,
            datasource: { type: "grafana", uid: "-- Grafana --" },
          },
          {
            name: "Releases",
            target: {
              type: "tags",
              tags: ["$releaseTag", "prod"],
              matchAny: false,
            },
            enable: true,
            iconColor: "#39d99c",
            datasource: { type: "grafana", uid: "-- Grafana --" },
          },
          { name: "Disabled", type: "tags", tags: ["ignored"], enable: false },
          {
            name: "Other panels",
            type: "tags",
            tags: ["excluded"],
            enable: true,
            filter: { ids: [99] },
          },
        ],
      },
      panels: [
        {
          id: 1,
          title: "Annotation SDK",
          type: "metricspanel-events-panel",
          targets: [{ refId: "A", expr: "vector(233)", instant: true }],
          gridPos: { x: 0, y: 0, w: 12, h: 12 },
        },
        {
          id: 2,
          title: "Annotated curve",
          type: "timeseries",
          targets: [{ refId: "A", expr: "vector(233)" }],
          gridPos: { x: 12, y: 0, w: 12, h: 12 },
        },
      ],
    };
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source },
        })
      ).ok(),
    ).toBeTruthy();
    for (const record of [
      {
        dashboardUID: source.uid,
        panelId: 2,
        time: start - 10000,
        timeEnd: end + 10000,
        text: "Maintenance region",
        tags: ["maintenance"],
      },
      {
        time: start + 60000,
        text: "Release <script>literal</script>",
        tags: ["release", "prod"],
      },
      { time: start + 50000, text: "Ignored", tags: ["ignored"] },
      { time: start + 50000, text: "Excluded", tags: ["excluded"] },
    ]) {
      const response = await page.request.post(endpoint + "/api/annotations", {
        data: record,
      });
      expect(response.ok(), await response.text()).toBeTruthy();
      ids.push((await response.json()).id);
    }
    await page.goto(endpoint + "/d/annotation-dashboard/annotations");
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    const panel = page.getByRole("region", {
        name: "Annotation SDK",
        exact: true,
      }),
      plot = page.getByRole("region", { name: "Annotated curve", exact: true });
    await expect(panel).toContainText("Panel annotations: 1");
    await expect(plot.locator(".annotation-marker")).toHaveCount(2);
    await expect(plot.locator(".annotation-marker rect")).toHaveCount(1);
    expect(await plot.locator(".annotation-marker script").count()).toBe(0);
    await plot
      .getByRole("button", { name: "Annotations", exact: true })
      .click();
    let dialog = page.getByRole("dialog", { name: "Annotations", exact: true });
    await dialog
      .getByLabel("Annotation text", { exact: true })
      .fill("UI deployment");
    await dialog
      .getByLabel("Tags (comma separated)", { exact: true })
      .fill("ui, release");
    await dialog
      .getByRole("button", { name: "Save annotation", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    await expect(plot.locator(".annotation-marker")).toHaveCount(3);
    await expect(panel).toContainText("Panel annotations: 1");
    const stored = await (
      await page.request.get(
        endpoint + "/api/annotations?dashboardUID=annotation-dashboard",
      )
    ).json();
    const created = stored.find(
      (event: { text: string }) => event.text === "UI deployment",
    );
    expect(created.panelId).toBe(2);
    ids.push(created.id);
    await plot
      .getByRole("button", { name: "Annotations", exact: true })
      .click();
    const row = dialog
      .locator(".annotation-list > div")
      .filter({ hasText: "UI deployment" });
    await row
      .getByRole("button", { name: "Edit annotation", exact: true })
      .click();
    await dialog
      .getByLabel("Annotation text", { exact: true })
      .fill("UI edited");
    await dialog
      .getByRole("button", { name: "Save annotation", exact: true })
      .click();
    await expect(
      plot.locator('.annotation-marker[aria-label="Annotation: UI edited"]'),
    ).toHaveCount(1);
    await plot.screenshot({
      path: testInfo.outputPath("annotations-desktop.png"),
      animations: "disabled",
    });
    await plot
      .getByRole("button", { name: "Annotations", exact: true })
      .click();
    await dialog
      .locator(".annotation-list > div")
      .filter({ hasText: "UI edited" })
      .getByRole("button", { name: "Delete annotation", exact: true })
      .click();
    await expect(
      dialog.locator(".annotation-list > div").filter({ hasText: "UI edited" }),
    ).toHaveCount(0);
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(plot.locator(".annotation-marker")).toHaveCount(2);
    // V2 annotation query targets use the same builtin tag adapter.
    const imported = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      {
        data: {
          apiVersion: "dashboard.grafana.app/v2beta1",
          kind: "Dashboard",
          metadata: { name: "annotation-v2" },
          spec: {
            title: "V2 annotation proof",
            timeSettings: {
              from: String(start),
              to: String(end),
              timezone: "utc",
              autoRefresh: "",
            },
            annotations: [
              {
                kind: "AnnotationQuery",
                spec: {
                  name: "V2 releases",
                  enable: true,
                  hide: false,
                  query: {
                    kind: "DataQuery",
                    group: "grafana",
                    datasource: { name: "-- Grafana --" },
                    spec: {
                      type: "tags",
                      tags: ["release", "prod"],
                      matchAny: false,
                    },
                  },
                },
              },
            ],
            elements: {
              proof: {
                kind: "Panel",
                spec: {
                  id: 1,
                  title: "V2 annotated curve",
                  data: {
                    kind: "QueryGroup",
                    spec: {
                      queries: [
                        {
                          kind: "PanelQuery",
                          spec: {
                            refId: "A",
                            query: {
                              kind: "DataQuery",
                              group: "prometheus",
                              spec: { expr: "vector(233)" },
                            },
                          },
                        },
                      ],
                    },
                  },
                  vizConfig: {
                    kind: "VizConfig",
                    group: "timeseries",
                    spec: {},
                  },
                },
              },
            },
          },
        },
      },
    );
    expect(imported.ok(), await imported.text()).toBeTruthy();
    const saved = (await imported.json()).dashboard;
    try {
      await page.goto(endpoint + "/d/annotation-v2/annotations");
      const v2 = page.getByRole("region", {
        name: "V2 annotated curve",
        exact: true,
      });
      await expect(v2.locator(".annotation-marker")).toHaveCount(1);
    } finally {
      await page.request.delete(endpoint + "/api/v1/dashboards/" + saved.id);
    }
    await page.goto(endpoint + "/d/annotation-dashboard/annotations");
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("group", { name: "Annotated curve", exact: true })
      .scrollIntoViewIfNeeded();
    await plot.getByRole("button", { name: "注释", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "注释", exact: true });
    await expect(dialog).toContainText("注释内容");
    await expect(
      dialog.getByRole("button", { name: "保存注释", exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: testInfo.outputPath("annotations-mobile.png"),
      animations: "disabled",
    });
    await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    // Real alert evaluations must reach native markers and public SDK fields.
    source.time.to = String(Date.now() + 60000);
    source.annotations.list.push({
      name: "Service alerts",
      enable: true,
      target: {
        type: "tags",
        tags: ["service:api", "environment:prod"],
        matchAny: false,
      },
      filter: { ids: [1] },
    });
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    const sampleTime = Date.now() - 5000;
    const ingest = async (value: number) => {
      const response = await page.request.post(endpoint + "/api/v1/ingest", {
        data: {
          samples: [
            {
              name: "annotation_alert_flag",
              timestamp: sampleTime,
              value,
              labels: {},
            },
          ],
        },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
    };
    const evaluate = async (uid: string) => {
      await expect
        .poll(async () =>
          (
            await page.request.post(
              endpoint + `/api/v1/alerts/rules/${uid}/evaluate`,
            )
          ).status(),
        )
        .toBe(200);
    };
    await ingest(1);
    ruleUIDs.push("annotation-linked-alert", "annotation-global-alert");
    let response = await page.request.post(
      endpoint + "/api/v1/provisioning/alert-rules",
      {
        data: {
          uid: ruleUIDs[0],
          title: "API deployment alert",
          folderUID: "general",
          ruleGroup: "events",
          condition: "A",
          for: "0s",
          annotations: { __dashboardUid__: source.uid, __panelId__: "2" },
          labels: { service: "api", environment: "prod" },
          data: [
            {
              refId: "A",
              datasourceUid: "metricspanel",
              model: { expr: "annotation_alert_flag == bool 1", instant: true },
            },
          ],
        },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    await evaluate(ruleUIDs[0]);
    await ingest(0);
    await evaluate(ruleUIDs[0]);
    response = await page.request.post(endpoint + "/api/v1/alerts/rules", {
      data: {
        uid: ruleUIDs[1],
        title: "Global service alert",
        expr: "vector(1)",
        condition: "nonzero",
        interval_seconds: 86400,
        labels: { service: "api", environment: "prod" },
      },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    await evaluate(ruleUIDs[1]);
    const alertEvents = await (
      await page.request.get(
        endpoint + `/api/annotations?type=alert&alertUID=${ruleUIDs[0]}`,
      )
    ).json();
    expect(
      alertEvents.map((event: { newState: string }) => event.newState),
    ).toEqual(["Normal", "Alerting"]);
    await page.reload();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    await expect(panel).toContainText("Panel annotations: 2");
    await expect(panel).toContainText("Panel alert states: Alerting");
    await expect(plot.locator(".annotation-marker")).toHaveCount(4);
    await expect(
      plot.locator('[data-alert-state="Alerting"] circle'),
    ).toHaveAttribute("fill", "#f2495c");
    await expect(
      plot.locator('[data-alert-state="Normal"] circle'),
    ).toHaveAttribute("fill", "#39d99c");
    await expect(
      plot.locator('[data-alert-state="Alerting"] title'),
    ).toContainText("正常 → 触发告警");
    await plot.getByRole("button", { name: "注释", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "注释", exact: true });
    const alerts = dialog
      .locator(".annotation-list > div")
      .filter({ hasText: "API deployment alert" });
    await expect(alerts).toHaveCount(2);
    await expect(alerts.first()).toContainText("告警状态");
    await expect(
      alerts.getByRole("button", { name: "编辑注释", exact: true }),
    ).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath("alert-annotations-desktop.png"),
      animations: "disabled",
    });
    await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    expect(errors).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview", { timeout: 5000 }).catch(() => {});
    for (const uid of ruleUIDs) {
      await page.request
        .delete(endpoint + "/api/v1/alerts/rules/" + uid, { timeout: 5000 })
        .catch(() => {});
      const events = await page.request
        .get(endpoint + "/api/annotations?alertUID=" + uid, { timeout: 5000 })
        .then((response) => response.json())
        .catch(() => []);
      for (const event of events) ids.push(event.id);
    }
    for (const id of ids)
      await page.request
        .delete(endpoint + "/api/annotations/" + id, { timeout: 5000 })
        .catch(() => {});
    await page.request.delete(
      endpoint + "/api/dashboards/uid/annotation-dashboard",
    );
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-events-app",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-annotations-e2e-")
    )
      throw new Error("Unsafe annotation fixture cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
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
  const queries: { from: string; to: string; queries?: { refId: string }[] }[] =
    [];
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
      .locator("svg.chart-svg");
    await expect(plot.locator("polyline")).toHaveCount(1);
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
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
    timing.panels.push({
      id: 3,
      title: "Time empty SDK",
      type: "metricspanel-events-panel",
      timeFrom: "15m",
      targets: [],
      gridPos: { x: 0, y: 10, w: 12, h: 8 },
    });
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
    const emptyPanel = page.getByRole("region", {
      name: "Time empty SDK",
      exact: true,
    });
    await emptyPanel.scrollIntoViewIfNeeded();
    await expect(emptyPanel).toContainText("Panel value: empty");
    await expect(emptyPanel).toContainText("Relative time: 15m");
    const emptyWindow = /Panel window: (\d+) \/ (\d+) \/ utc/.exec(
      await emptyPanel.innerText(),
    )!;
    expect(Number(emptyWindow[2]) - Number(emptyWindow[1])).toBe(900000);
    await expect(emptyPanel).toContainText(
      `Panel variables: ${emptyWindow[1]} / ${emptyWindow[2]} / 900000`,
    );
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
    await panel
      .getByRole("button", { name: "SDK zoom window", exact: true })
      .click();
    await expect(panel).toContainText(
      `Panel window: ${before - 7300000} / ${before - 7220000} / utc`,
    );
    params = new URL(page.url()).searchParams;
    expect(Number(params.get("from"))).toBe(before - 100000);
    expect(Number(params.get("to"))).toBe(before - 20000);
    expect(params.get("var-shift")).toBe("2h");
    const shiftedPlot = page
      .getByRole("region", { name: "Time native plot", exact: true })
      .locator("svg.chart-svg");
    await expect(shiftedPlot).toBeVisible();
    // Constant series have a zero-height polyline, while the SVG remains draggable.
    await expect(shiftedPlot.locator("polyline").first()).toHaveAttribute(
      "points",
      /\d/,
    );
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    const shiftedBox = (await shiftedPlot.boundingBox())!;
    await page.mouse.move(
      shiftedBox.x + shiftedBox.width * 0.4,
      shiftedBox.y + shiftedBox.height * 0.4,
    );
    await page.mouse.down();
    await page.mouse.move(
      shiftedBox.x + shiftedBox.width * 0.7,
      shiftedBox.y + shiftedBox.height * 0.4,
      { steps: 5 },
    );
    await page.mouse.up();
    await expect
      .poll(() => Number(new URL(page.url()).searchParams.get("from")))
      .toBeGreaterThan(before - 100000);
    expect(Number(new URL(page.url()).searchParams.get("to"))).toBeLessThan(
      before - 20000,
    );
    await page.goto(
      `${endpoint}/d/time-dashboard/time?from=${before - 240000}&to=${before}&timezone=utc&var-shift=2h`,
    );
    await expect(panel).toContainText(
      `Panel window: ${before - 7440000} / ${before - 7200000} / utc`,
    );
    const nativePanel = page.getByRole("region", {
      name: "Time native plot",
      exact: true,
    });
    await expect(nativePanel.locator("polyline").first()).toHaveAttribute(
      "points",
      /\d/,
    );
    const siblingCount = () =>
      queries.filter((request) =>
        request.queries?.some((query) => query.refId === "N"),
      ).length;
    const siblingBefore = siblingCount(),
      dashboardURL = page.url();
    await panel
      .getByRole("combobox", { name: "Zoom scope", exact: true })
      .selectOption("panel");
    await panel
      .getByRole("button", { name: "SDK zoom window", exact: true })
      .click();
    await expect(panel).toContainText(
      `Panel window: ${before - 7420000} / ${before - 7220000} / utc`,
    );
    expect(page.url()).toBe(dashboardURL);
    expect(siblingCount()).toBe(siblingBefore);
    await expect(
      panel.getByRole("button", { name: "Reset panel zoom", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Refresh metrics", exact: true })
      .click();
    await expect.poll(siblingCount).toBeGreaterThan(siblingBefore);
    await expect(panel).toContainText(
      `Panel window: ${before - 7420000} / ${before - 7220000} / utc`,
    );
    await panel
      .getByRole("button", { name: "Reset panel zoom", exact: true })
      .click();
    await expect(panel).toContainText(
      `Panel window: ${before - 7440000} / ${before - 7200000} / utc`,
    );
    expect(page.url()).toBe(dashboardURL);
    await nativePanel
      .getByRole("combobox", { name: "Zoom scope", exact: true })
      .selectOption("panel");
    const nativeBefore = siblingCount();
    await expect(
      page.getByRole("region", { name: "SDK global events", exact: true }),
    ).toBeVisible();
    const localBox = (await shiftedPlot.boundingBox())!;
    await page.mouse.move(
      localBox.x + localBox.width * 0.4,
      localBox.y + localBox.height * 0.4,
    );
    await page.mouse.down();
    await page.mouse.move(
      localBox.x + localBox.width * 0.7,
      localBox.y + localBox.height * 0.4,
      { steps: 5 },
    );
    await page.mouse.up();
    await expect.poll(siblingCount).toBeGreaterThan(nativeBefore);
    await expect(
      nativePanel.getByRole("button", {
        name: "Reset panel zoom",
        exact: true,
      }),
    ).toBeVisible();
    expect(page.url()).toBe(dashboardURL);
    await nativePanel
      .getByRole("button", { name: "Reset panel zoom", exact: true })
      .click();
    await expect(
      nativePanel.getByRole("button", {
        name: "Reset panel zoom",
        exact: true,
      }),
    ).toHaveCount(0);
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

test("Comparison requests retain raw SDK timestamps, opt out individual queries and align native curves", async ({
  page,
  endpoint,
}, testInfo) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-compare-e2e-"),
  );
  const fixture = path.join(
      temp,
      process.platform === "win32" ? "fixture.exe" : "fixture",
    ),
    archive = path.join(temp, "events.zip");
  const run = promisify(execFile),
    errors: string[] = [];
  const bodies: { from: string; to: string; queries: { refId: string }[] }[] =
    [];
  const importedIDs: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/ds/query")
      bodies.push(request.postDataJSON());
  });
  const end = Date.now() - 60000,
    start = end - 120000,
    day = 86400000;
  try {
    await run(
      "go",
      ["build", "-o", fixture, "./internal/plugins/testdata/sdk-backend"],
      { cwd: repoRoot, windowsHide: true },
    );
    await run(fixture, ["--package-extension-events", archive], {
      windowsHide: true,
    });
    expect(
      (
        await page.request.post(endpoint + "/api/v1/plugins/install", {
          data: await readFile(archive),
          headers: { "Content-Type": "application/zip" },
        })
      ).ok(),
    ).toBeTruthy();
    expect(
      (
        await page.request.post(endpoint + "/api/v1/ingest", {
          data: {
            samples: [
              { name: "compare_fixture", value: 100, timestamp: start + 10000 },
              { name: "compare_fixture", value: 150, timestamp: start + 60000 },
              { name: "compare_fixture", value: 25, timestamp: start - 60000 },
              {
                name: "compare_fixture",
                value: 5,
                timestamp: start - day - 60000,
              },
              {
                name: "compare_fixture",
                value: 10,
                timestamp: start - day + 10000,
              },
              {
                name: "compare_fixture",
                value: 15,
                timestamp: start - day + 60000,
              },
            ],
          },
        })
      ).ok(),
    ).toBeTruthy();
    const source = {
      uid: "compare-dashboard",
      title: "Comparison dashboard",
      timezone: "utc",
      time: { from: String(start), to: String(end) },
      refresh: "",
      panels: [
        {
          id: 1,
          title: "Comparison SDK",
          type: "metricspanel-events-panel",
          timeCompare: "1d",
          compareWith: "bad",
          gridPos: { x: 0, y: 0, w: 12, h: 16 },
          targets: [
            { refId: "A", expr: "compare_fixture", instant: true },
            {
              refId: "B",
              expr: "vector(777)",
              instant: true,
              timeRangeCompare: false,
            },
          ],
        },
        {
          id: 2,
          title: "Comparison curves",
          type: "timeseries",
          timeCompare: "1d",
          compareWith: "bad",
          gridPos: { x: 12, y: 0, w: 12, h: 16 },
          targets: [{ refId: "N", expr: "compare_fixture" }],
        },
      ],
    };
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source },
        })
      ).ok(),
    ).toBeTruthy();
    await page.goto(endpoint + "/d/compare-dashboard/compare");
    const panel = page.getByRole("region", {
        name: "Comparison SDK",
        exact: true,
      }),
      plot = page.getByRole("region", {
        name: "Comparison curves",
        exact: true,
      });
    await expect(panel).toContainText("Panel value: 150");
    await expect(panel).toContainText("Compare with: 1d");
    await panel.getByText("Frame inspection", { exact: true }).click();
    const inspect = async () =>
      JSON.parse(
        (await panel.getByText(/^Frame details:/).innerText()).replace(
          "Frame details: ",
          "",
        ),
      ) as {
        refId: string;
        compare: boolean;
        diff?: number;
        time: number;
        value: number;
      }[];
    await expect
      .poll(async () => (await inspect()).map((frame) => frame.refId).sort())
      .toEqual(["A", "A-compare", "B"]);
    expect(
      (await inspect()).find((frame) => frame.refId === "A-compare"),
    ).toMatchObject({ compare: true, diff: -day, time: end - day, value: 15 });
    expect(
      (await inspect()).find((frame) => frame.refId === "B"),
    ).toMatchObject({ compare: false, value: 777 });
    await expect(panel).toContainText(
      `Panel variables: ${start} / ${end} / 120000`,
    );
    expect(
      bodies.some(
        (body) =>
          Number(body.from) === start - day &&
          Number(body.to) === end - day &&
          body.queries.length === 1 &&
          body.queries[0].refId === "A-compare",
      ),
    ).toBeTruthy();
    expect(
      bodies
        .flatMap((body) => body.queries)
        .some((query) => query.refId === "B-compare"),
    ).toBeFalsy();
    const normal = plot.locator("polyline:not([stroke-dasharray])"),
      compared = plot.locator('polyline[stroke-dasharray="1 5 4 5"]');
    await expect(normal).toHaveCount(1);
    await expect(compared).toHaveCount(1);
    const firstX = async (line: typeof normal) =>
      Number((await line.getAttribute("points"))!.split(/[ ,]/)[0]);
    expect(await firstX(compared)).toBeCloseTo(await firstX(normal), 4);
    for (let round = 0; round < 2; round++) {
      const count = bodies.length;
      await page
        .getByRole("button", { name: "Refresh metrics", exact: true })
        .click();
      await expect.poll(() => bodies.length).toBeGreaterThan(count);
      await expect
        .poll(async () => (await inspect()).map((frame) => frame.refId).sort())
        .toEqual(["A", "A-compare", "B"]);
    }
    await page.screenshot({
      path: testInfo.outputPath("comparison-desktop.png"),
      animations: "disabled",
    });
    source.panels[0].timeCompare = "__previousPeriod";
    source.panels[1].timeCompare = "__previousPeriod";
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    await page.reload();
    await panel.getByText("Frame inspection", { exact: true }).click();
    await expect
      .poll(
        async () =>
          (await inspect()).find((frame) => frame.refId === "A-compare")?.diff,
      )
      .toBe(-120000);
    expect(
      bodies.some(
        (body) =>
          Number(body.from) === start - 120000 &&
          Number(body.to) === start &&
          body.queries.some((query) => query.refId === "A-compare"),
      ),
    ).toBeTruthy();
    await expect(panel).toContainText("Compare with: Previous period");
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(panel).toContainText("比较范围: 前一时段");
    await panel
      .getByRole("combobox", { name: "缩放范围", exact: true })
      .selectOption("panel");
    await expect(
      panel.getByRole("combobox", { name: "缩放范围", exact: true }),
    ).toHaveValue("panel");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await plot.scrollIntoViewIfNeeded();
    await expect(plot).toContainText("比较范围: 前一时段");
    await expect(plot).toContainText("(比较)");
    await plot.screenshot({
      path: testInfo.outputPath("comparison-mobile.png"),
      animations: "disabled",
    });
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await page.setViewportSize({ width: 1536, height: 1024 });
    source.panels[0].timeCompare = "";
    source.panels[1].timeCompare = "";
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    const disabledCount = bodies.length;
    await page.reload();
    await expect(panel).toContainText("Panel value: 150");
    await expect(plot.locator("polyline")).toHaveCount(1);
    await expect(panel.getByRole("alert")).toHaveCount(0);
    expect(
      bodies
        .slice(disabledCount)
        .flatMap((body) => body.queries)
        .some((query) => query.refId.endsWith("-compare")),
    ).toBeFalsy();
    for (const format of ["classic", "v1", "v2"]) {
      const uid = `compare-contract-${format}`;
      const classic = {
        ...source,
        uid,
        panels: source.panels.map((item) => ({
          ...item,
          timeCompare: "1d",
        })),
      };
      const resource =
        format === "classic"
          ? classic
          : format === "v1"
            ? {
                apiVersion: "dashboard.grafana.app/v1beta1",
                kind: "Dashboard",
                metadata: { name: uid },
                spec: classic,
              }
            : {
                apiVersion: "dashboard.grafana.app/v2beta1",
                kind: "Dashboard",
                metadata: { name: uid },
                spec: {
                  title: source.title,
                  timeSettings: {
                    ...source.time,
                    timezone: "utc",
                    autoRefresh: "",
                  },
                  elements: Object.fromEntries(
                    classic.panels.map((item) => [
                      String(item.id),
                      {
                        kind: "Panel",
                        spec: {
                          id: item.id,
                          title: item.title,
                          data: {
                            kind: "QueryGroup",
                            spec: {
                              queryOptions: { timeCompare: "1d" },
                              queries: item.targets.map(
                                ({ refId, ...query }) => ({
                                  kind: "PanelQuery",
                                  spec: {
                                    refId,
                                    hidden: false,
                                    query: {
                                      kind: "DataQuery",
                                      group: "prometheus",
                                      spec: query,
                                    },
                                  },
                                }),
                              ),
                            },
                          },
                          vizConfig: {
                            kind: "VizConfig",
                            group: item.type,
                            spec: {
                              options: {},
                              fieldConfig: { defaults: {}, overrides: [] },
                            },
                          },
                        },
                      },
                    ]),
                  ),
                  layout: {
                    kind: "GridLayout",
                    spec: {
                      items: classic.panels.map((item) => ({
                        kind: "GridLayoutItem",
                        spec: {
                          x: item.gridPos.x,
                          y: item.gridPos.y,
                          width: item.gridPos.w,
                          height: item.gridPos.h,
                          element: {
                            kind: "ElementReference",
                            name: String(item.id),
                          },
                        },
                      })),
                    },
                  },
                },
              };
      const imported = await page.request.post(
        endpoint + "/api/v1/import/grafana",
        { data: resource },
      );
      expect(imported.ok(), await imported.text()).toBeTruthy();
      const saved = (await imported.json()).dashboard;
      importedIDs.push(saved.id);
      expect(saved.grafana).toEqual(resource);
      const count = bodies.length;
      await page.goto(`${endpoint}/d/${uid}/compare`);
      await expect(panel).toContainText("Panel value: 150");
      await expect(panel).toContainText("Compare with: 1d");
      await panel.getByText("Frame inspection", { exact: true }).click();
      await expect
        .poll(async () => (await inspect()).map((frame) => frame.refId).sort())
        .toEqual(["A", "A-compare", "B"]);
      expect(
        (await inspect()).find((frame) => frame.refId === "A-compare"),
      ).toMatchObject({
        compare: true,
        diff: -day,
        time: end - day,
        value: 15,
      });
      await expect(normal).toHaveCount(1);
      await expect(compared).toHaveCount(1);
      expect(
        bodies
          .slice(count)
          .flatMap((body) => body.queries)
          .some((query) => query.refId === "B-compare"),
      ).toBeFalsy();
      const stored = await (
        await page.request.get(endpoint + "/api/v1/dashboards")
      ).json();
      expect(
        stored.find((dashboard: { id: string }) => dashboard.id === saved.id)
          .grafana,
      ).toEqual(resource);
      // Verify the native agent export uses the same lossless source.
      const output = await run(
        process.env.METRICSPANEL_TEST_BINARY ||
          path.join(
            repoRoot,
            "bin",
            process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
          ),
        [
          "dashboards",
          "export",
          "--server",
          endpoint,
          "--id",
          saved.id,
          "--format",
          "grafana",
        ],
        { windowsHide: true, env: { ...process.env, METRICSPANEL_TOKEN: "" } },
      );
      expect(JSON.parse(output.stdout)).toEqual(resource);
    }
    // Earlier MetricsPanel templates using compareWith still execute.
    Reflect.deleteProperty(source.panels[0], "timeCompare");
    Reflect.deleteProperty(source.panels[1], "timeCompare");
    source.panels[0].compareWith = "1d";
    source.panels[1].compareWith = "1d";
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    await page.goto(endpoint + "/d/compare-dashboard/compare");
    await expect(panel).toContainText("Compare with: 1d");
    await expect(compared).toHaveCount(1);
    source.panels[0].timeCompare = "bad";
    source.panels[1].timeCompare = "bad";
    source.panels[1].targets[0].timeRangeCompare = false;
    expect(
      (
        await page.request.post(endpoint + "/api/dashboards/db", {
          data: { dashboard: source, overwrite: true },
        })
      ).ok(),
    ).toBeTruthy();
    await page.reload();
    await expect(panel.getByRole("alert")).toContainText(
      "Invalid comparison interval",
    );
    await expect(panel).toContainText("Panel value: 150");
    await expect(plot.getByRole("alert")).toHaveCount(0);
    await expect(plot.locator("polyline")).toHaveCount(1);
    expect(errors).toEqual([]);
  } finally {
    await page.goto(endpoint + "/#overview").catch(() => {});
    for (const id of importedIDs)
      await page.request
        .delete(endpoint + "/api/v1/dashboards/" + id, { timeout: 5000 })
        .catch(() => {});
    await page.request.delete(
      endpoint + "/api/dashboards/uid/compare-dashboard",
    );
    await page.request.delete(
      endpoint + "/api/v1/plugins/metricspanel-events-app",
    );
    const resolved = path.resolve(temp);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-compare-e2e-")
    )
      throw new Error("Unsafe comparison test cleanup target");
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
