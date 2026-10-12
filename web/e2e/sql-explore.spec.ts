import { test as base, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const external = process.env.METRICSPANEL_SQL_E2E_URL || "";
const token = external
  ? process.env.METRICSPANEL_SQL_E2E_TOKEN ||
    "metricspanel-integration-test-token"
  : "";
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      if (external) {
        await use(external.replace(/\/$/, ""));
        return;
      }
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-sql-explore-e2e-"),
      );
      const binary =
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
          path.join(temp, "test.db"),
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
        logs = "";
      server.stderr.on("data", (chunk) => {
        logs += chunk.toString();
        for (const line of logs.split("\n")) {
          try {
            const record = JSON.parse(line);
            if (record.address) endpoint = `http://${record.address}`;
          } catch {
            /* A log record may be split between chunks. */
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
          const exit = new Promise<void>((resolve) =>
            server.once("exit", () => resolve()),
          );
          server.kill();
          await exit;
        }
        const resolved = path.resolve(temp);
        if (
          !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path
            .basename(resolved)
            .startsWith("metricspanel233-sql-explore-e2e-")
        )
          throw new Error(
            "Refusing cleanup outside SQL browser test directory",
          );
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test.use({
  extraHTTPHeaders: token ? { Authorization: `Bearer ${token}` } : {},
});

test("Desktop SQL annotations query real MySQL and PostgreSQL events and retain edits", async ({
  page,
  endpoint,
}) => {
  test.skip(!external, "Uses the existing single Docker integration fixture");
  test.setTimeout(120000);
  await initialize(page);
  const sources: string[] = [],
    dashboards: string[] = [],
    issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  let failed = false;
  try {
    for (const mysql of [true, false]) {
      const type = mysql ? "mysql" : "grafana-postgresql-datasource";
      const created = await page.request.post(endpoint + "/api/datasources", {
        data: {
          name: "Browser SQL annotations " + type,
          type,
          url: mysql ? "127.0.0.1:3306" : "127.0.0.1:5432",
          user: mysql ? "exporter" : "metrics",
          database: mysql ? "business" : "postgres",
          jsonData: { sslmode: "disable", queryTimeout: 2, maxRows: 1000 },
          secureJsonData: {
            password: mysql
              ? "integration-exporter-password"
              : "integration-postgres-password",
          },
        },
      });
      expect(created.ok(), await created.text()).toBeTruthy();
      const source = await created.json();
      sources.push(source.uid);
      const meta = await (
        await page.request.get(endpoint + "/api/plugins/" + type + "/settings")
      ).json();
      expect(meta.annotations).toBe(true);
      const reference = { uid: source.uid, type };
      const clock = (macro: string) =>
        mysql ? macro : macro + "::timestamptz";
      const point =
        "SELECT $__unixEpochFrom()+60 AS time, CONCAT('Deploy ', '$region') AS text, 'prod, release' AS tags";
      const region = `SELECT ${clock("$__timeFrom()")} AS time, ${clock("$__timeTo()")} AS timeend, 'Maintenance $region' AS text, 'ops, scheduled' AS tags`;
      const added = await page.request.post(endpoint + "/api/v1/dashboards", {
        data: {
          name: "SQL annotation proof " + type,
          variables: [
            {
              name: "region",
              type: "constant",
              query: "west",
              current: "west",
              config: { current: { value: "west" } },
            },
          ],
          annotations: [
            {
              name: "Deploy events",
              enable: true,
              datasource: reference,
              iconColor: "#f2495c",
              rawQuery: point,
              opaque: { retained: "233" },
            },
            {
              name: "Maintenance windows",
              enable: true,
              datasource: reference,
              iconColor: "#ffb357",
              target: {
                refId: "Anno",
                rawSql: region,
                format: "table",
                opaque: "keep",
              },
            },
          ],
          panels: [
            {
              id: "events",
              title: "SQL annotation plot",
              metric: "",
              aggregation: "last",
              unit: "",
              visualization: "timeseries",
              config: {
                id: 1,
                type: "timeseries",
                gridPos: { x: 0, y: 0, w: 24, h: 10 },
                datasource: reference,
                targets: [
                  {
                    refId: "A",
                    datasource: reference,
                    format: "time_series",
                    rawSql: `SELECT ${clock("$__timeFrom()")} AS time, 233 AS value UNION ALL SELECT ${clock("$__timeTo()")} AS time, 466 AS value ORDER BY time`,
                  },
                ],
              },
            },
          ],
        },
      });
      expect(added.ok(), await added.text()).toBeTruthy();
      const dashboard = await added.json();
      dashboards.push(dashboard.id);
      await page.goto(endpoint + "/d/" + dashboard.id + "/sql-annotations");
      const panel = page.getByRole("region", {
        name: "SQL annotation plot",
        exact: true,
      });
      await expect(
        panel.getByRole("img", {
          name: "Annotation: Deploy west",
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        panel
          .getByRole("img", { name: "Annotation: Deploy west", exact: true })
          .locator("title"),
      ).toContainText("prod, release");
      const maintenance = panel.getByRole("img", {
        name: "Annotation: Maintenance west",
        exact: true,
      });
      await expect(maintenance).toBeVisible();
      await expect(maintenance.locator("rect")).toBeVisible();
      await page
        .getByRole("button", { name: "Annotation queries", exact: true })
        .click();
      const editor = page.getByRole("dialog", {
        name: "Annotation queries",
        exact: true,
      });
      await editor
        .getByRole("button", {
          name: "Edit annotation query Deploy events",
          exact: true,
        })
        .click();
      await expect(editor.getByLabel("SQL query", { exact: true })).toHaveValue(
        point,
      );
      await expect(
        editor.getByLabel("Result format", { exact: true }),
      ).toHaveCount(0);
      await editor
        .getByRole("button", { name: "Test annotation query", exact: true })
        .click();
      await expect(editor.locator(".annotation-query-preview")).toContainText(
        "Deploy west",
      );
      const changed = point
        .replace("Deploy ", "Deploy 699 ")
        .replace("AS text", "AS detail");
      await editor.getByLabel("SQL query", { exact: true }).fill(changed);
      await editor.getByText("Event field mappings", { exact: true }).click();
      await editor
        .getByLabel("Mapping value text", { exact: true })
        .fill("detail");
      await editor
        .getByRole("button", { name: "Test annotation query", exact: true })
        .click();
      await expect(editor.locator(".annotation-query-preview")).toContainText(
        "Deploy 699 west",
      );
      await editor
        .getByRole("button", { name: "Save annotation queries", exact: true })
        .click();
      await expect(editor).not.toBeVisible();
      await expect(
        panel.getByRole("img", {
          name: "Annotation: Deploy 699 west",
          exact: true,
        }),
      ).toBeVisible();
      await page.reload();
      await expect(
        panel.getByRole("img", {
          name: "Annotation: Deploy 699 west",
          exact: true,
        }),
      ).toBeVisible();
      const persisted = (
        await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
      ).find((item: any) => item.id === dashboard.id);
      expect(persisted.annotations[0].rawQuery).toBeUndefined();
      expect(persisted.annotations[0].target.rawSql).toBe(changed);
      expect(persisted.annotations[0].target.format).toBe("table");
      expect(persisted.annotations[0].mappings.text.value).toBe("detail");
      expect(persisted.annotations[0].opaque).toEqual({ retained: "233" });
      expect(persisted.annotations[1].target.opaque).toBe("keep");
      if (process.env.METRICSPANEL_SQL_QA_DIR) {
        await mkdir(process.env.METRICSPANEL_SQL_QA_DIR, { recursive: true });
        await page.screenshot({
          path: path.join(
            process.env.METRICSPANEL_SQL_QA_DIR,
            (mysql ? "mysql" : "postgres") + "-annotations-desktop.png",
          ),
          fullPage: true,
          animations: "disabled",
        });
      }
      if (!mysql) {
        await page.getByRole("button", { name: "Switch language", exact: true }).click();
        await page
          .getByRole("button", { name: "注释查询", exact: true })
          .click();
        const localized = page.getByRole("dialog", {
          name: "注释查询",
          exact: true,
        });
        await localized
          .getByRole("button", {
            name: "编辑注释查询 Deploy events",
            exact: true,
          })
          .click();
        await expect(
          localized.getByLabel("SQL 查询", { exact: true }),
        ).toHaveValue(changed);
        await localized.getByText("事件字段映射", { exact: true }).click();
        await expect(
          localized.getByLabel("映射值 text", { exact: true }),
        ).toHaveValue("detail");
        await localized
          .getByRole("button", { name: "测试注释查询", exact: true })
          .click();
        await expect(
          localized.locator(".annotation-query-preview"),
        ).toContainText("Deploy 699 west");
        if (process.env.METRICSPANEL_SQL_QA_DIR)
          await page.screenshot({
            path: path.join(
              process.env.METRICSPANEL_SQL_QA_DIR,
              "postgres-annotation-editor-zh-desktop.png",
            ),
            fullPage: true,
            animations: "disabled",
          });
        await localized
          .getByRole("button", { name: "取消", exact: true })
          .click();
        await expect(localized).not.toBeVisible();
        await page
          .getByRole("button", { name: "Switch language", exact: true })
          .click();
      }
    }
    expect(issues).toEqual([]);
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try {
      await cleanupDashboards(endpoint, dashboards);
      await cleanupSources(endpoint, sources);
    } catch (error) {
      if (!failed) throw error;
    }
  }
});
async function initialize(page: Page) {
  await page.addInitScript(
    ({ token }) => {
      localStorage.setItem("metricspanel-locale", "en");
      if (token) sessionStorage.setItem("metricspanel-token", token);
    },
    { token },
  );
  page.setDefaultTimeout(15000);
}
async function createSource(
  page: Page,
  endpoint: string,
  name: string,
  type: string,
  url: string,
  user: string,
  database: string,
  password: string,
) {
  await page.goto(endpoint + "/#plugins");
  await page
    .getByRole("button", { name: "Add datasource", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Add datasource",
    exact: true,
  });
  await dialog.getByLabel("Datasource name", { exact: true }).fill(name);
  await dialog.getByLabel("Plugin type", { exact: true }).selectOption(type);
  await dialog.getByLabel("URL", { exact: true }).fill(url);
  await dialog.getByLabel("Database user", { exact: true }).fill(user);
  await dialog.getByLabel("Database name", { exact: true }).fill(database);
  await dialog.getByLabel("Database password", { exact: true }).fill(password);
  await dialog.getByLabel("TLS mode", { exact: true }).selectOption("disable");
  await dialog.getByLabel("Query timeout (seconds)", { exact: true }).fill("2");
  await dialog.getByLabel("Maximum result rows", { exact: true }).fill("1000");
  await dialog
    .getByRole("button", { name: "Save datasource", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const sources = await (
    await page.request.get(endpoint + "/api/datasources")
  ).json();
  const source = sources.find((source: any) => source.name === name);
  expect(source).toBeTruthy();
  expect(source.secureJsonFields.password).toBe(true);
  expect(JSON.stringify(source)).not.toContain(password);
  return source;
}
async function cleanupSources(endpoint: string, uids: string[]) {
  for (const uid of uids) {
    const response = await fetch(
      endpoint + "/api/datasources/uid/" + encodeURIComponent(uid),
      {
        method: "DELETE",
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        signal: AbortSignal.timeout(5000),
      },
    );
    if (!response.ok && response.status !== 404)
      throw new Error(
        `SQL test datasource cleanup failed: HTTP ${response.status}`,
      );
  }
}
async function cleanupDashboards(endpoint: string, ids: string[]) {
  for (const id of ids) {
    const response = await fetch(
      endpoint + "/api/v1/dashboards/" + encodeURIComponent(id),
      {
        method: "DELETE",
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        signal: AbortSignal.timeout(5000),
      },
    );
    if (!response.ok && response.status !== 404)
      throw new Error(
        `SQL test dashboard cleanup failed: HTTP ${response.status}`,
      );
  }
}

test("Desktop native SQL configuration preserves secrets and Explore executes configured SDK sources", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  await initialize(page);
  const issues: string[] = [],
    ids: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  let failed = false;
  try {
    const source = await createSource(
      page,
      endpoint,
      "Browser SQL config",
      "mysql",
      "127.0.0.1:1",
      "reader",
      "business",
      "sql-ui-secret-233",
    );
    ids.push(source.uid);
    const sqlMeta = await (
      await page.request.get(endpoint + "/api/plugins/mysql/settings")
    ).json();
    expect(sqlMeta.builtIn).toBe(true);
    await page
      .getByRole("button", { name: "Edit Browser SQL config", exact: true })
      .click();
    let dialog = page.getByRole("dialog", {
      name: "Edit datasource",
      exact: true,
    });
    await expect(
      dialog.getByLabel("Database password", { exact: true }),
    ).toHaveValue("");
    await dialog.getByText("Advanced settings", { exact: true }).click();
    await dialog
      .getByRole("textbox", { name: "Connection settings (JSON)", exact: true })
      .fill(
        JSON.stringify({ ...source.jsonData, opaque: { retained: "233" } }),
      );
    await dialog.getByLabel("Maximum result rows", { exact: true }).fill("233");
    await dialog
      .getByRole("button", { name: "Save datasource", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    let current = await (
      await page.request.get(endpoint + "/api/datasources/uid/" + source.uid)
    ).json();
    expect(current.jsonData.opaque).toEqual({ retained: "233" });
    expect(current.jsonData.maxRows).toBe(233);
    expect(current.secureJsonFields.password).toBe(true);
    await page
      .getByRole("button", { name: "Edit Browser SQL config", exact: true })
      .click();
    dialog = page.getByRole("dialog", { name: "Edit datasource", exact: true });
    await dialog
      .getByLabel("Remove stored database password", { exact: true })
      .check();
    await dialog
      .getByRole("button", { name: "Save datasource", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    current = await (
      await page.request.get(endpoint + "/api/datasources/uid/" + source.uid)
    ).json();
    expect(current.secureJsonFields.password).toBeUndefined();

    await page.goto(endpoint + "/#explore");
    await expect(
      page.getByRole("heading", { name: "Explore", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("combobox", { name: "Metric", exact: true }),
    ).toBeVisible();
    await page
      .getByLabel("Datasource", { exact: true })
      .selectOption(source.uid);
    await expect(page.getByLabel("SQL query", { exact: true })).toHaveValue(
      "SELECT 1 AS value",
    );
    await expect(page.getByLabel("Result format", { exact: true })).toHaveValue(
      "table",
    );
    await page
      .getByLabel("Datasource", { exact: true })
      .selectOption("grafana");
    await expect(
      page.getByLabel("Builtin query type", { exact: true }),
    ).toBeVisible();
    await page.getByLabel("Start value", { exact: true }).fill("233");
    await page.getByLabel("Spread", { exact: true }).fill("0");
    await page.getByLabel("Noise", { exact: true }).fill("0");
    await page.getByRole("button", { name: "Run query", exact: true }).click();
    const result = page
      .getByRole("region", {
        name: "Query result",
        exact: true,
      })
      .first();
    await expect(result).toContainText("233");
    await expect(
      result.getByRole("button", { name: "Next", exact: true }),
    ).toBeVisible();
    await result.getByRole("button", { name: "Next", exact: true }).click();
    await expect(result).toContainText("2 / 3");
    await result
      .getByRole("button", { name: "Time series view", exact: true })
      .click();
    await expect(result.locator(".chart-wrap svg")).toBeVisible();
    await page.getByRole("button", { name: "Add query", exact: true }).click();
    await expect(page.getByLabel("Select query", { exact: true })).toHaveValue(
      "B",
    );
    await page.getByLabel("Start value", { exact: true }).fill("466");
    await page.getByLabel("Spread", { exact: true }).fill("0");
    await page.getByRole("button", { name: "Run query", exact: true }).click();
    await expect(result).toContainText("2 frames");
    await result
      .getByRole("button", { name: "Table view", exact: true })
      .click();
    await expect(result).toContainText("466");
    await page
      .getByRole("button", { name: "Remove selected query", exact: true })
      .click();
    await expect(page.getByLabel("Select query", { exact: true })).toHaveValue(
      "A",
    );
    await page.getByLabel("Datasource", { exact: true }).selectOption("stored");
    await expect(
      page.getByRole("combobox", { name: "Metric", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Metric browser", exact: false }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await expect(page.getByLabel("数据源", { exact: true })).toBeVisible();
    expect(issues).toEqual([]);
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try {
      await cleanupSources(endpoint, ids);
    } catch (error) {
      if (!failed) throw error;
    }
  }
});

test("Desktop SQL Explore queries real MySQL and PostgreSQL inside the single integration container", async ({
  page,
  endpoint,
}) => {
  test.skip(
    !external,
    "Real database browser validation uses METRICSPANEL_SQL_E2E_URL for the existing single integration container",
  );
  test.setTimeout(120000);
  await initialize(page);
  const issues: string[] = [],
    ids: string[] = [],
    dashboards: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  let failed = false;
  try {
    for (const item of [
      {
        type: "mysql",
        name: "Browser live MySQL",
        url: "127.0.0.1:3306",
        user: "exporter",
        database: "business",
        password: "integration-exporter-password",
        value: "233",
      },
      {
        type: "grafana-postgresql-datasource",
        name: "Browser live PostgreSQL",
        url: "127.0.0.1:5432",
        user: "metrics",
        database: "postgres",
        password: "integration-postgres-password",
        value: "466",
      },
    ]) {
      await test.step(item.name, async () => {
        const source = await createSource(
          page,
          endpoint,
          item.name,
          item.type,
          item.url,
          item.user,
          item.database,
          item.password,
        );
        ids.push(source.uid);
        await page
          .getByRole("button", {
            name: "Test connection " + item.name,
            exact: true,
          })
          .click();
        await expect(page.getByRole("status")).toContainText(
          "Data source is working",
        );
        await page.goto(endpoint + "/#explore");
        await page
          .getByLabel("Datasource", { exact: true })
          .selectOption(source.uid);
        const query = page.getByLabel("SQL query", { exact: true });
        await query.fill(
          `SELECT ${item.value} AS value, 'orders' AS category, NULL AS optional`,
        );
        await page
          .getByRole("button", { name: "Run query", exact: true })
          .click();
        const result = page
          .getByRole("region", {
            name: "Query result",
            exact: true,
          })
          .first();
        await expect(result).toContainText(item.value);
        await expect(result).toContainText("orders");
        await expect(result).toContainText("optional");
        await query.fill(
          item.type === "mysql"
            ? "SELECT $__timeFrom() AS time, 233 AS value UNION ALL SELECT $__timeTo() AS time, 466 AS value ORDER BY time"
            : "SELECT $__timeFrom()::timestamptz AS time, 233::double precision AS value UNION ALL SELECT $__timeTo()::timestamptz AS time, 466::double precision AS value ORDER BY time",
        );
        await page
          .getByLabel("Result format", { exact: true })
          .selectOption("time_series");
        await query.press("Control+Enter");
        await expect(result).toContainText("233");
        await expect(result).toContainText("466");
        await result
          .getByRole("button", { name: "Time series view", exact: true })
          .click();
        await expect(result.locator(".chart-wrap svg")).toBeVisible();
        if (process.env.METRICSPANEL_SQL_QA_DIR) {
          const directory = process.env.METRICSPANEL_SQL_QA_DIR;
          await mkdir(directory, { recursive: true });
          await page.screenshot({
            path: path.join(
              directory,
              (item.type === "mysql" ? "mysql" : "postgres") +
                "-explore-desktop.png",
            ),
            fullPage: true,
            animations: "disabled",
          });
        }
        const uid = "browser-sql-variable-" + item.type;
        const imported = await page.request.post(
          endpoint + "/api/v1/import/grafana",
          {
            data: {
              uid,
              title: "Browser SQL variable " + item.type,
              templating: {
                list: [
                  {
                    name: "region",
                    type: "query",
                    multi: true,
                    refresh: 1,
                    datasource: { uid: source.uid, type: item.type },
                    query:
                      "SELECT 'East cluster' AS __text, 'east' AS __value UNION ALL SELECT 'West cluster', 'west'",
                    current: { value: ["east"], text: ["east"] },
                    options: [],
                  },
                ],
              },
              panels: [
                {
                  id: 1,
                  type: "table",
                  title: "SQL region result",
                  gridPos: { x: 0, y: 0, w: 24, h: 8 },
                  datasource: { uid: source.uid, type: item.type },
                  targets: [
                    {
                      refId: "A",
                      datasource: { uid: source.uid, type: item.type },
                      format: "table",
                      rawSql:
                        "SELECT 233 AS value, 'east' AS region WHERE 'east' IN($region) UNION ALL SELECT 466 AS value, 'west' AS region WHERE 'west' IN($region)",
                    },
                  ],
                },
              ],
            },
          },
        );
        expect(imported.ok(), await imported.text()).toBeTruthy();
        dashboards.push((await imported.json()).dashboard.id);
        await page.goto(endpoint + "/d/" + uid + "/sql-query-variables");
        const variable = page.getByLabel("region", { exact: true });
        await expect(variable.getByRole("option")).toHaveCount(2);
        await expect(
          variable.getByRole("option", { name: "East cluster", exact: true }),
        ).toHaveAttribute("value", "east");
        await expect(
          variable.getByRole("option", { name: "West cluster", exact: true }),
        ).toHaveAttribute("value", "west");
        const panel = page.getByRole("region", {
          name: "SQL region result",
          exact: true,
        });
        await expect(panel).toContainText("233");
        await expect(panel).not.toContainText("466");
        await variable.selectOption(["east", "west"]);
        await expect(panel).toContainText("466");
        await variable.selectOption("west");
        await expect(panel).toContainText("466");
        await expect(panel).not.toContainText("233");

        const native = await page.request.post(
          endpoint + "/api/v1/dashboards",
          {
            data: {
              name: "Native SQL " + item.name,
              panels: [
                {
                  id: "sql-result",
                  title: "Native SQL result",
                  metric: "",
                  aggregation: "last",
                  unit: "",
                  visualization: "table",
                  config: {
                    id: 1,
                    type: "table",
                    gridPos: { x: 0, y: 0, w: 24, h: 8 },
                    datasource: { uid: source.uid, type: item.type },
                    targets: [
                      {
                        refId: "A",
                        datasource: { uid: source.uid, type: item.type },
                        format: "table",
                        rawSql: "SELECT 233 AS value, 'native SQL' AS category",
                      },
                    ],
                  },
                },
              ],
            },
          },
        );
        expect(native.ok(), await native.text()).toBeTruthy();
        const nativeDashboard = await native.json();
        dashboards.push(nativeDashboard.id);
        expect(nativeDashboard.grafana).toBeFalsy();
        await page.goto(endpoint + "/d/" + nativeDashboard.id + "/native-sql");
        const nativePanel = page.getByRole("region", {
          name: "Native SQL result",
          exact: true,
        });
        await expect(nativePanel).toContainText("233");
        await expect(nativePanel).toContainText("native SQL");
        await page
          .getByRole("button", { name: "Panel queries", exact: true })
          .click();
        const editor = page.getByRole("dialog", {
          name: "Panel queries",
          exact: true,
        });
        await editor
          .getByLabel("SQL query", { exact: true })
          .fill("SELECT 699 AS value, 'edited native SQL' AS category");
        await editor
          .getByRole("button", { name: "Save queries", exact: true })
          .click();
        await expect(editor).not.toBeVisible();
        await expect(nativePanel).toContainText("699");
        await expect(nativePanel).toContainText("edited native SQL");
        await page.reload();
        await expect(nativePanel).toContainText("699");
        await expect(nativePanel).toContainText("edited native SQL");
        const persisted = (
          await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
        ).find((dashboard: any) => dashboard.id === nativeDashboard.id);
        expect(persisted).toBeTruthy();
        expect(persisted.grafana).toBeFalsy();
        expect(persisted.panels[0].config.targets[0].rawSql).toContain(
          "SELECT 699",
        );
      });
    }
    expect(issues).toEqual([]);
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try {
      await cleanupDashboards(endpoint, dashboards);
      await cleanupSources(endpoint, ids);
    } catch (error) {
      if (!failed) throw error;
    }
  }
});
