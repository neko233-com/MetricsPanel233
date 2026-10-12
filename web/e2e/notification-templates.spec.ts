import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
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
        path.join(os.tmpdir(), "metricspanel233-template-e2e-"),
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
            /* Log records may be split across chunks. */
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
          !path.basename(resolved).startsWith("metricspanel233-template-e2e-")
        )
          throw new Error(
            "Refusing cleanup outside notification template test directory",
          );
        await rm(resolved, { recursive: true, force: true, maxRetries: 3 });
      }
    },
    { scope: "worker" },
  ],
});

test("Desktop notification templates preview drafts, deliver selected definitions and preserve contact edits", async ({
  page,
  endpoint,
}) => {
  test.setTimeout(90000);
  page.setDefaultTimeout(15000);
  const received: Record<string, unknown>[] = [];
  const secrets: string[] = [];
  const receiver = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk.toString();
    });
    request.on("end", () => {
      received.push(JSON.parse(body));
      secrets.push(request.headers.authorization || "");
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
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() =>
    localStorage.setItem("metricspanel-locale", "en"),
  );
  const title = `{{define "ops.title"}}{{.Receiver}} {{.Status | toUpper}}{{end}}`;
  const body = `{{define "ops.message"}}{{range .Alerts}}{{.Annotations.summary}}{{end}}{{end}}`;
  const source = title + body;
  try {
    await page.goto(endpoint + "/#alerts");
    await page.getByRole("tab", { name: "Notifications", exact: true }).click();
    const templates = page.getByRole("region", {
      name: "Notification templates",
      exact: true,
    });
    const contacts = page.getByRole("region", {
      name: "Contact points",
      exact: true,
    });
    await templates
      .getByRole("button", { name: "Add template", exact: true })
      .click();
    let editor = page.getByRole("dialog", {
      name: "Add notification template",
      exact: true,
    });
    await editor
      .getByLabel("Template group name", { exact: true })
      .fill("operations");
    await editor
      .getByRole("textbox", { name: "Template source", exact: true })
      .fill(source);
    await editor
      .getByRole("button", { name: "Preview draft", exact: true })
      .click();
    await expect(
      editor.locator("pre").filter({ hasText: "Operations FIRING" }),
    ).toBeVisible();
    let listing = await page.request.get(
      endpoint + "/api/v1/alerting/notification-templates",
    );
    expect(await listing.json()).toEqual([]);
    await editor
      .getByRole("button", { name: "Save template", exact: true })
      .click();
    await expect(editor).not.toBeVisible();
    await expect(templates).toContainText("ops.message, ops.title");
    await contacts
      .getByRole("button", { name: "Add contact point", exact: true })
      .click();
    editor = page.getByRole("dialog", {
      name: "Add contact point",
      exact: true,
    });
    await editor
      .getByLabel("Contact point name", { exact: true })
      .fill("Operations receiver");
    await editor
      .getByLabel("Webhook URL", { exact: true })
      .fill(`http://127.0.0.1:${address.port}/alerts`);
    await editor
      .getByLabel("Bearer token", { exact: true })
      .fill("template-pc-secret");
    await editor
      .getByLabel("Use saved title definition", { exact: true })
      .selectOption("ops.title");
    await editor
      .getByLabel("Use saved message definition", { exact: true })
      .selectOption("ops.message");
    await editor
      .getByRole("button", { name: "Preview with sample data", exact: true })
      .click();
    await expect(
      editor.locator("pre").filter({ hasText: "Preview FIRING" }),
    ).toBeVisible();
    await editor
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(editor).not.toBeVisible();
    const contactRow = contacts
      .getByRole("row")
      .filter({ hasText: "Operations receiver" });
    await contactRow.getByRole("button", { name: "Test", exact: true }).click();
    await expect.poll(() => received.length).toBe(1);
    expect(received[0].title).toBe("Operations receiver FIRING");
    expect(typeof received[0].message).toBe("string");
    expect(secrets[0]).toBe("Bearer template-pc-secret");
    await contactRow.getByRole("button", { name: "Edit", exact: true }).click();
    editor = page.getByRole("dialog", {
      name: "Edit contact point",
      exact: true,
    });
    await expect(
      editor.getByRole("textbox", { name: "Title template", exact: true }),
    ).toHaveValue('{{ template "ops.title" . }}');
    await editor
      .getByLabel("Contact point name", { exact: true })
      .fill("Renamed receiver");
    await editor
      .getByRole("button", { name: "Save contact point", exact: true })
      .click();
    await expect(editor).not.toBeVisible();
    const renamed = contacts
      .getByRole("row")
      .filter({ hasText: "Renamed receiver" });
    await renamed.getByRole("button", { name: "Test", exact: true }).click();
    await expect.poll(() => received.length).toBe(2);
    expect(received[1].title).toBe("Renamed receiver FIRING");
    expect(secrets[1]).toBe("Bearer template-pc-secret");
    const groupRow = templates
      .getByRole("row")
      .filter({ hasText: "operations" });
    await groupRow.getByRole("button", { name: "Edit", exact: true }).click();
    editor = page.getByRole("dialog", {
      name: "Edit notification template",
      exact: true,
    });
    await editor
      .getByRole("textbox", { name: "Template source", exact: true })
      .fill(source.replace("{{.Receiver}}", "Updated {{.Receiver}}"));
    await editor
      .getByRole("button", { name: "Preview draft", exact: true })
      .click();
    await expect(
      editor.locator("pre").filter({ hasText: "Updated Operations FIRING" }),
    ).toBeVisible();
    listing = await page.request.get(
      endpoint + "/api/v1/alerting/notification-templates",
    );
    const stored = await listing.json();
    expect(stored[0].template).toBe(source);
    expect(stored[0].version).toBe(1);
    await editor
      .getByRole("button", { name: "Save template", exact: true })
      .click();
    await expect(editor).not.toBeVisible();
    await renamed.getByRole("button", { name: "Test", exact: true }).click();
    await expect.poll(() => received.length).toBe(3);
    expect(received[2].title).toBe("Updated Renamed receiver FIRING");
    const response = await page.request.get(
      endpoint + "/api/v1/alerting/contact-points",
    );
    const data = await response.text();
    expect(data).not.toContain("template-pc-secret");
    expect(data).toContain("title_template");
    expect(errors).toEqual([]);
  } finally {
    await new Promise<void>((resolve, reject) =>
      receiver.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
