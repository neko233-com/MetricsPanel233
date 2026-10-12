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
        path.join(os.tmpdir(), "metricspanel233-notification-routing-e2e-"),
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
          !path
            .basename(resolved)
            .startsWith("metricspanel233-notification-routing-e2e-")
        )
          throw new Error(
            "Refusing cleanup outside notification routing test directory",
          );
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Desktop recursive notification policy, calendar preview and durable groups deliver real webhooks", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  page.setDefaultTimeout(15000);
  const received: any[] = [];
  const receiver = createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c.toString()));
    req.on("end", () => {
      received.push(JSON.parse(raw));
      res.writeHead(204);
      res.end();
    });
  });
  await new Promise<void>((resolve) =>
    receiver.listen(0, "127.0.0.1", resolve),
  );
  const addr = receiver.address();
  if (!addr || typeof addr === "string")
    throw new Error("Missing webhook port");
  const issues: string[] = [];
  page.on("pageerror", (e) => issues.push(e.message));
  page.on("console", (e) => {
    if (["warning", "error"].includes(e.type())) issues.push(e.text());
  });
  try {
    await page.addInitScript(() =>
      localStorage.setItem("metricspanel-locale", "en"),
    );
    const point = await page.request.post(
      endpoint + "/api/v1/alerting/contact-points",
      {
        data: {
          uid: "routing-webhook",
          name: "Routing receiver",
          url: `http://127.0.0.1:${addr.port}/alerts`,
        },
      },
    );
    expect(point.status(), await point.text()).toBe(201);
    await page.goto(endpoint + "/#alerts");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    const mute = page.getByRole("region", {
      name: "Mute time intervals",
      exact: true,
    });
    await mute
      .getByRole("button", { name: "Add time interval", exact: true })
      .click();
    let dialog = page.getByRole("dialog", {
      name: "Add time interval",
      exact: true,
    });
    await dialog
      .getByLabel("Interval name", { exact: true })
      .fill("daily-window");
    await dialog.getByLabel("Start time", { exact: true }).fill("00:00");
    await dialog.getByLabel("End time", { exact: true }).fill("24:00");
    await dialog.getByLabel("IANA time zone", { exact: true }).fill("UTC");
    await dialog
      .getByRole("button", { name: "Check time", exact: true })
      .click();
    await expect(dialog.getByRole("status")).toHaveText("Within interval");
    await dialog
      .getByRole("button", { name: "Save time interval", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(mute).toContainText("daily-window");
    const policy = page.getByRole("region", {
      name: "Notification policy",
      exact: true,
    });
    await policy
      .getByLabel("Default contact point", { exact: true })
      .selectOption("routing-webhook");
    await policy.getByLabel("Group by", { exact: true }).selectOption("custom");
    await policy.getByLabel("Grouping labels", { exact: true }).fill("team");
    await policy.getByLabel("Grouping labels", { exact: true }).blur();
    await policy.getByLabel("Group wait", { exact: true }).fill("1s");
    await policy.getByLabel("Group interval", { exact: true }).fill("2s");
    await policy.getByLabel("Repeat interval", { exact: true }).fill("5s");
    await expect(
      policy.locator(".notification-effective").first(),
    ).toContainText("repeat 6s");
    await policy
      .getByRole("button", { name: "Add route", exact: true })
      .click();
    const parent = policy.locator('[data-route-depth="0"]');
    await parent.getByLabel("Label", { exact: true }).fill("team");
    await parent.getByLabel("Value", { exact: true }).fill("ops");
    await parent.getByLabel("Contact point", { exact: true }).selectOption("");
    await parent
      .locator(".notification-route-details > summary")
      .first()
      .click();
    await parent
      .getByLabel("Mute time intervals", { exact: true })
      .selectOption("daily-window");
    await parent
      .getByRole("button", { name: "Add child route", exact: true })
      .click();
    const child = parent.locator('[data-route-depth="1"]');
    await child.getByLabel("Contact point", { exact: true }).selectOption("");
    await child.getByLabel("Label", { exact: true }).fill("severity");
    await child.getByLabel("Value", { exact: true }).fill("critical");
    await child
      .locator(".notification-route-details > summary")
      .first()
      .click();
    await child
      .getByLabel("Active time intervals", { exact: true })
      .selectOption("daily-window");
    await policy
      .getByRole("button", { name: "Save policy", exact: true })
      .click();
    await expect(
      policy.getByRole("button", { name: "Save policy", exact: true }),
    ).toBeDisabled();
    const saved = await (
      await page.request.get(endpoint + "/api/v1/alerting/notification-policy")
    ).json();
    expect(saved.group_by).toEqual(["team"]);
    expect(saved.repeat_interval).toBe("5s");
    expect(saved.routes[0].mute_time_intervals).toEqual(["daily-window"]);
    expect(saved.routes[0].routes[0].active_time_intervals).toEqual([
      "daily-window",
    ]);
    expect(saved.routes[0].routes[0].receiver).toBe("");
    await page.reload();
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    await expect(
      policy.getByLabel("Repeat interval", { exact: true }).first(),
    ).toHaveValue("5s");
    await expect(policy.locator('[data-route-depth="1"]')).toBeVisible();
    for (const [uid, expr] of [
      ["group-a", "vector(1)"],
      ["group-b", "vector(2)"],
    ]) {
      const r = await page.request.post(endpoint + "/api/v1/alerts/rules", {
        data: {
          uid,
          title: uid,
          expr,
          condition: "nonzero",
          labels: { team: "ops", severity: "critical" },
          paused: false,
        },
      });
      expect(r.status(), await r.text()).toBe(200);
      const evaluate = await page.request.post(
        endpoint + `/api/v1/alerts/rules/${uid}/evaluate`,
      );
      expect(evaluate.ok(), await evaluate.text()).toBeTruthy();
    }
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(1);
    expect(received[0].alerts).toHaveLength(2);
    expect(received[0].groupLabels).toEqual({ team: "ops" });
    await expect
      .poll(async () => {
        const g = await (
          await page.request.get(
            endpoint + "/api/v1/alerting/notification-groups",
          )
        ).json();
        return g[0]?.effective_repeat_interval;
      })
      .toBe("6s");
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(2);
    expect(received[1].notificationId).not.toBe(received[0].notificationId);
    const groups = page.getByRole("region", {
      name: "Notification groups",
      exact: true,
    });
    await expect(groups).toContainText("2 firing");
    await expect(groups).toContainText("6s");
    if (process.env.METRICSPANEL_ROUTING_QA_DIR) {
      await mkdir(process.env.METRICSPANEL_ROUTING_QA_DIR, { recursive: true });
      await page.screenshot({
        path: path.join(
          process.env.METRICSPANEL_ROUTING_QA_DIR,
          "notification-routing-desktop.png",
        ),
        fullPage: true,
      });
    }
    await mute
      .getByRole("button", { name: "Edit time interval", exact: true })
      .click();
    dialog = page.getByRole("dialog", {
      name: "Edit time interval",
      exact: true,
    });
    await expect(
      dialog.getByLabel("Interval name", { exact: true }),
    ).toBeDisabled();
    await dialog
      .getByLabel("IANA time zone", { exact: true })
      .fill("Asia/Shanghai");
    await dialog
      .getByRole("button", { name: "Save time interval", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(mute).toContainText("Asia/Shanghai");
    expect(issues).toEqual([]);
  } finally {
    receiver.closeAllConnections();
    await new Promise<void>((resolve, reject) =>
      receiver.close((e) => (e ? reject(e) : resolve())),
    );
  }
});
