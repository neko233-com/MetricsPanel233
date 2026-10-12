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
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-folder-library-e2e-"),
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
        serverLogs = "";
      service.stderr!.on("data", (chunk) => {
        serverLogs += chunk.toString();
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
        if (process.env.METRICSPANEL_CATALOG_QA_DIR)
          await writeFile(
            path.join(
              process.env.METRICSPANEL_CATALOG_QA_DIR,
              `${path.basename(temp)}-server.log`,
            ),
            serverLogs,
          );
        const resolved = path.resolve(temp);
        if (
          !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
          !path
            .basename(resolved)
            .startsWith("metricspanel233-folder-library-e2e-")
        )
          throw new Error("Unsafe folder/library fixture cleanup");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});
const nav = (page: Page) =>
  page.getByRole("navigation", { name: "Folder navigation", exact: true });
async function visit(page: Page, endpoint: string) {
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(endpoint + "/#dashboards");
  await expect(page).toHaveTitle("MetricsPanel233");
  await expect(
    page.getByRole("heading", { name: "Dashboards", exact: true }),
  ).toBeVisible();
  await expect(nav(page)).toBeVisible();
  await expect(page.locator(".dashboard-file-input")).not.toBeVisible();
}
async function createFolder(page: Page, name: string) {
  await nav(page)
    .getByRole("button", { name: "New folder", exact: true })
    .click();
  const dialog = page.getByRole("dialog", { name: "New folder", exact: true });
  await dialog.getByLabel("Folder name", { exact: true }).fill(name);
  await dialog
    .getByRole("button", { name: "Save folder", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    nav(page).getByRole("button", { name, exact: true }),
  ).toBeVisible();
}
async function capture(page: Page, name: string) {
  if (!process.env.METRICSPANEL_CATALOG_QA_DIR) return;
  if (!(await page.getByRole("dialog").count())) {
    const dismiss = page.locator(".toast .icon-button");
    if (await dismiss.isVisible()) await dismiss.click();
  }
  await mkdir(process.env.METRICSPANEL_CATALOG_QA_DIR, { recursive: true });
  await page.screenshot({
    path: path.join(process.env.METRICSPANEL_CATALOG_QA_DIR, name),
    fullPage: true,
    animations: "disabled",
  });
}
async function rawDownload(page: Page, id: string) {
  const ready = page.waitForEvent("download");
  await page.getByRole("button", { name: `Export ${id}`, exact: true }).click();
  const artifact = await ready;
  return readFile((await artifact.path())!, "utf8");
}

test("Folders create nested paths, rename and move with revision checks; imports and raw exports retain unsafe integers", async ({
  page,
  endpoint,
}) => {
  await visit(page, endpoint);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (
      ["error", "warning"].includes(entry.type()) &&
      !entry.text().includes("status of 412")
    )
      errors.push(entry.text());
  });
  await createFolder(page, "Operations");
  await nav(page)
    .getByRole("button", { name: "Operations", exact: true })
    .click();
  await createFolder(page, "Services");
  await nav(page)
    .getByRole("button", { name: "Services", exact: true })
    .click();
  await expect(
    page.getByRole("navigation", { name: "Folder path" }),
  ).toContainText("GeneralOperationsServices");
  await page
    .getByRole("button", { name: "New dashboard", exact: true })
    .click();
  let dialog = page.getByRole("dialog", { name: "New dashboard", exact: true });
  await dialog
    .getByLabel("Dashboard name", { exact: true })
    .fill("Service metrics");
  const folders = await (
    await page.request.get(endpoint + "/api/v1/folders")
  ).json();
  const services = folders.find((item: any) => item.title === "Services");
  const operations = folders.find((item: any) => item.title === "Operations");
  await expect(dialog.getByRole("combobox")).toHaveValue(services.uid);
  await dialog
    .getByRole("button", { name: "Create dashboard", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.goto(endpoint + "/#dashboards");
  await expect(nav(page)).toBeVisible();
  const dashboards = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  expect(
    dashboards.find((item: any) => item.name === "Service metrics").folder_uid,
  ).toBe(services.uid);
  await nav(page)
    .getByRole("button", { name: "Operations", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Import dashboard", exact: true });
  await dialog.getByRole("tab", { name: "JSON", exact: true }).click();
  const raw =
    '{"uid":"folder-raw","title":"Folder import","opaque":9007199254740993,"text":"9007199254740993","panels":[{"id":1,"type":"stat","title":"Integer","targets":[{"refId":"A","expr":"vector(233)"}]}]}';
  await dialog.getByLabel("Dashboard JSON", { exact: true }).fill(raw);
  await dialog
    .getByRole("button", { name: "Load dashboard", exact: true })
    .click();
  await expect(
    dialog.getByRole("combobox", { name: "Folder", exact: true }),
  ).toHaveValue(operations.uid);
  await dialog
    .getByRole("button", { name: "Import dashboard", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.goto(endpoint + "/#dashboards");
  await expect(nav(page)).toBeVisible();
  let imported = (
    await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
  ).find((item: any) => item.name === "Folder import");
  expect(imported.folder_uid).toBe(operations.uid);
  await page
    .getByRole("button", { name: "Move dashboard Folder import", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Move dashboard", exact: true });
  await dialog.getByRole("combobox").selectOption(services.uid);
  await dialog
    .getByRole("button", { name: "Move dashboard", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const exported = await rawDownload(page, "Folder import");
  expect(exported).toContain('"opaque":9007199254740993');
  expect(exported).toContain('"text":"9007199254740993"');
  imported = (
    await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
  ).find((item: any) => item.name === "Folder import");
  expect(imported.folder_uid).toBe(services.uid);

  await nav(page)
    .getByRole("button", { name: "Rename folder Operations", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Rename folder", exact: true });
  const changed = await page.request.put(
    endpoint + `/api/folders/${operations.uid}`,
    { data: { title: "Operations from CLI", version: operations.version } },
  );
  expect(changed.ok()).toBeTruthy();
  await dialog.getByLabel("Folder name", { exact: true }).fill("Stale title");
  await dialog
    .getByRole("button", { name: "Save folder", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("version changed");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.reload();
  await expect(
    nav(page).getByRole("button", { name: "Operations from CLI", exact: true }),
  ).toBeVisible();
  await nav(page)
    .getByRole("button", {
      name: "Rename folder Operations from CLI",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", { name: "Rename folder", exact: true });
  await dialog
    .getByLabel("Folder name", { exact: true })
    .fill("Operations renamed");
  await dialog
    .getByRole("button", { name: "Save folder", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await nav(page)
    .getByRole("button", { name: "Move folder Services", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Move folder", exact: true });
  await expect(
    dialog.getByRole("option", {
      name: "Operations renamed / Services",
      exact: true,
    }),
  ).toHaveCount(0);
  await dialog.getByRole("combobox").selectOption("");
  await dialog
    .getByRole("button", { name: "Save folder", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await nav(page)
    .getByRole("button", { name: "Services", exact: true })
    .click();
  await expect(
    page.getByRole("navigation", { name: "Folder path" }),
  ).toContainText("GeneralServices");
  await capture(page, "folder-browser-en-desktop.png");
  await nav(page)
    .getByRole("button", { name: "Delete folder Services", exact: true })
    .click();
  dialog = page.getByRole("dialog", { name: "Delete folder", exact: true });
  await expect(dialog).toContainText("also deletes its subfolders, dashboards");
  await expect(dialog.getByRole("checkbox")).not.toBeChecked();
  await dialog
    .getByRole("button", { name: "Delete folder", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    (
      await (await page.request.get(endpoint + "/api/v1/dashboards")).json()
    ).some((item: any) =>
      ["Service metrics", "Folder import"].includes(item.name),
    ),
  ).toBeFalsy();
  expect(errors).toEqual([]);
});

test("Library models retain unsafe integers through shared updates, insertion, conflicts, unlink and delete", async ({
  page,
  endpoint,
}) => {
  await visit(page, endpoint);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (
      ["error", "warning"].includes(entry.type()) &&
      !/status of (400|409|412)/.test(entry.text())
    )
      errors.push(entry.text());
  });
  const dashboardIDs = new Map<string, string>();
  const libraryWrites: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().endsWith("/api/library-elements/shared-status") &&
      request.method() === "PATCH"
    )
      libraryWrites.push(request.postData() || "");
  });
  for (const id of ["library-a", "library-b"]) {
    const response = await page.request.post(endpoint + "/api/v1/dashboards", {
      data: { id, name: id, panels: [] },
    });
    expect(response.ok()).toBeTruthy();
    dashboardIDs.set(id, (await response.json()).id);
  }
  await page.reload();
  await expect(nav(page)).toBeVisible();
  await page.getByRole("tab", { name: "Library panels", exact: true }).click();
  await expect(
    page.getByText("No library panels found.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "New library panel", exact: true })
    .click();
  let dialog = page.getByRole("dialog", {
    name: "New library panel",
    exact: true,
  });
  await dialog
    .getByLabel("Library panel name", { exact: true })
    .fill("Shared service status");
  await dialog
    .getByLabel("Library panel UID (optional)", { exact: true })
    .fill("shared-status");
  const model =
    '{"type":"stat","title":"Shared stat","opaque":9007199254740993,"text":"9007199254740993","targets":[{"refId":"A","expr":"vector(233)"}],"fieldConfig":{"defaults":{},"overrides":[]},"options":{}}';
  await dialog
    .getByRole("textbox", { name: "Panel model JSON", exact: true })
    .fill(model);
  await dialog
    .getByRole("button", { name: "Save library panel", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("button", {
      name: "Edit library panel Shared service status",
      exact: true,
    }),
  ).toBeVisible();
  for (const id of ["library-a", "library-b"]) {
    await page
      .getByRole("button", {
        name: "Add to dashboard Shared service status",
        exact: true,
      })
      .click();
    dialog = page.getByRole("dialog", {
      name: "Add to dashboard",
      exact: true,
    });
    await dialog.getByRole("combobox").selectOption(dashboardIDs.get(id)!);
    await dialog
      .getByRole("button", { name: "Add library panel", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
  }
  await expect(
    page.getByRole("button", {
      name: "Connections Shared service status",
      exact: true,
    }),
  ).toHaveText("2");
  await page
    .getByRole("button", {
      name: "Edit library panel Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", {
    name: "Edit library panel",
    exact: true,
  });
  await expect(
    dialog.getByRole("textbox", { name: "Panel model JSON", exact: true }),
  ).toHaveValue(model);
  const old = (
    await (
      await page.request.get(endpoint + "/api/library-elements/shared-status")
    ).json()
  ).result;
  const external = await page.request.patch(
    endpoint + "/api/library-elements/shared-status",
    { data: { name: "Shared service status", version: old.version } },
  );
  expect(external.ok()).toBeTruthy();
  await dialog
    .getByRole("button", { name: "Save library panel", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("version changed");
  await dialog
    .getByRole("button", { name: "Reload model", exact: true })
    .click();
  await expect(
    dialog.getByRole("textbox", { name: "Panel model JSON", exact: true }),
  ).toHaveValue(model);
  await expect(
    dialog.getByRole("button", { name: "Save library panel", exact: true }),
  ).toBeEnabled();
  await dialog
    .getByRole("textbox", { name: "Panel model JSON", exact: true })
    .fill(model.replace("Shared stat", "Shared updated"));
  await dialog
    .getByRole("button", { name: "Save library panel", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const getDashboards = async () =>
    (await (
      await page.request.get(endpoint + "/api/v1/dashboards")
    ).json()) as any[];
  expect(libraryWrites.at(-1)).toContain("Shared updated");
  expect(
    await (
      await page.request.get(endpoint + "/api/v1/library-models/shared-status")
    ).text(),
  ).toContain("Shared updated");
  for (const id of ["library-a", "library-b"])
    expect(
      (await getDashboards()).find((item) => item.id === dashboardIDs.get(id))
        .panels[0].title,
    ).toBe("Shared updated");
  await page
    .getByRole("button", {
      name: "Delete library panel Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", {
    name: "Delete library panel",
    exact: true,
  });
  await dialog
    .getByRole("button", { name: "Delete library panel", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("unlink");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await capture(page, "library-panels-en-desktop.png");
  await page
    .getByRole("button", {
      name: "Connections Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", { name: "Panel connections", exact: true });
  await expect(
    dialog.getByRole("button", { name: "library-a", exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "library-b", exact: true }),
  ).toBeVisible();
  await dialog
    .getByRole("button", {
      name: "Unlink panel Shared updated library-a",
      exact: true,
    })
    .click();
  await expect(
    dialog.getByRole("button", { name: "library-a", exact: true }),
  ).not.toBeVisible();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page
    .getByRole("button", {
      name: "Edit library panel Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", {
    name: "Edit library panel",
    exact: true,
  });
  await expect(
    dialog.getByRole("textbox", { name: "Panel model JSON", exact: true }),
  ).toHaveValue(/9007199254740993/);
  await expect(
    dialog.getByRole("button", { name: "Save library panel", exact: true }),
  ).toBeEnabled();
  await dialog
    .getByRole("textbox", { name: "Panel model JSON", exact: true })
    .fill(model.replace("Shared stat", "Shared latest"));
  await dialog
    .getByRole("button", { name: "Save library panel", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const detached = (await getDashboards()).find(
    (item) => item.id === dashboardIDs.get("library-a"),
  );
  const linked = (await getDashboards()).find(
    (item) => item.id === dashboardIDs.get("library-b"),
  );
  expect(detached.panels[0].title).toBe("Shared updated");
  expect(detached.panels[0].config.libraryPanel).toBeUndefined();
  expect(linked.panels[0].title).toBe("Shared latest");
  await page
    .getByRole("button", {
      name: "Connections Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", { name: "Panel connections", exact: true });
  await dialog
    .getByRole("button", {
      name: "Unlink panel Shared latest library-b",
      exact: true,
    })
    .click();
  await expect(
    dialog.getByText("This panel is not connected to a dashboard.", {
      exact: true,
    }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page
    .getByRole("button", {
      name: "Delete library panel Shared service status",
      exact: true,
    })
    .click();
  dialog = page.getByRole("dialog", {
    name: "Delete library panel",
    exact: true,
  });
  await dialog
    .getByRole("button", { name: "Delete library panel", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("No library panels found.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "Dashboards", exact: true }).click();
  for (const id of ["library-a", "library-b"]) {
    const exported = await rawDownload(page, id);
    expect(exported).toContain('"opaque":9007199254740993');
    expect(exported).toContain('"text":"9007199254740993"');
  }
  expect(errors).toEqual([]);
});

test("Chinese folder and library controls fit a desktop viewport and recover from a failed read", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (
      ["error", "warning"].includes(entry.type()) &&
      !entry.text().includes("503")
    )
      errors.push(entry.text());
  });
  await page.setViewportSize({ width: 1536, height: 1024 });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "zh"),
  );
  await page.goto(endpoint + "/#dashboards");
  await expect(
    page.getByRole("navigation", { name: "文件夹导航", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".dashboard-file-input")).not.toBeVisible();
  await page
    .getByRole("navigation", { name: "文件夹导航", exact: true })
    .getByRole("button", { name: "新建文件夹", exact: true })
    .click();
  let dialog = page.getByRole("dialog", { name: "新建文件夹", exact: true });
  await dialog.getByLabel("文件夹名称", { exact: true }).fill("数据平台");
  await dialog.getByRole("button", { name: "保存文件夹", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await capture(page, "folder-browser-zh-desktop.png");
  let failed = false;
  await page.route("**/api/library-elements?*", async (route) => {
    if (!failed) {
      failed = true;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "Catalog temporarily unavailable" }),
      });
    } else await route.continue();
  });
  await page.getByRole("tab", { name: "库面板", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Catalog temporarily unavailable",
  );
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(
    page.getByText("没有找到库面板。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "新建库面板", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "新建库面板", exact: true });
  await dialog.getByLabel("库面板名称", { exact: true }).fill("集群状态");
  await dialog
    .getByRole("combobox", { name: "文件夹", exact: true })
    .selectOption({ label: "数据平台" });
  await expect(
    dialog.getByRole("textbox", { name: "面板模型 JSON", exact: true }),
  ).toBeVisible();
  expect(
    (await dialog
      .getByRole("textbox", { name: "面板模型 JSON", exact: true })
      .boundingBox())!.width,
  ).toBeGreaterThan(250);
  await capture(page, "library-model-zh-desktop.png");
  await dialog.getByRole("button", { name: "保存库面板", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: "编辑库面板 集群状态", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "删除库面板 集群状态", exact: true })
    .scrollIntoViewIfNeeded();
  await expect(
    page.getByRole("button", { name: "删除库面板 集群状态", exact: true }),
  ).toBeInViewport({ ratio: 1 });
  await expect(
    page.getByRole("button", { name: "添加到仪表盘 集群状态", exact: true }),
  ).toBeInViewport({ ratio: 1 });
  await capture(page, "library-panels-zh-desktop.png");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
});

test("Library UID folder filters, search and page shrink use the real catalog", async ({
  page,
  endpoint,
}) => {
  const response = await page.request.post(endpoint + "/api/folders", {
    data: { uid: "paging-fixtures", title: "Paging fixtures" },
  });
  expect(response.ok()).toBeTruthy();
  for (let index = 0; index < 21; index++) {
    const created = await page.request.post(
      endpoint + "/api/library-elements",
      {
        data: {
          uid: `paging-${index}`,
          name: `Page fixture ${String(index).padStart(2, "0")}`,
          kind: 1,
          folderUid: "paging-fixtures",
          model: { type: "stat", title: "Page", targets: [] },
        },
      },
    );
    expect(created.ok()).toBeTruthy();
  }
  await visit(page, endpoint);
  await page.getByRole("tab", { name: "Library panels", exact: true }).click();
  await page
    .getByRole("combobox", { name: "Folder", exact: true })
    .selectOption("paging-fixtures");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.locator(".library-panels tbody tr")).toHaveCount(20);
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect(page.locator(".library-panels tbody tr")).toHaveCount(1);
  await page
    .getByRole("button", {
      name: "Delete library panel Page fixture 20",
      exact: true,
    })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Delete library panel",
    exact: true,
  });
  await dialog
    .getByRole("button", { name: "Delete library panel", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.locator(".library-panels tbody tr")).toHaveCount(20);
  await expect(
    page.getByRole("button", { name: "Previous", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Next", exact: true }),
  ).toBeDisabled();
  await page
    .getByLabel("Search library panels", { exact: true })
    .fill("Page fixture 03");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.locator(".library-panels tbody tr")).toHaveCount(1);
  await expect(
    page.getByRole("button", {
      name: "Edit library panel Page fixture 03",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByLabel("Search library panels", { exact: true })
    .fill("not-in-library");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(
    page.getByText("No library panels found.", { exact: true }),
  ).toBeVisible();
  expect(
    (await page.request.delete(endpoint + "/api/folders/paging-fixtures")).ok(),
  ).toBeTruthy();
});

test("A real browser snapshot detaches library references and retains its frozen text after shared updates", async ({
  page,
  endpoint,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) errors.push(entry.text());
  });
  const model = {
    type: "text",
    title: "Shared text",
    options: { mode: "markdown", content: "Frozen library content" },
  };
  const created = await page.request.post(endpoint + "/api/library-elements", {
    data: { uid: "snapshot-text", name: "Snapshot text", kind: 1, model },
  });
  expect(created.ok()).toBeTruthy();
  const library = (await created.json()).result;
  const imported = await page.request.post(
    endpoint + "/api/v1/import/grafana",
    {
      data: {
        uid: "library-snapshot-source",
        title: "Library snapshot source",
        refresh: "",
        panels: [
          {
            id: 1,
            type: "text",
            title: "Shared text",
            libraryPanel: { uid: library.uid, name: library.name },
            gridPos: { x: 0, y: 0, w: 24, h: 8 },
          },
        ],
      },
    },
  );
  expect(imported.ok(), await imported.text()).toBeTruthy();
  const source = (await imported.json()).dashboard;
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  await page.goto(endpoint + "/d/library-snapshot-source/view");
  await expect(
    page.getByRole("region", { name: "Shared text", exact: true }),
  ).toContainText("Frozen library content");
  await page
    .getByRole("button", { name: "Create snapshot", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Create snapshot",
    exact: true,
  });
  await dialog
    .getByLabel("Snapshot name", { exact: true })
    .fill("Frozen library snapshot");
  await dialog
    .getByRole("button", { name: "Save snapshot", exact: true })
    .click();
  const link = dialog.getByRole("link", { name: "Open snapshot", exact: true });
  await expect(link).toBeVisible();
  const snapshots = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  const snapshot = snapshots.find(
    (dashboard: any) => dashboard.name === "Frozen library snapshot",
  );
  expect(snapshot).toBeTruthy();
  expect(JSON.stringify(snapshot)).not.toContain('"libraryPanel"');
  expect(snapshot.panels[0].config.options.content).toBe(
    "Frozen library content",
  );
  expect(snapshot.grafana.panels[0].options.content).toBe(
    "Frozen library content",
  );
  const changed = await page.request.patch(
    endpoint + `/api/library-elements/${library.uid}`,
    {
      data: {
        version: library.version,
        model: {
          ...model,
          options: { ...model.options, content: "Shared library changed" },
        },
      },
    },
  );
  expect(changed.ok()).toBeTruthy();
  const dashboards = await (
    await page.request.get(endpoint + "/api/v1/dashboards")
  ).json();
  expect(
    dashboards.find((dashboard: any) => dashboard.id === source.id).panels[0]
      .config.options.content,
  ).toBe("Shared library changed");
  const saved = dashboards.find(
    (dashboard: any) => dashboard.id === snapshot.id,
  );
  expect(JSON.stringify(saved)).not.toContain('"libraryPanel"');
  expect(saved.panels[0].config.options.content).toBe("Frozen library content");
  const connections = (
    await (
      await page.request.get(
        endpoint + `/api/library-elements/${library.uid}/connections`,
      )
    ).json()
  ).result;
  expect(connections).toHaveLength(1);
  expect(connections[0].dashboardUid).toBe(source.id);
  expect(
    connections.some(
      (connection: any) => connection.dashboardUid === snapshot.id,
    ),
  ).toBeFalsy();
  await link.click();
  await expect(
    page.getByRole("heading", { name: "Frozen library snapshot", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Shared text", exact: true }),
  ).toContainText("Frozen library content");
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Shared text", exact: true }),
  ).toContainText("Frozen library content");
  await capture(page, "library-snapshot-en-desktop.png");
  await page.goto(endpoint + "/d/library-snapshot-source/view");
  await expect(
    page.getByRole("region", { name: "Shared text", exact: true }),
  ).toContainText("Shared library changed");
  expect(errors).toEqual([]);
});
