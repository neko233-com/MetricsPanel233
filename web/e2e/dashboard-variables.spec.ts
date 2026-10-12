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
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-variables-e2e-"),
      );
      const server = spawn(
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
        logs = "";
      server.stderr.on("data", (chunk) => {
        logs += chunk.toString();
        for (const line of logs.split("\n")) {
          try {
            const record = JSON.parse(line);
            if (record.address) endpoint = `http://${record.address}`;
          } catch {}
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
          !path.basename(resolved).startsWith("metricspanel233-variables-e2e-")
        )
          throw new Error("Unsafe variable fixture cleanup");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});
function issues(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (
      ["error", "warning"].includes(entry.type()) &&
      !entry.text().includes("409 (Conflict)")
    )
      errors.push(entry.text());
  });
  return errors;
}
async function init(page: Page) {
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  page.setDefaultTimeout(15000);
}
const variable = (
  name: string,
  type: string,
  query: string,
  current = "",
  config: Record<string, unknown> = {},
) => ({
  name,
  type,
  query,
  current,
  options: [],
  multi: false,
  include_all: false,
  config: {
    name,
    type,
    query,
    current: { value: current, text: current },
    ...config,
  },
});
async function list(page: Page, endpoint: string, id: string) {
  return (
    await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
  ).find((dashboard: any) => dashboard.id === id);
}
async function editor(page: Page) {
  await page.getByRole("button", { name: "Variables", exact: true }).click();
  return page.getByRole("dialog", { name: "Dashboard variables", exact: true });
}

test("Native dashboard variable editing, defaults, URL restoration and snapshot use effective values", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(120000);
  await init(page);
  const errors = issues(page);
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      name: "Native variables",
      variables: [
        variable("count", "custom", "Current : 233, Alternate : 466", "233", {
          label: "Count",
        }),
        {
          ...variable("region", "custom", "East : east, West : west", "east", {
            label: "Region",
            current: { value: ["east"], text: ["East"] },
            multi: true,
            includeAll: true,
          }),
          multi: true,
          include_all: true,
        },
      ],
      panels: [
        {
          id: "count",
          title: "Variable count",
          metric: "",
          expr: "vector($count)",
          aggregation: "last",
          unit: "",
          visualization: "stat",
        },
      ],
    },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const saved = await created.json();
  await page.goto(
    endpoint +
      "/d/" +
      saved.id +
      "/native-variables?var-count=466&var-region=east&var-region=west&from=now-1h",
  );
  const panel = page.getByRole("region", {
    name: "Variable count",
    exact: true,
  });
  await expect(panel).toContainText("466");
  await expect(page.getByLabel("region", { exact: true })).toHaveValues([
    "east",
    "west",
  ]);
  await expect(
    page
      .getByLabel("region", { exact: true })
      .getByRole("option", { name: "East", exact: true }),
  ).toHaveAttribute("value", "east");
  await page.getByLabel("count", { exact: true }).selectOption("233");
  await expect(panel).toContainText("233");
  await page.getByLabel("region", { exact: true }).selectOption("$__all");
  await expect(page).toHaveURL(/var-region=%24__all/);
  await page.reload();
  await expect(page.getByLabel("region", { exact: true })).toHaveValues([
    "$__all",
  ]);
  await expect(panel).toContainText("233");
  let dialog = await editor(page);
  await dialog
    .getByLabel("Variable label", { exact: true })
    .fill("Uncommitted");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect((await list(page, endpoint, saved.id)).variables[0].config.label).toBe(
    "Count",
  );
  dialog = await editor(page);
  await dialog
    .getByLabel("Variable query", { exact: true })
    .fill("Revised : 699, Alternate : 466");
  await dialog
    .getByLabel("Variable default selection", { exact: true })
    .fill("699");
  await dialog.getByLabel("Variable label", { exact: true }).fill("Orders");
  await dialog
    .getByRole("button", { name: "Preview values", exact: true })
    .click();
  await expect(dialog.locator(".variable-preview")).toContainText("Revised");
  await expect(dialog.locator(".variable-preview")).toContainText("699");
  for (const item of [
    { name: "text", type: "textbox", query: "production" },
    { name: "fixed", type: "constant", query: "233" },
    { name: "source", type: "datasource", query: "prometheus" },
    { name: "window", type: "interval", query: "1m,5m" },
    { name: "metric", type: "query", query: "metrics(metricspanel_.*)" },
  ]) {
    await dialog
      .getByRole("button", { name: "Add variable", exact: true })
      .click();
    await dialog.getByLabel("Variable name", { exact: true }).fill(item.name);
    await dialog
      .getByLabel("Variable type", { exact: true })
      .selectOption(item.type);
    await dialog.getByLabel("Variable query", { exact: true }).fill(item.query);
    await dialog
      .getByRole("button", { name: "Preview values", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "Preview values", exact: true }),
    ).toBeEnabled();
    await expect(dialog.locator(".variable-preview")).toContainText(
      "Selected value",
    );
  }
  await dialog
    .getByRole("button", { name: "Move variable up metric", exact: true })
    .click();
  await dialog
    .getByRole("button", { name: "Save variables", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(panel).toContainText("699");
  await expect(
    page
      .getByLabel("count", { exact: true })
      .getByRole("option", { name: "Revised", exact: true }),
  ).toHaveAttribute("value", "699");
  await expect(page.getByLabel("text", { exact: true })).toHaveValue(
    "production",
  );
  await expect(page.getByLabel("fixed", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("source", { exact: true })).toHaveValue(
    "metricspanel",
  );
  await expect(page.getByLabel("window", { exact: true })).toHaveValue("1m");
  const current = await list(page, endpoint, saved.id);
  expect(current.variables.map((v: any) => v.name)).toEqual([
    "count",
    "region",
    "text",
    "fixed",
    "source",
    "metric",
    "window",
  ]);
  await page
    .getByRole("button", { name: "Panel queries", exact: true })
    .click();
  const queries = page.getByRole("dialog", {
    name: "Panel queries",
    exact: true,
  });
  await queries.getByRole("button", { name: "Run query", exact: true }).click();
  await expect(queries.locator(".annotation-query-preview")).toContainText(
    "699",
  );
  await queries
    .getByRole("button", { name: "Close dialog", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Create snapshot", exact: true })
    .click();
  const snapshot = page.getByRole("dialog", {
    name: "Create snapshot",
    exact: true,
  });
  await snapshot
    .getByRole("button", { name: "Save snapshot", exact: true })
    .click();
  await expect(
    snapshot.getByRole("link", { name: "Open snapshot", exact: true }),
  ).toBeVisible();
  await snapshot
    .getByRole("link", { name: "Open snapshot", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "Variable count", exact: true }),
  ).toContainText("699");
  await page.goto(endpoint + "/d/" + saved.id + "/native-variables");
  dialog = await editor(page);
  await dialog
    .getByRole("button", { name: "metric · Query", exact: true })
    .click();
  await dialog
    .getByRole("button", { name: "Delete variable metric", exact: true })
    .click();
  await dialog
    .getByRole("button", { name: "Save variables", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    (await list(page, endpoint, saved.id)).variables.map((v: any) => v.name),
  ).not.toContain("metric");
  expect(errors).toEqual([]);
});

test("Dependent variables query in order, cancel obsolete options and preserve an editing revision", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  await init(page);
  const errors = issues(page);
  const ingested = await page.request.post(endpoint + "/api/v1/ingest", {
    data: {
      samples: [
        {
          name: "variable_fixture",
          value: 233,
          timestamp: Date.now(),
          labels: { region: "east", host: "east-server" },
        },
        {
          name: "variable_fixture",
          value: 466,
          timestamp: Date.now(),
          labels: { region: "west", host: "west-server" },
        },
      ],
    },
  });
  expect(ingested.ok()).toBeTruthy();
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      name: "Dependent variables",
      variables: [
        variable(
          "host",
          "query",
          'label_values(variable_fixture{region="$region"},host)',
        ),
        variable("region", "custom", "East : east, West : west", "east"),
      ],
      panels: [
        {
          id: "load",
          title: "Dependent load",
          metric: "",
          expr: 'sum(variable_fixture{host="$host"})',
          aggregation: "last",
          unit: "",
          visualization: "stat",
        },
      ],
    },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const saved = await created.json();
  await page.goto(endpoint + "/d/" + saved.id + "/dependencies");
  const panel = page.getByRole("region", {
    name: "Dependent load",
    exact: true,
  });
  await expect(page.getByLabel("host", { exact: true })).toHaveValue(
    "east-server",
  );
  await expect(panel).toContainText("233");
  let held = false,
    release: () => void = () => {};
  const blocked = new Promise<void>((resolve) => (release = resolve));
  await page.route(
    "**/prometheus/api/v1/label/host/values?*",
    async (route) => {
      if (decodeURIComponent(route.request().url()).includes('region="west"')) {
        held = true;
        await blocked;
      }
      try {
        await route.continue();
      } catch {}
    },
  );
  await page.getByLabel("region", { exact: true }).selectOption("west");
  await expect.poll(() => held).toBe(true);
  await page.getByLabel("region", { exact: true }).selectOption("east");
  release();
  await expect(page.getByLabel("host", { exact: true })).toHaveValue(
    "east-server",
  );
  await expect(panel).toContainText("233");
  await page.unroute("**/prometheus/api/v1/label/host/values?*");
  await page.getByLabel("region", { exact: true }).selectOption("west");
  await expect(page.getByLabel("host", { exact: true })).toHaveValue(
    "west-server",
  );
  await expect(panel).toContainText("466");
  const dialog = await editor(page);
  await dialog
    .getByLabel("Variable label", { exact: true })
    .fill("Host edited");
  const concurrent = await list(page, endpoint, saved.id);
  const updated = await page.request.put(
    endpoint + "/api/v1/dashboards/" + saved.id,
    {
      data: { ...concurrent, name: "Changed elsewhere" },
      headers: { "If-Match": String(concurrent.updated_at) },
    },
  );
  expect(updated.ok()).toBeTruthy();
  await dialog
    .getByRole("button", { name: "Save variables", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText(/changed|reload/i);
  await expect(
    dialog.getByLabel("Variable label", { exact: true }),
  ).toHaveValue("Host edited");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await page
    .getByRole("button", { name: "Switch language", exact: true })
    .click();
  await page.getByRole("button", { name: "变量", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "仪表盘变量", exact: true }),
  ).toBeVisible();
  const qa = process.env.METRICSPANEL_VARIABLES_QA_DIR;
  if (qa) {
    await mkdir(qa, { recursive: true });
    await page.screenshot({
      path: path.join(qa, "variables-editor-desktop.png"),
      fullPage: false,
      animations: "disabled",
    });
  }
  expect(errors).toEqual([]);
});

test("Classic, V1 and V2 variable edits keep unknown fields and exact source numbers", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(120000);
  await init(page);
  const errors = issues(page);
  for (const format of ["classic", "v1", "v2"]) {
    const uid = "variable-preservation-" + format;
    const classic = `{"uid":"${uid}","title":"Variables ${format}","opaque":9007199254740993,"templating":{"future":9007199254740993,"list":[{"name":"number","type":"custom","query":"233,466","current":{"value":"233","text":"233","opaque":9007199254740993},"options":[{"value":"233","text":"233","opaque":9007199254740993}],"opaque":9007199254740993}]},"panels":[{"id":1,"title":"Imported variable","type":"stat","gridPos":{"x":0,"y":0,"w":12,"h":8},"targets":[{"refId":"A","expr":"vector($number)","instant":true}],"opaque":9007199254740993}]}`;
    const raw =
      format === "classic"
        ? classic
        : format === "v1"
          ? `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"${uid}","opaque":9007199254740993},"spec":${classic}}`
          : `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"${uid}","opaque":9007199254740993},"spec":{"title":"Variables v2","opaque":9007199254740993,"variables":[{"kind":"CustomVariable","opaque":9007199254740993,"spec":{"name":"number","query":"233,466","current":{"value":"233","text":"233","opaque":9007199254740993},"options":[{"value":"233","text":"233","opaque":9007199254740993}],"hide":"dontHide","opaque":9007199254740993}}],"elements":{"orders":{"kind":"Panel","spec":{"id":1,"title":"Imported variable","data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","spec":{"expr":"vector($number)","instant":true}}}}]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}},"opaque":9007199254740993}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"kind":"ElementReference","name":"orders"}}}]}}}}`;
    const imported = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      { data: raw, headers: { "Content-Type": "application/json" } },
    );
    expect(imported.ok(), await imported.text()).toBeTruthy();
    const saved = (await imported.json()).dashboard;
    await page.goto(endpoint + "/d/" + uid + "/variables");
    await expect(
      page.getByRole("region", { name: "Imported variable", exact: true }),
    ).toContainText("233");
    const dialog = await editor(page);
    await dialog
      .getByLabel("Variable label", { exact: true })
      .fill("Imported Number");
    await dialog
      .getByRole("button", { name: "Save variables", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const exported = await page.request.get(
      endpoint + "/api/v1/dashboard-exports/" + saved.id + "?format=grafana",
    );
    expect(exported.ok()).toBeTruthy();
    const next = await exported.text();
    expect(next).toContain("Imported Number");
    expect(next).toContain("9007199254740993");
    expect(next).not.toContain("9007199254740992");
    await page.reload();
    await expect(
      page.getByRole("region", { name: "Imported variable", exact: true }),
    ).toContainText("233");
  }
  expect(errors).toEqual([]);
});
