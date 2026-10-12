import { test as base, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const qa = process.env.METRICSPANEL_LAYOUT_QA_DIR;
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-dashboard-layout-e2e-"),
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
        let end: number;
        while ((end = pending.indexOf("\n")) >= 0) {
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
            .startsWith("metricspanel233-dashboard-layout-e2e-")
        )
          throw new Error("Unsafe dashboard layout fixture cleanup");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

async function visit(page: Page, endpoint: string, uid: string, title: string) {
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(
    `${endpoint}/d/${encodeURIComponent(uid)}/${encodeURIComponent(title)}`,
  );
  await expect(page).toHaveTitle("MetricsPanel233");
  await expect(
    page.getByRole("heading", { name: title, exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Edit layout", exact: true }).click();
  return page.getByRole("dialog", { name: "Edit layout", exact: true });
}
async function raw(
  page: Page,
  endpoint: string,
  id: string,
  format = "native",
) {
  const response = await page.request.get(
    `${endpoint}/api/v1/dashboard-exports/${encodeURIComponent(id)}?format=${format}`,
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.text();
}
async function screenshot(page: Page, name: string) {
  if (qa) {
    await mkdir(qa, { recursive: true });
    await page.screenshot({
      path: path.join(qa, name),
      fullPage: false,
      animations: "disabled",
    });
  }
}

test("Native dashboard layout supports mouse, keyboard, numeric edits, cancellation and durable save", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const source = JSON.stringify({
    name: "Layout native",
    panels: ["Alpha", "Beta", "Gamma", "Delta"].map((title, index) => ({
      id: `native-${index}`,
      title,
      metric: "metricspanel_uptime_seconds",
      expr: "vector(233)",
      aggregation: "last",
      unit: "",
      visualization: "stat",
      config: {
        gridPos: {
          x: (index % 2) * 12,
          y: Math.floor(index / 2) * 4,
          w: 12,
          h: 4,
        },
        opaque: "INTEGER_LITERAL",
      },
    })),
  }).replaceAll('"INTEGER_LITERAL"', "9007199254740993");
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: source,
    headers: { "Content-Type": "application/json" },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const dashboard = await created.json();
  try {
    const before = await raw(page, endpoint, dashboard.id);
    let dialog = await visit(page, endpoint, dashboard.id, "Layout native");
    await expect(page.locator(".dashboard-panel-cell")).toHaveCount(4);
    const move = dialog.getByRole("button", {
      name: "Move panel: Alpha",
      exact: true,
    });
    await move.focus();
    await move.press("ArrowRight");
    await expect(
      dialog.getByLabel("Column (0–23)", { exact: true }),
    ).toHaveValue("1");
    await move.press("Shift+ArrowDown");
    await expect(
      dialog.getByLabel("Height (rows)", { exact: true }),
    ).toHaveValue("5");
    const box = await move.boundingBox();
    expect(box).toBeTruthy();
    await page.mouse.move(box!.x + 40, box!.y + 15);
    await page.mouse.down();
    await page.mouse.move(box!.x + 40, box!.y + 43);
    await page.mouse.up();
    await expect(
      dialog.getByLabel("Row (0–10000)", { exact: true }),
    ).toHaveValue("1");
    const resize = await dialog
      .getByRole("button", { name: "Resize panel: Alpha", exact: true })
      .boundingBox();
    const canvas = await dialog.locator(".layout-canvas").boundingBox();
    await page.mouse.move(resize!.x + 10, resize!.y + 10);
    await page.mouse.down();
    await page.mouse.move(resize!.x + 10 + canvas!.width / 24, resize!.y + 38);
    await page.mouse.up();
    await expect(
      dialog.getByLabel("Width (columns)", { exact: true }),
    ).toHaveValue("13");
    await expect(
      dialog.getByLabel("Height (rows)", { exact: true }),
    ).toHaveValue("6");
    await screenshot(page, "layout-native-mouse-keyboard.png");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await raw(page, endpoint, dashboard.id)).toBe(before);
    await page
      .getByRole("button", { name: "Edit layout", exact: true })
      .click();
    dialog = page.getByRole("dialog", { name: "Edit layout", exact: true });
    await dialog.getByLabel("Width (columns)", { exact: true }).fill("8");
    await dialog.getByLabel("Column (0–23)", { exact: true }).fill("3");
    await dialog.getByLabel("Height (rows)", { exact: true }).fill("6");
    await dialog.getByLabel("Row (0–10000)", { exact: true }).fill("8");
    await dialog
      .getByRole("button", { name: "Save layout", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await page.reload();
    await expect(
      page.locator('[data-panel-id="native-0"].dashboard-panel-cell'),
    ).toHaveCSS("grid-column-start", "4");
    await expect(
      page.locator('[data-panel-id="native-0"].dashboard-panel-cell'),
    ).toHaveCSS("grid-row-start", "9");
    const after = await raw(page, endpoint, dashboard.id);
    expect(after).toMatch(/"opaque"\s*:\s*9007199254740993/);
    expect(JSON.parse(after).panels[0].config.gridPos).toEqual({
      x: 3,
      y: 8,
      w: 8,
      h: 6,
    });
    await expect
      .poll(() =>
        page.locator(".dashboard-panel-cell").evaluateAll((cells) =>
          Math.max(
            ...cells.map((cell) => {
              const slot = cell.getBoundingClientRect(),
                panel = cell
                  .querySelector(".chart-panel")!
                  .getBoundingClientRect();
              return Math.max(
                Math.abs(slot.top - panel.top),
                Math.abs(slot.bottom - panel.bottom),
              );
            }),
          ),
        ),
      )
      .toBeLessThan(1);
    expect(
      await page
        .locator(".dashboard-panel-cell .stat-value")
        .evaluateAll((values) =>
          values.every(
            (value) =>
              parseFloat(getComputedStyle(value).fontSize) <=
                value.clientHeight && value.clientHeight > 0,
          ),
        ),
    ).toBe(true);
    await screenshot(page, "layout-native-saved.png");
    expect(errors).toEqual([]);
  } finally {
    await page.request.delete(endpoint + "/api/v1/dashboards/" + dashboard.id);
  }
});

for (const kind of ["Classic", "V1", "V2"])
  test(`${kind} imported dashboard layout saves base panel positions and preserves raw Grafana fields`, async ({
    page,
    endpoint,
  }) => {
    const uid = `layout-${kind.toLowerCase()}`,
      title = `Layout ${kind}`;
    const classic = {
      uid,
      title,
      opaque: "INTEGER_LITERAL",
      templating: {
        list: [
          {
            name: "host",
            type: "custom",
            query: "alpha,beta",
            multi: true,
            current: { value: ["alpha", "beta"], text: ["alpha", "beta"] },
            options: [
              { text: "alpha", value: "alpha" },
              { text: "beta", value: "beta" },
            ],
          },
        ],
      },
      panels: [
        {
          id: 7,
          title: "Repeated text",
          type: "text",
          gridPos: { x: 0, y: 0, w: 12, h: 4 },
          repeat: "host",
          options: { mode: "markdown", content: "Layout fixture" },
        },
      ],
    };
    const v2 = {
      apiVersion: "dashboard.grafana.app/v2beta1",
      kind: "Dashboard",
      metadata: { name: uid, opaque: "INTEGER_LITERAL" },
      spec: {
        title,
        opaque: "INTEGER_LITERAL",
        elements: {
          text: {
            kind: "Panel",
            spec: {
              id: 7,
              title: "V2 text",
              opaque: "INTEGER_LITERAL",
              data: {
                kind: "QueryGroup",
                spec: { queries: [], transformations: [] },
              },
              vizConfig: {
                kind: "VizConfig",
                group: "text",
                spec: {
                  options: { mode: "markdown", content: "Layout fixture" },
                  fieldConfig: { defaults: {}, overrides: [] },
                },
              },
            },
          },
        },
        layout: {
          kind: "AutoGridLayout",
          spec: {
            opaque: "INTEGER_LITERAL",
            maxColumnCount: 2,
            items: [
              {
                kind: "AutoGridLayoutItem",
                spec: {
                  opaque: "INTEGER_LITERAL",
                  element: { kind: "ElementReference", name: "text" },
                },
              },
            ],
          },
        },
      },
    };
    const v1 = {
      apiVersion: "dashboard.grafana.app/v1beta1",
      kind: "Dashboard",
      metadata: { name: uid, opaque: "INTEGER_LITERAL" },
      spec: classic,
    };
    const imported = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      {
        data: JSON.stringify(
          kind === "Classic" ? classic : kind === "V1" ? v1 : v2,
        ).replaceAll('"INTEGER_LITERAL"', "9007199254740993"),
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(imported.ok(), await imported.text()).toBeTruthy();
    const dashboard = (await imported.json()).dashboard;
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    try {
      const dialog = await visit(page, endpoint, uid, title);
      await expect(dialog.locator(".layout-tile")).toHaveCount(1);
      expect(
        await dialog.locator(".layout-tile").getAttribute("data-panel-id"),
      ).toBe(dashboard.panels[0].id);
      await dialog.getByLabel("Width (columns)", { exact: true }).fill("8");
      await dialog.getByLabel("Column (0–23)", { exact: true }).fill("2");
      await dialog.getByLabel("Row (0–10000)", { exact: true }).fill("3");
      await dialog
        .getByRole("button", { name: "Save layout", exact: true })
        .click();
      await expect(dialog).not.toBeVisible();
      await page.reload();
      const saved = JSON.parse(await raw(page, endpoint, dashboard.id));
      expect(saved.panels[0].config.gridPos).toMatchObject({
        x: 2,
        y: 3,
        w: 8,
      });
      const exported = await raw(page, endpoint, dashboard.id, "grafana");
      expect(exported).toMatch(/"opaque"\s*:\s*9007199254740993/);
      expect(exported).not.toMatch(/"opaque"\s*:\s*9007199254740992/);
      if (kind !== "V2") {
        const original =
          kind === "V1" ? JSON.parse(exported).spec : JSON.parse(exported);
        expect(original.panels[0].repeat).toBe("host");
        expect(original.templating.list).toHaveLength(1);
      } else expect(JSON.parse(exported).spec.layout.kind).toBe("GridLayout");
      await expect(
        page.getByText("Layout fixture", { exact: true }).first(),
      ).toBeVisible();
      await screenshot(page, `layout-${kind.toLowerCase()}-saved.png`);
      expect(errors).toEqual([]);
    } finally {
      await page.request.delete(
        endpoint + "/api/v1/dashboards/" + dashboard.id,
      );
    }
  });

test("A layout revision conflict keeps the draft and offers a reload without overwriting", async ({
  page,
  endpoint,
}) => {
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: {
      name: "Layout conflict",
      panels: [
        {
          id: "conflict-panel",
          title: "Conflict panel",
          metric: "up",
          aggregation: "last",
          unit: "",
          config: { gridPos: { x: 0, y: 0, w: 12, h: 4 } },
        },
      ],
    },
  });
  expect(created.ok()).toBeTruthy();
  const dashboard = await created.json();
  try {
    const dialog = await visit(page, endpoint, dashboard.id, "Layout conflict");
    await dialog.getByLabel("Column (0–23)", { exact: true }).fill("2");
    const concurrent = await page.request.patch(
      endpoint + `/api/v1/dashboards/${dashboard.id}/layout`,
      {
        headers: { "If-Match": String(dashboard.updated_at) },
        data: { panels: [{ id: "conflict-panel", x: 0, y: 9, w: 12, h: 4 }] },
      },
    );
    expect(concurrent.ok(), await concurrent.text()).toBeTruthy();
    await dialog
      .getByRole("button", { name: "Save layout", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toHaveText(
      "Dashboard changed. Reload it before editing the layout again.",
    );
    await expect(
      dialog.getByLabel("Column (0–23)", { exact: true }),
    ).toHaveValue("2");
    expect(
      JSON.parse(await raw(page, endpoint, dashboard.id)).panels[0].config
        .gridPos,
    ).toEqual({ x: 0, y: 9, w: 12, h: 4 });
    await screenshot(page, "layout-conflict.png");
    await dialog
      .getByRole("button", { name: "Reload dashboard", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(
      page.locator('[data-panel-id="conflict-panel"].dashboard-panel-cell'),
    ).toHaveCSS("grid-row-start", "10");
  } finally {
    await page.request.delete(endpoint + "/api/v1/dashboards/" + dashboard.id);
  }
});
