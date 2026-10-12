import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm, mkdir } from "node:fs/promises";
import { createServer, type IncomingHttpHeaders } from "node:http";
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
        path.join(os.tmpdir(), "metricspanel233-notifications-e2e-"),
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
            .startsWith("metricspanel233-notifications-e2e-")
        )
          throw new Error(
            "Refusing cleanup outside notification test directory",
          );
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Desktop webhook management preserves encrypted credentials, delivers tests and persists label routing", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  page.setDefaultTimeout(15000);
  const received: { headers: IncomingHttpHeaders; body: unknown }[] = [];
  let receiverStatus = 200;
  const receiver = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk.toString();
    });
    request.on("end", () => {
      received.push({ headers: request.headers, body: JSON.parse(body) });
      response.writeHead(receiverStatus, {
        "Content-Type": "application/json",
      });
      response.end('{"ok":true}');
    });
  });
  await new Promise<void>((resolve) =>
    receiver.listen(0, "127.0.0.1", resolve),
  );
  const address = receiver.address();
  if (!address || typeof address === "string")
    throw new Error("No webhook port");
  const webhook = `http://127.0.0.1:${address.port}/alerts`;
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  let uid = "";
  try {
    await page.goto(endpoint + "/#alerts");
    await expect(
      page.getByRole("heading", { name: "Alerts", exact: true }),
    ).toBeVisible();
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    const contacts = page.getByRole("region", {
      name: "Contact points",
      exact: true,
    });
    await expect(contacts).toContainText(
      "Add a webhook, then select it in the notification policy.",
    );
    await contacts
      .getByRole("button", { name: "Add contact point", exact: true })
      .click();
    let dialog = page.getByRole("dialog", {
      name: "Add contact point",
      exact: true,
    });
    await dialog
      .getByLabel("Contact point name", { exact: true })
      .fill("Operations webhook");
    await dialog.getByLabel("Webhook URL", { exact: true }).fill(webhook);
    await dialog
      .getByLabel("Bearer token", { exact: true })
      .fill("notification-secret-233");
    await dialog
      .getByRole("button", { name: "Add header", exact: true })
      .click();
    await dialog
      .getByLabel("Header name", { exact: true })
      .fill("X-Webhook-Key");
    await dialog
      .getByLabel("Header value", { exact: true })
      .fill("header-secret-233");
    await dialog
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(contacts).toContainText("Operations webhook");
    let points = await (
      await page.request.get(endpoint + "/api/v1/alerting/contact-points")
    ).json();
    expect(points).toHaveLength(1);
    uid = points[0].uid;
    expect(points[0].secure_fields).toEqual({
      bearer_token: true,
      "header:X-Webhook-Key": true,
    });
    expect(JSON.stringify(points)).not.toContain("notification-secret-233");
    expect(JSON.stringify(points)).not.toContain("header-secret-233");

    await contacts.getByRole("button", { name: "Edit", exact: true }).click();
    dialog = page.getByRole("dialog", {
      name: "Edit contact point",
      exact: true,
    });
    await expect(
      dialog.getByLabel("Bearer token", { exact: true }),
    ).toHaveValue("");
    await expect(
      dialog.getByLabel("Header value", { exact: true }),
    ).toHaveValue("");
    await dialog
      .getByLabel("Contact point name", { exact: true })
      .fill("Primary webhook");
    await dialog
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(contacts).toContainText("Primary webhook");
    await contacts.getByRole("button", { name: "Test", exact: true }).click();
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(1);
    expect(received[0].headers.authorization).toBe(
      "Bearer notification-secret-233",
    );
    expect(received[0].headers["x-webhook-key"]).toBe("header-secret-233");
    const history = page.getByRole("region", {
      name: "Delivery history",
      exact: true,
    });
    await history.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(history).toContainText("Sent");
    await expect(history).toContainText("HTTP 200");
    await expect(history).toContainText("1 / 8");

    const policy = page.getByRole("region", {
      name: "Notification policy",
      exact: true,
    });
    await policy
      .getByLabel("Default contact point", { exact: true })
      .selectOption(uid);
    await policy
      .getByRole("button", { name: "Add route", exact: true })
      .click();
    await policy.getByLabel("Contact point", { exact: true }).selectOption(uid);
    await policy.getByLabel("Label", { exact: true }).fill("severity");
    await policy.getByLabel("Value", { exact: true }).fill("critical|warning");
    await policy.getByLabel("Operator", { exact: true }).selectOption("=~");
    await policy
      .getByLabel("Continue matching later routes", { exact: true })
      .check();
    await policy
      .getByRole("button", { name: "Save policy", exact: true })
      .click();
    await expect(
      policy.getByRole("button", { name: "Save policy", exact: true }),
    ).toBeDisabled();
    const saved = await (
      await page.request.get(endpoint + "/api/v1/alerting/notification-policy")
    ).json();
    expect(saved.receiver).toBe(uid);
    expect(saved.routes).toEqual([
      {
        receiver: uid,
        matchers: [
          { name: "severity", operator: "=~", value: "critical|warning" },
        ],
        continue: true,
      },
    ]);
    await page.reload();
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    await expect(
      policy.getByLabel("Default contact point", { exact: true }),
    ).toHaveValue(uid);
    await expect(policy.getByLabel("Value", { exact: true })).toHaveValue(
      "critical|warning",
    );
    await expect(
      policy.getByLabel("Continue matching later routes", { exact: true }),
    ).toBeChecked();
    await policy.getByLabel("Value", { exact: true }).fill("unsaved");
    await history.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(policy.getByLabel("Value", { exact: true })).toHaveValue(
      "unsaved",
    );
    await policy
      .getByRole("button", { name: "Discard changes", exact: true })
      .click();
    await expect(policy.getByLabel("Value", { exact: true })).toHaveValue(
      "critical|warning",
    );

    await contacts.getByRole("button", { name: "Delete", exact: true }).click();
    dialog = page.getByRole("dialog", {
      name: "Delete contact point",
      exact: true,
    });
    await expect(dialog).toContainText("referenced by the notification policy");
    await expect(
      dialog.getByRole("button", { name: "Delete", exact: true }),
    ).toBeDisabled();
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    if (process.env.METRICSPANEL_NOTIFICATIONS_QA_DIR) {
      const directory = process.env.METRICSPANEL_NOTIFICATIONS_QA_DIR;
      await mkdir(directory, { recursive: true });
      await page.screenshot({
        path: path.join(directory, "notifications-desktop.png"),
        fullPage: true,
      });
    }

    await contacts.getByRole("button", { name: "Edit", exact: true }).click();
    dialog = page.getByRole("dialog", {
      name: "Edit contact point",
      exact: true,
    });
    points = await (
      await page.request.get(endpoint + "/api/v1/alerting/contact-points")
    ).json();
    const changed = await page.request.put(
      endpoint + "/api/v1/alerting/contact-points/" + uid,
      { data: { ...points[0], name: "Changed in another session" } },
    );
    expect(changed.ok(), await changed.text()).toBeTruthy();
    await dialog
      .getByLabel("Contact point name", { exact: true })
      .fill("Stale edit");
    await dialog
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toContainText(
      "configuration changed",
    );
    await expect(
      dialog.getByLabel("Contact point name", { exact: true }),
    ).toHaveValue("Stale edit");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await history.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(contacts).toContainText("Changed in another session");
    await contacts.getByRole("button", { name: "Edit", exact: true }).click();
    dialog = page.getByRole("dialog", {
      name: "Edit contact point",
      exact: true,
    });
    await dialog
      .getByLabel("Remove stored bearer token", { exact: true })
      .check();
    await dialog
      .getByRole("button", { name: "Remove header", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const publicPoint = await (
      await page.request.get(
        endpoint + "/api/v1/alerting/contact-points/" + uid,
      )
    ).json();
    expect(publicPoint.secure_fields).toEqual({});
    await contacts.getByRole("button", { name: "Test", exact: true }).click();
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(2);
    expect(received[1].headers.authorization).toBeUndefined();
    expect(received[1].headers["x-webhook-key"]).toBeUndefined();
    receiverStatus = 400;
    await contacts.getByRole("button", { name: "Test", exact: true }).click();
    await expect
      .poll(
        async () => {
          const entries = await (
            await page.request.get(endpoint + "/api/v1/alerting/notifications")
          ).json();
          return entries.some((entry: any) => entry.state === "failed");
        },
        { timeout: 15000 },
      )
      .toBeTruthy();
    await history.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(history).toContainText("Failed");
    await expect(history).toContainText("HTTP 400");
    receiverStatus = 200;
    await history
      .getByRole("button", { name: "Retry delivery", exact: true })
      .click();
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(4);
    await expect
      .poll(
        async () => {
          const entries = await (
            await page.request.get(endpoint + "/api/v1/alerting/notifications")
          ).json();
          return entries.every((entry: any) => entry.state === "sent");
        },
        { timeout: 15000 },
      )
      .toBeTruthy();
    await history.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(
      history.getByRole("button", { name: "Retry delivery", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("tab", { name: "Notifications", exact: true }).focus();
    await page
      .getByRole("tab", { name: "Notifications", exact: true })
      .press("ArrowLeft");
    await expect(
      page.getByRole("tab", { name: "Alert rules", exact: true }),
    ).toHaveAttribute("aria-selected", "true");
    await page
      .getByRole("button", { name: "Create alert", exact: true })
      .first()
      .click();
    dialog = page.getByRole("dialog", { name: "Create alert", exact: true });
    await dialog
      .getByLabel("Rule name", { exact: true })
      .fill("Notification override");
    await dialog
      .getByLabel("PromQL condition", { exact: true })
      .fill("vector(0)");
    await dialog
      .getByLabel("Contact point override", { exact: true })
      .selectOption(uid);
    await dialog
      .getByRole("button", { name: "Save rule", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const savedRule = (
      await (await page.request.get(endpoint + "/api/v1/alerts/rules")).json()
    ).find((rule: any) => rule.title === "Notification override");
    expect(savedRule.notification_receiver).toBe(uid);
    await page
      .getByRole("button", { name: "Edit Notification override", exact: true })
      .click();
    dialog = page.getByRole("dialog", { name: "Edit alert rule", exact: true });
    await expect(
      dialog.getByLabel("Contact point override", { exact: true }),
    ).toHaveValue(uid);
    await dialog
      .getByLabel("Contact point override", { exact: true })
      .selectOption("");
    await dialog
      .getByRole("button", { name: "Save rule", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const defaultRule = await (
      await page.request.get(endpoint + "/api/v1/alerts/rules/" + savedRule.uid)
    ).json();
    expect(defaultRule.notification_receiver || "").toBe("");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    await page
      .getByRole("button", { name: "Switch language", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "联系点", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "通知策略", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "发送记录", exact: true }),
    ).toBeVisible();
    expect(issues.filter((issue) => !issue.includes("409 (Conflict)"))).toEqual(
      [],
    );
  } finally {
    receiver.closeAllConnections();
    await new Promise<void>((resolve, reject) =>
      receiver.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
