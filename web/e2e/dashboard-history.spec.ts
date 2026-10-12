import { test as base, expect, type Page } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const qa =
  process.env.METRICSPANEL_HISTORY_QA_DIR ||
  process.env.METRICSPANEL_CATALOG_QA_DIR;
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-dashboard-history-e2e-"),
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
        pending = "",
        logs = "";
      service.stderr!.on("data", (chunk) => {
        logs += chunk.toString();
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
        if (qa) {
          await mkdir(qa, { recursive: true });
          await writeFile(
            path.join(qa, `${path.basename(temp)}-server.log`),
            logs,
          );
        }
        const resolved = path.resolve(temp);
        if (
          !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path
            .basename(resolved)
            .startsWith("metricspanel233-dashboard-history-e2e-")
        )
          throw new Error("Unsafe dashboard history fixture cleanup");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

function issues(page: Page, expected: number[] = []) {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (
      ["error", "warning"].includes(entry.type()) &&
      !expected.some((status) => entry.text().includes(`status of ${status}`))
    )
      errors.push(entry.text());
  });
  return errors;
}
async function capture(page: Page, name: string) {
  if (!qa) return;
  await mkdir(qa, { recursive: true });
  await page.screenshot({
    path: path.join(qa, name),
    fullPage: true,
    animations: "disabled",
  });
}
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
  await page
    .getByRole("button", { name: "Version history", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Version history",
    exact: true,
  });
  await expect(dialog.getByRole("table")).toBeVisible();
  return dialog;
}
async function rawNative(page: Page, endpoint: string, id: string) {
  const response = await page.request.get(
    `${endpoint}/api/v1/dashboard-exports/${encodeURIComponent(id)}?format=native`,
  );
  expect(response.ok()).toBeTruthy();
  return response.text();
}
async function saveRaw(page: Page, endpoint: string, id: string, raw: string) {
  const revision = JSON.parse(await rawNative(page, endpoint, id)).updated_at;
  const response = await page.request.put(
    `${endpoint}/api/v1/dashboards/${encodeURIComponent(id)}`,
    {
      data: raw,
      headers: {
        "Content-Type": "application/json",
        "If-Match": String(revision),
      },
    },
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.json();
}
async function versions(page: Page, endpoint: string, id: string, limit = 20) {
  const response = await page.request.get(
    `${endpoint}/api/v1/dashboard-versions/${encodeURIComponent(id)}?limit=${limit}&start=0`,
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.json();
}
function classic(uid: string, title: string, content: string) {
  return {
    uid,
    title,
    version: "VERSION_LITERAL",
    opaque: "INTEGER_LITERAL",
    digits: "9007199254740993",
    panels: [
      {
        id: 7,
        type: "text",
        title: "Saved text",
        gridPos: { x: 0, y: 0, w: 24, h: 8 },
        options: { mode: "markdown", content },
        targets: [],
      },
    ],
  };
}
function source(kind: string, uid: string, title: string) {
  const content = `first-visible-${kind}`;
  let value: unknown = classic(uid, title, content);
  if (kind === "V1")
    value = {
      uid: "outer-must-not-be-used",
      metadata: { name: "outer-resource-must-not-be-used" },
      dashboard: {
        apiVersion: "dashboard.grafana.app/v1beta1",
        kind: "Dashboard",
        metadata: { name: uid },
        spec: classic(uid, title, content),
      },
    };
  if (kind === "V2")
    value = {
      uid: "outer-must-not-be-used",
      metadata: { name: "outer-resource-must-not-be-used" },
      dashboard: {
        apiVersion: "dashboard.grafana.app/v2beta1",
        kind: "Dashboard",
        metadata: { name: uid, generation: "VERSION_LITERAL" },
        spec: {
          title,
          opaque: "INTEGER_LITERAL",
          digits: "9007199254740993",
          elements: {
            text: {
              kind: "Panel",
              spec: {
                id: 7,
                title: "Saved text",
                data: {
                  kind: "QueryGroup",
                  spec: { queries: [], transformations: [] },
                },
                vizConfig: {
                  kind: "VizConfig",
                  group: "text",
                  spec: {
                    options: { mode: "markdown", content },
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
                    element: { kind: "ElementReference", name: "text" },
                  },
                },
              ],
            },
          },
        },
      },
    };
  return JSON.stringify(value)
    .replaceAll('"VERSION_LITERAL"', "9007199254740993")
    .replaceAll('"INTEGER_LITERAL"', "9007199254740993");
}
const assertExact = (raw: string) => {
  expect(raw).toMatch(/"opaque"\s*:\s*9007199254740993\s*[,}]/);
  expect(raw).not.toMatch(/"opaque"\s*:\s*9007199254740992\s*[,}]/);
};

for (const kind of ["native", "Classic", "V1", "V2"])
  test(`${kind} dashboard history views, compares and restores raw versions while keeping identity and current folder`, async ({
    page,
    endpoint,
  }) => {
    const errors = issues(page);
    const title = `Initial ${kind}`,
      changed = `Changed ${kind}`,
      uid = `history-${kind.toLowerCase()}`;
    let saved: any;
    if (kind === "native") {
      const raw = `{"name":${JSON.stringify(title)},"panels":[{"id":"native-panel","title":"first-visible-native","metric":"metricspanel_uptime_seconds","expr":"vector(23)","aggregation":"last","unit":"","visualization":"timeseries","config":{"opaque":9007199254740993,"digits":"9007199254740993"}}]}`;
      const response = await page.request.post(
        endpoint + "/api/v1/dashboards",
        { data: raw, headers: { "Content-Type": "application/json" } },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      saved = await response.json();
    } else {
      const response = await page.request.post(
        endpoint + "/api/v1/import/grafana",
        {
          data: source(kind, uid, title),
          headers: { "Content-Type": "application/json" },
        },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      saved = (await response.json()).dashboard;
    }
    const first = (await versions(page, endpoint, saved.id)).versions[0]
      .versionKey;
    expect(first).toBe(kind === "native" ? "1" : "9007199254740993");
    const original = await rawNative(page, endpoint, saved.id);
    assertExact(original);
    await saveRaw(
      page,
      endpoint,
      saved.id,
      original
        .replaceAll(title, changed)
        .replaceAll(`first-visible-${kind}`, `second-visible-${kind}`)
        .replaceAll('"opaque":9007199254740993', '"opaque":9007199254740995'),
    );
    const folderResponse = await page.request.post(endpoint + "/api/folders", {
      data: { title: `${kind} current folder` },
    });
    expect(folderResponse.ok()).toBeTruthy();
    const folder = await folderResponse.json();
    let current = JSON.parse(await rawNative(page, endpoint, saved.id));
    const moved = await page.request.put(
      `${endpoint}/api/v1/dashboard-folders/${saved.id}`,
      { data: { folderUid: folder.uid, revision: current.updated_at } },
    );
    expect(moved.ok(), await moved.text()).toBeTruthy();
    const latest = await versions(page, endpoint, saved.id);
    const dialog = await visit(
      page,
      endpoint,
      kind === "native" ? saved.id : uid,
      changed,
    );
    await expect(dialog.locator("tbody")).toContainText(first);
    await expect(
      dialog.getByRole("button", {
        name: `Restore version ${latest.currentVersionKey}`,
        exact: true,
      }),
    ).toBeDisabled();
    await dialog
      .getByRole("button", { name: `View version ${first}`, exact: true })
      .click();
    await expect(
      dialog.getByLabel("Version JSON", { exact: true }),
    ).toContainText("9007199254740993");
    assertExact(
      await dialog.getByLabel("Version JSON", { exact: true }).innerText(),
    );
    await dialog
      .getByRole("combobox", { name: "Export format", exact: true })
      .selectOption("grafana");
    await dialog
      .locator(".history-document-heading")
      .getByRole("button", { name: "View JSON", exact: true })
      .click();
    await expect(
      dialog.getByLabel("Version JSON", { exact: true }),
    ).toContainText("9007199254740993");
    const ready = page.waitForEvent("download");
    await dialog
      .getByRole("button", { name: "Download JSON", exact: true })
      .click();
    const artifact = await ready;
    expect(artifact.suggestedFilename()).toContain(`-v${first}-grafana.json`);
    assertExact(await readFile((await artifact.path())!, "utf8"));
    await dialog.getByLabel("Base version", { exact: true }).fill(first);
    await dialog
      .getByLabel("Compare version", { exact: true })
      .fill(latest.currentVersionKey);
    await dialog.getByRole("button", { name: "Compare", exact: true }).click();
    const diff = dialog.getByLabel("Version changes", { exact: true });
    await expect(diff).toContainText("9007199254740993");
    await expect(diff).toContainText("9007199254740995");
    expect(await diff.innerText()).not.toContain("9007199254740992");
    await capture(
      page,
      `dashboard-history-${kind.toLowerCase()}-en-desktop.png`,
    );
    await dialog
      .getByRole("button", { name: `Restore version ${first}`, exact: true })
      .click();
    await dialog
      .getByLabel("Restore note (optional)", { exact: true })
      .fill(`Restore ${kind} from browser`);
    await dialog
      .getByRole("button", { name: "Confirm restore", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: title, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("dialog", { name: "Version history", exact: true }),
    ).not.toBeVisible();
    expect(new URL(page.url()).pathname).toContain(
      `/d/${kind === "native" ? saved.id : uid}/`,
    );
    const restoredText = await rawNative(page, endpoint, saved.id);
    assertExact(restoredText);
    current = JSON.parse(restoredText);
    expect(current.id).toBe(saved.id);
    expect(current.folder_uid).toBe(folder.uid);
    expect(current.name).toBe(title);
    expect(current.grafana_id).toBe(saved.grafana_id);
    expect(current.panels[0].title).toBe(
      kind === "native" ? "first-visible-native" : "Saved text",
    );
    if (kind !== "native")
      await expect(page.locator(".grafana-grid")).toContainText(
        `first-visible-${kind}`,
      );
    const after = await versions(page, endpoint, saved.id);
    expect(after.versions).toHaveLength(latest.versions.length + 1);
    expect(after.versions[0].restoredFromKey).toBe(first);
    expect(after.versions[0].message).toContain(`Restore ${kind} from browser`);
    expect(errors).toEqual([]);
  });

test("History cursor pages remain stable when another save inserts a version", async ({
  page,
  endpoint,
}) => {
  const errors = issues(page);
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: { name: "Cursor 0", panels: [] },
  });
  expect(created.ok()).toBeTruthy();
  const saved = await created.json();
  for (let index = 1; index <= 22; index++)
    await saveRaw(
      page,
      endpoint,
      saved.id,
      (await rawNative(page, endpoint, saved.id)).replace(
        `Cursor ${index - 1}`,
        `Cursor ${index}`,
      ),
    );
  const dialog = await visit(page, endpoint, saved.id, "Cursor 22");
  const rowKeys = () =>
    dialog.locator("tbody tr td:first-child strong").allTextContents();
  await expect(dialog.locator("tbody tr")).toHaveCount(20);
  expect(await rowKeys()).toEqual(
    Array.from({ length: 20 }, (_, i) => String(23 - i)),
  );
  await saveRaw(
    page,
    endpoint,
    saved.id,
    (await rawNative(page, endpoint, saved.id)).replace(
      "Cursor 22",
      "Concurrent newest",
    ),
  );
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  await expect(dialog.locator("tbody tr")).toHaveCount(3);
  expect(await rowKeys()).toEqual(["3", "2", "1"]);
  await expect(
    dialog.getByRole("button", { name: "Next", exact: true }),
  ).toBeDisabled();
  await dialog.getByRole("button", { name: "Previous", exact: true }).click();
  await expect(dialog.locator("tbody tr")).toHaveCount(20);
  await expect(dialog.locator("tbody tr").first()).toContainText("24");
  expect(errors).toEqual([]);
});

test("Chinese mobile history retries a failed read and refuses a stale restore until the dashboard is reloaded", async ({
  page,
  endpoint,
}) => {
  const errors = issues(page, [503, 409]);
  const response = await page.request.post(
    endpoint + "/api/v1/import/grafana",
    {
      data: source("Classic", "history-conflict", "Conflict initial"),
      headers: { "Content-Type": "application/json" },
    },
  );
  expect(response.ok()).toBeTruthy();
  const saved = (await response.json()).dashboard;
  const first = (await versions(page, endpoint, saved.id)).versions[0]
    .versionKey;
  await saveRaw(
    page,
    endpoint,
    saved.id,
    (await rawNative(page, endpoint, saved.id)).replaceAll(
      "Conflict initial",
      "Conflict changed",
    ),
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "zh"),
  );
  let fail = true;
  await page.route(
    `**/api/v1/dashboard-versions/${saved.id}?*`,
    async (route) => {
      if (fail) {
        fail = false;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: '{"message":"History read unavailable"}',
        });
      } else await route.continue();
    },
  );
  await page.goto(endpoint + "/d/history-conflict/Conflict%20changed");
  await expect(
    page.getByRole("heading", { name: "Conflict changed", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "版本历史", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "版本历史", exact: true });
  await expect(dialog.getByRole("alert")).toContainText(
    "History read unavailable",
  );
  await dialog.getByRole("button", { name: "重试", exact: true }).click();
  await expect(dialog.getByRole("table")).toBeVisible();
  await dialog
    .getByRole("button", { name: `查看版本 ${first}`, exact: true })
    .click();
  await expect(dialog.getByLabel("版本 JSON", { exact: true })).toContainText(
    "9007199254740993",
  );
  const pre = await dialog
    .getByLabel("版本 JSON", { exact: true })
    .boundingBox();
  expect(pre!.width).toBeGreaterThan(250);
  await capture(page, "dashboard-history-zh-mobile.png");
  await dialog.getByLabel("版本 JSON", { exact: true }).scrollIntoViewIfNeeded();
  await capture(page, "dashboard-history-json-zh-mobile.png");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  for (const button of await dialog
    .locator(".history-row-actions button")
    .all()) {
    const box = await button.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  }
  await dialog
    .getByRole("button", { name: `恢复版本 ${first}`, exact: true })
    .click();
  await saveRaw(
    page,
    endpoint,
    saved.id,
    (await rawNative(page, endpoint, saved.id)).replaceAll(
      "Conflict changed",
      "Concurrent change",
    ),
  );
  const before = await versions(page, endpoint, saved.id);
  await dialog.getByRole("button", { name: "确认恢复", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "确认恢复", exact: true }),
  ).toBeDisabled();
  expect((await versions(page, endpoint, saved.id)).versions).toHaveLength(
    before.versions.length,
  );
  await dialog
    .getByRole("button", { name: "重新加载仪表盘", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Concurrent change", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "版本历史", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "版本历史", exact: true });
  await dialog
    .getByRole("button", { name: `恢复版本 ${first}`, exact: true })
    .click();
  await dialog.getByRole("button", { name: "确认恢复", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Conflict initial", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("Native panel edits and removals keep their opening revision after an external save refreshes the view", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(45000);
  const errors = issues(page, [409]);
  const panel = {
    id: "original-panel",
    title: "Original panel",
    metric: "metricspanel_uptime_seconds",
    expr: "vector(23)",
    aggregation: "last",
    unit: "",
  };
  const created = await page.request.post(endpoint + "/api/v1/dashboards", {
    data: { name: "Concurrent native", panels: [panel] },
  });
  expect(created.ok()).toBeTruthy();
  const saved = await created.json();
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(`${endpoint}/d/${saved.id}/Concurrent%20native`);
  await expect(
    page.getByRole("heading", { name: "Concurrent native", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Edit Original panel", exact: true })
    .click();
  let dialog = page.getByRole("dialog", { name: "Edit panel", exact: true });
  await dialog
    .getByLabel("Panel title", { exact: true })
    .fill("Local stale title");
  let current = JSON.parse(await rawNative(page, endpoint, saved.id));
  current.name = "External native save";
  current.panels[0].title = "External original";
  current.panels.push({
    ...panel,
    id: "external-panel",
    title: "External added panel",
  });
  await saveRaw(page, endpoint, saved.id, JSON.stringify(current));
  await expect(
    page.getByRole("heading", { name: "External native save", exact: true }),
  ).toBeVisible({ timeout: 15000 });
  const before = await versions(page, endpoint, saved.id);
  await dialog.getByRole("button", { name: "Save panel", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText(
    "Dashboard changed. Close and reopen this editor.",
  );
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel("Panel title", { exact: true })).toHaveValue(
    "Local stale title",
  );
  current = JSON.parse(await rawNative(page, endpoint, saved.id));
  expect(current.name).toBe("External native save");
  expect(current.panels.map((item: any) => item.title)).toEqual([
    "External original",
    "External added panel",
  ]);
  expect((await versions(page, endpoint, saved.id)).versions).toHaveLength(
    before.versions.length,
  );
  await capture(page, "dashboard-history-native-edit-conflict-en-desktop.png");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await page
    .getByRole("button", { name: "Remove External original", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Remove panel", exact: true });
  current.name = "Second external save";
  current.panels[1].title = "Second external panel";
  await saveRaw(page, endpoint, saved.id, JSON.stringify(current));
  await expect(
    page.getByRole("heading", { name: "Second external save", exact: true }),
  ).toBeVisible({ timeout: 15000 });
  const beforeRemoval = await versions(page, endpoint, saved.id);
  await dialog
    .getByRole("button", { name: "Remove panel", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText(
    "Dashboard changed. Close and reopen this editor.",
  );
  expect((await versions(page, endpoint, saved.id)).versions).toHaveLength(
    beforeRemoval.versions.length,
  );
  current = JSON.parse(await rawNative(page, endpoint, saved.id));
  expect(current.panels).toHaveLength(2);
  expect(current.panels[1].title).toBe("Second external panel");
  expect(errors).toEqual([]);
});
