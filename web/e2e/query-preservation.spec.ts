import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);

test("Query editing preserves imported raw fields after browser number rounding", async ({
  page,
}) => {
  const temp = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-query-preservation-e2e-"),
  );
  const service = spawn(
    process.env.METRICSPANEL_TEST_BINARY ||
      path.join(
        root,
        "bin",
        process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
      ),
    ["serve", "--listen", "127.0.0.1:0", "--db", path.join(temp, "control.db")],
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
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
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
    await page.addInitScript(() =>
      localStorage.setItem("metricspanel-locale", "en"),
    );
    const source = `{"uid":"query-precision-pc","title":"Query preservation","opaque":9007199254740993,"panels":[{"id":1,"type":"stat","title":"Orders","gridPos":{"x":0,"y":0,"w":12,"h":8},"options":{"opaque":9007199254740993},"targets":[{"refId":"A","expr":"vector(233)","instant":true,"opaque":9007199254740993,"nested":{"counter":9007199254740993},"datasource":{"uid":"metricspanel","type":"prometheus"}}]},{"id":2,"type":"text","title":"Untouched","gridPos":{"x":12,"y":0,"w":12,"h":8},"options":{"content":"Keep this panel","opaque":9007199254740993}}]}`;
    const imported = await page.request.post(
      endpoint + "/api/v1/import/grafana",
      { data: source, headers: { "Content-Type": "application/json" } },
    );
    expect(imported.ok(), await imported.text()).toBeTruthy();
    const saved = (await imported.json()).dashboard;
    await page.goto(endpoint + "/d/query-precision-pc/query-preservation");
    await expect(
      page.getByRole("heading", { name: "Query preservation", exact: true }),
    ).toBeVisible();
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
    expect(query.opaque).toBe(9007199254740992);
    query.expr = "vector(234)";
    await field.fill(JSON.stringify(query, null, 2));
    await dialog
      .getByRole("button", { name: "Save queries", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const exported = await page.request.get(
      endpoint + "/api/v1/dashboard-exports/" + saved.id + "?format=grafana",
    );
    expect(exported.ok(), await exported.text()).toBeTruthy();
    const raw = await exported.text();
    expect(raw).toContain("vector(234)");
    expect(raw).toContain("9007199254740993");
    expect(raw).not.toContain("9007199254740992");
    expect(raw).toContain("Keep this panel");
    await page.reload();
    await expect(
      page.getByRole("region", { name: "Orders", exact: true }),
    ).toContainText("234");
    expect(errors).toEqual([]);
    const qa = process.env.METRICSPANEL_QUERY_QA_DIR;
    if (qa) {
      await mkdir(qa, { recursive: true });
      await page.screenshot({
        path: path.join(qa, "query-preservation-desktop.png"),
        fullPage: false,
      });
    }
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
        .startsWith("metricspanel233-query-preservation-e2e-")
    )
      throw new Error("Unsafe query fixture cleanup");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});
