import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm, mkdir } from "node:fs/promises";
import { createServer } from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);

test("Desktop inhibition rules preview real alerts, preserve conflicts and resume webhooks after restart", async ({
  page,
}) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const temporary = await mkdtemp(
    path.join(os.tmpdir(), "metricspanel233-inhibition-e2e-"),
  );
  const binary =
    process.env.METRICSPANEL_TEST_BINARY ||
    path.join(
      root,
      "bin",
      process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
    );
  let active: ReturnType<typeof spawn> | null = null;
  async function stop() {
    if (active && active.exitCode === null) {
      const exited = new Promise<void>((resolve) =>
        active!.once("exit", () => resolve()),
      );
      active.kill();
      await exited;
    }
    active = null;
  }
  async function start() {
    let endpoint = "",
      logs = "";
    active = spawn(
      binary,
      [
        "serve",
        "--listen",
        "127.0.0.1:0",
        "--db",
        path.join(temporary, "test.db"),
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
    active.stderr!.on("data", (chunk) => {
      logs += chunk.toString();
      for (const line of logs.split("\n")) {
        try {
          const record = JSON.parse(line);
          if (record.address) endpoint = "http://" + record.address;
        } catch {
          /* A log record can span chunks. */
        }
      }
    });
    await expect
      .poll(() => endpoint, { timeout: 15000, message: logs })
      .not.toBe("");
    return endpoint;
  }
  const received: any[] = [];
  const receiver = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => (body += chunk.toString()));
    request.on("end", () => {
      received.push(JSON.parse(body));
      response.writeHead(204);
      response.end();
    });
  });
  await new Promise<void>((resolve) =>
    receiver.listen(0, "127.0.0.1", resolve),
  );
  const address = receiver.address();
  if (!address || typeof address === "string")
    throw new Error("Webhook port unavailable");
  const issues: string[] = [];
  page.on("pageerror", (reason) => issues.push(reason.message));
  page.on("console", (entry) => {
    if (
      ["warning", "error"].includes(entry.type()) &&
      !/Failed to load resource:.*409/.test(entry.text())
    )
      issues.push(entry.text());
  });
  await page.addInitScript(() => {
    // Restart navigation intentionally uses about:blank, whose opaque origin
    // does not have localStorage. Only initialize real application documents.
    if (location.protocol === "http:" || location.protocol === "https:") {
      localStorage.setItem("metricspanel-locale", "en");
    }
  });
  let endpoint = "";
  try {
    endpoint = await start();
    let response = await page.request.post(
      endpoint + "/api/v1/alerting/contact-points",
      {
        data: {
          uid: "inhibition-webhook",
          name: "Inhibition receiver",
          url: "http://127.0.0.1:" + address.port + "/alerts",
        },
      },
    );
    expect(response.status(), await response.text()).toBe(201);
    response = await page.request.put(
      endpoint + "/api/v1/alerting/notification-policy",
      { data: { version: 0, receiver: "inhibition-webhook", routes: [] } },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    async function createAlert(uid: string, severity: string) {
      const saved = await page.request.post(endpoint + "/api/v1/alerts/rules", {
        data: {
          uid,
          title: uid,
          expr: "vector(1)",
          condition: "nonzero",
          interval_seconds: 86400,
          labels: { severity, cluster: "east" },
        },
      });
      expect(saved.ok(), await saved.text()).toBeTruthy();
      const evaluated = await page.request.post(
        endpoint + "/api/v1/alerts/rules/" + uid + "/evaluate",
      );
      expect(evaluated.ok(), await evaluated.text()).toBeTruthy();
    }
    await createAlert("inhibition-source", "critical");
    await expect
      .poll(() =>
        received.some((delivery) =>
          delivery.alerts.some(
            (alert: any) => alert.labels.severity === "critical",
          ),
        ),
      )
      .toBeTruthy();
    const warnings = () =>
      received.filter((delivery) =>
        delivery.alerts.some(
          (alert: any) => alert.labels.severity === "warning",
        ),
      );
    await page.goto(endpoint + "/#alerts");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    const panel = page.getByRole("region", {
      name: "Inhibition rules",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "Add inhibition rule", exact: true })
      .click();
    let dialog = page.getByRole("dialog", {
      name: "Add inhibition rule",
      exact: true,
    });
    await dialog
      .getByLabel("Rule name", { exact: true })
      .fill("Critical suppresses warnings");
    await dialog.getByLabel("Target labels JSON", { exact: true }).fill("[]");
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toHaveText(
      "Target labels must be a JSON object with string values.",
    );
    await expect(dialog.getByLabel("Rule name", { exact: true })).toHaveValue(
      "Critical suppresses warnings",
    );
    await dialog
      .getByLabel("Target labels JSON", { exact: true })
      .fill('{"severity":"warning","cluster":"east"}');
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    const preview = () =>
      dialog.getByRole("region", {
        name: "Inhibition preview result",
        exact: true,
      });
    await expect(preview().getByRole("status")).toHaveText(
      "Candidate notifications are inhibited.",
    );
    await expect(preview()).toContainText("inhibition-source");
    await dialog
      .getByLabel("Target labels JSON", { exact: true })
      .fill('{"severity":"warning","cluster":"west"}');
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(preview().getByRole("status")).toHaveText(
      "Candidate notifications are not inhibited.",
    );
    await dialog.getByLabel("Target value 1", { exact: true }).fill("critical");
    await dialog
      .getByLabel("Target labels JSON", { exact: true })
      .fill('{"severity":"critical","cluster":"east"}');
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(preview().getByRole("status")).toHaveText(
      "Candidate excluded from self-inhibition.",
    );
    await dialog.getByLabel("Target value 1", { exact: true }).fill("warning");
    await dialog
      .getByRole("button", { name: "Save inhibition rule", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    let rules = await (
      await page.request.get(endpoint + "/api/v1/alerting/inhibition-rules")
    ).json();
    expect(rules).toHaveLength(1);
    const uid = rules[0].uid;
    await createAlert("inhibition-target", "warning");
    await expect
      .poll(async () =>
        (
          await (
            await page.request.get(endpoint + "/api/v1/alerting/notifications")
          ).json()
        ).some(
          (delivery: any) =>
            delivery.rule_uid === "inhibition-target" &&
            delivery.state === "suppressed" &&
            delivery.last_error.includes("Inhibited"),
        ),
      )
      .toBeTruthy();
    expect(warnings()).toHaveLength(0);
    await panel
      .getByRole("button", {
        name: "Edit inhibition rule Critical suppresses warnings",
        exact: true,
      })
      .click();
    dialog = page.getByRole("dialog", {
      name: "Edit inhibition rule",
      exact: true,
    });
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(preview()).toContainText("inhibition-target");
    await expect(preview()).toContainText("inhibition-source");
    await dialog.getByLabel("Equal labels", { exact: true }).fill("");
    await dialog
      .getByRole("button", { name: "Save inhibition rule", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await panel
      .getByRole("button", {
        name: "Edit inhibition rule Critical suppresses warnings",
        exact: true,
      })
      .click();
    dialog = page.getByRole("dialog", {
      name: "Edit inhibition rule",
      exact: true,
    });
    await expect(
      dialog.getByLabel("Equal labels", { exact: true }),
    ).toHaveValue("");
    await dialog.getByLabel("Rule name", { exact: true }).fill("Local draft");
    let current = await (
      await page.request.get(
        endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      )
    ).json();
    response = await page.request.put(
      endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      {
        data: {
          ...current,
          name: "External inhibition update",
          updated_at: undefined,
        },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    await dialog
      .getByRole("button", { name: "Save inhibition rule", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toContainText("This rule changed.");
    await expect(dialog.getByLabel("Rule name", { exact: true })).toHaveValue(
      "Local draft",
    );
    current = await (
      await page.request.get(
        endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      )
    ).json();
    expect(current.name).toBe("External inhibition update");
    await dialog.press("Escape");
    await expect(dialog).not.toBeVisible();
    await page.goto("about:blank");
    await stop();
    endpoint = await start();
    current = await (
      await page.request.get(
        endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      )
    ).json();
    expect(current.name).toBe("External inhibition update");
    expect(current.equal).toEqual([]);
    expect(current.disabled).toBe(false);
    expect(warnings()).toHaveLength(0);
    await page.goto(endpoint + "/#alerts");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    await panel
      .getByRole("button", {
        name: "Edit inhibition rule External inhibition update",
        exact: true,
      })
      .click();
    dialog = page.getByRole("dialog", {
      name: "Edit inhibition rule",
      exact: true,
    });
    await dialog.getByLabel("Enabled", { exact: true }).uncheck();
    await dialog
      .getByRole("button", { name: "Save inhibition rule", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect.poll(() => warnings().length, { timeout: 15000 }).toBe(1);
    expect(warnings()[0].status).toBe("firing");
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    const chinese = page.getByRole("region", {
      name: "告警抑制规则",
      exact: true,
    });
    await chinese
      .getByRole("button", {
        name: "编辑抑制规则 External inhibition update",
        exact: true,
      })
      .click();
    const localized = page.getByRole("dialog", {
      name: "编辑抑制规则",
      exact: true,
    });
    await localized
      .getByRole("button", { name: "测试匹配", exact: true })
      .click();
    await expect(
      localized.getByRole("region", { name: "抑制预览结果", exact: true }),
    ).toContainText("未抑制");
    if (process.env.METRICSPANEL_INHIBITION_QA_DIR) {
      await mkdir(process.env.METRICSPANEL_INHIBITION_QA_DIR, {
        recursive: true,
      });
      await page.screenshot({
        path: path.join(
          process.env.METRICSPANEL_INHIBITION_QA_DIR,
          "inhibition-editor-zh-desktop.png",
        ),
        fullPage: true,
        animations: "disabled",
      });
    }
    await localized.getByRole("button", { name: "取消", exact: true }).click();
    await chinese
      .getByRole("button", {
        name: "删除抑制规则 External inhibition update",
        exact: true,
      })
      .click();
    const removing = page.getByRole("dialog", {
      name: "删除抑制规则",
      exact: true,
    });
    await expect(
      removing.getByRole("button", { name: "删除", exact: true }),
    ).not.toBeFocused();
    current = await (
      await page.request.get(
        endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      )
    ).json();
    response = await page.request.put(
      endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
      {
        data: {
          ...current,
          name: "Latest inhibition rule",
          updated_at: undefined,
        },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    await removing.getByRole("button", { name: "删除", exact: true }).click();
    await expect(removing.getByRole("alert")).toHaveText(
      "规则已被修改。请关闭此窗口并刷新列表，再删除最新版本。",
    );
    expect(
      (
        await page.request.get(
          endpoint + "/api/v1/alerting/inhibition-rules/" + uid,
        )
      ).ok(),
    ).toBeTruthy();
    await removing.press("Escape");
    await expect(removing).not.toBeVisible();
    await chinese.getByRole("button", { name: "刷新", exact: true }).click();
    await chinese
      .getByRole("button", {
        name: "删除抑制规则 Latest inhibition rule",
        exact: true,
      })
      .click();
    await removing.getByRole("button", { name: "删除", exact: true }).click();
    await expect(removing).not.toBeVisible();
    rules = await (
      await page.request.get(endpoint + "/api/v1/alerting/inhibition-rules")
    ).json();
    expect(rules).toHaveLength(0);
    expect(issues).toEqual([]);
  } finally {
    await page.goto("about:blank").catch(() => {});
    await stop();
    await new Promise<void>((resolve) => receiver.close(() => resolve()));
    const resolved = path.resolve(temporary);
    if (
      !resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) ||
      !path.basename(resolved).startsWith("metricspanel233-inhibition-e2e-")
    )
      throw new Error("Unsafe inhibition test cleanup target");
    await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
  }
});
