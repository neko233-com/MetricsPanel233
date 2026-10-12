import { test as base, expect } from "@playwright/test";
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
const test = base.extend<{}, { endpoint: string }>({
  endpoint: [
    async ({}, use) => {
      const temp = await mkdtemp(
        path.join(os.tmpdir(), "metricspanel233-silences-e2e-"),
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
            /* Incomplete log chunk. */
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
          !path.basename(resolved).startsWith("metricspanel233-silences-e2e-")
        )
          throw new Error("Refusing cleanup outside silence test directory");
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Desktop silences preview labels, suppress webhooks and resume only firing alerts", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  page.setDefaultTimeout(15000);
  const received: { status: string; commonLabels: Record<string, string> }[] =
    [];
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
    throw new Error("No webhook port");
  const issues: string[] = [];
  page.on("pageerror", (error) => issues.push(error.message));
  page.on("console", (entry) => {
    if (["error", "warning"].includes(entry.type())) issues.push(entry.text());
  });
  const request = page.request;
  try {
    let response = await request.post(
      `${endpoint}/api/v1/alerting/contact-points`,
      {
        data: {
          uid: "silence-webhook",
          name: "Silence receiver",
          url: `http://127.0.0.1:${address.port}/alerts`,
        },
      },
    );
    expect(response.status()).toBe(201);
    response = await request.put(
      `${endpoint}/api/v1/alerting/notification-policy`,
      { data: { version: 0, receiver: "silence-webhook", routes: [] } },
    );
    expect(response.ok()).toBeTruthy();
    await page.addInitScript(() =>
      localStorage.setItem("metricspanel-locale", "en"),
    );
    await page.goto(endpoint + "/#alerts");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    const panel = page.getByRole("region", { name: "Silences", exact: true });
    await expect(panel).toBeVisible();
    await panel
      .getByRole("button", { name: "New silence", exact: true })
      .click();
    let dialog = page.getByRole("dialog", { name: "New silence", exact: true });
    await dialog
      .getByLabel("Matcher value 1", { exact: true })
      .fill("Silence service");
    await dialog.getByLabel("Created by", { exact: true }).fill("operations");
    await dialog
      .getByLabel("Comment", { exact: true })
      .fill("Maintenance window");
    await dialog
      .getByLabel("Test labels JSON", { exact: true })
      .fill('{"alertname":"Silence service"}');
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(dialog.getByRole("status")).toHaveText(
      "Labels match this silence.",
    );
    await dialog
      .getByLabel("Test labels JSON", { exact: true })
      .fill('{"alertname":"Other service"}');
    await dialog
      .getByRole("button", { name: "Test match", exact: true })
      .click();
    await expect(dialog.getByRole("status")).toHaveText(
      "Labels do not match this silence.",
    );
    await dialog
      .getByRole("button", { name: "Save silence", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(panel).toContainText("Maintenance window");
    response = await request.post(`${endpoint}/api/v1/alerts/rules`, {
      data: {
        uid: "silence-service",
        title: "Silence service",
        expr: "vector(1)",
        condition: "nonzero",
        interval_seconds: 86400,
      },
    });
    expect(response.ok()).toBeTruthy();
    response = await request.post(
      `${endpoint}/api/v1/alerts/rules/silence-service/evaluate`,
    );
    expect(response.ok()).toBeTruthy();
    await expect
      .poll(async () =>
        (
          (await (
            await request.get(`${endpoint}/api/v1/alerting/notifications`)
          ).json()) as { state: string }[]
        ).some((v) => v.state === "suppressed"),
      )
      .toBeTruthy();
    expect(received).toHaveLength(0);
    await panel.getByRole("button", { name: "Edit", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "Edit silence", exact: true });
    await dialog
      .getByLabel("Comment", { exact: true })
      .fill("Maintenance updated");
    await dialog
      .getByRole("button", { name: "Save silence", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(panel).toContainText("Maintenance updated");
    await page.reload();
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    await expect(panel).toContainText("Maintenance updated");
    await panel
      .getByRole("button", { name: "End silence", exact: true })
      .click();
    dialog = page.getByRole("dialog", { name: "End silence", exact: true });
    await dialog
      .getByRole("button", { name: "End silence", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect.poll(() => received.length).toBe(1);
    expect(received[0].status).toBe("firing");
    await panel
      .getByLabel("Silence state", { exact: true })
      .selectOption("expired");
    await expect(panel).toContainText("Maintenance updated");
    const now = Date.now();
    response = await request.post(
      `${endpoint}/api/alertmanager/grafana/api/v2/silences`,
      {
        data: {
          matchers: [
            {
              name: "alertname",
              value: "Resolved service",
              isRegex: false,
              isEqual: true,
            },
          ],
          startsAt: new Date(now - 1000).toISOString(),
          endsAt: new Date(now + 60000).toISOString(),
          createdBy: "operations",
          comment: "Recover during maintenance",
        },
      },
    );
    expect(response.ok()).toBeTruthy();
    const silence = (await response.json()) as { silenceID: string };
    response = await request.post(`${endpoint}/api/v1/alerts/rules`, {
      data: {
        uid: "resolved-service",
        title: "Resolved service",
        expr: "vector(1)",
        condition: "nonzero",
        interval_seconds: 86400,
      },
    });
    expect(response.ok()).toBeTruthy();
    response = await request.post(
      `${endpoint}/api/v1/alerts/rules/resolved-service/evaluate`,
    );
    expect(response.ok()).toBeTruthy();
    response = await request.get(
      `${endpoint}/api/v1/alerts/rules/resolved-service`,
    );
    const rule = (await response.json()) as Record<string, unknown>;
    delete rule.runtime;
    response = await request.put(
      `${endpoint}/api/v1/alerts/rules/resolved-service`,
      { data: { ...rule, expr: "vector(0)" } },
    );
    expect(response.ok()).toBeTruthy();
    response = await request.delete(
      `${endpoint}/api/alertmanager/grafana/api/v2/silence/${silence.silenceID}`,
    );
    expect(response.ok()).toBeTruthy();
    await expect
      .poll(async () =>
        (
          (await (
            await request.get(`${endpoint}/api/v1/alerting/notifications`)
          ).json()) as { rule_uid: string; state: string }[]
        )
          .filter((v) => v.rule_uid === "resolved-service")
          .every((v) => v.state === "canceled"),
      )
      .toBeTruthy();
    expect(received).toHaveLength(1);
    await panel.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(panel).toContainText("Recover during maintenance");
    const qa = process.env.METRICSPANEL_SILENCES_QA_DIR;
    if (qa) {
      await mkdir(qa, { recursive: true });
      await panel.screenshot({ path: path.join(qa, "silences-desktop.png") });
    }
    expect(await page.locator(".vite-error-overlay").count()).toBe(0);
    expect(issues).toEqual([]);
  } finally {
    await new Promise<void>((resolve, reject) =>
      receiver.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
