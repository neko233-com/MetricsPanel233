// Production-grade browser verification for local accounts and roles.
// Requires a webui build: go build -tags webui -o bin/metricspanel.exe ./cmd/metricspanel
import { chromium } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = path.resolve(webRoot, "..");
const binary =
  process.env.METRICSPANEL_TEST_BINARY ||
  path.join(
    repoRoot,
    "bin",
    process.platform === "win32" ? "metricspanel.exe" : "metricspanel",
  );
let endpoint = process.env.METRICSPANEL_EXTERNAL_URL || "";
let setupToken = process.env.METRICSPANEL_SETUP_TOKEN || "";
let temp = "";
let server = null;
if (!endpoint) {
  temp = await mkdtemp(path.join(os.tmpdir(), "mp-auth-smoke-"));
  server = spawn(
    binary,
    [
      "serve",
      "--auth",
      "local",
      "--listen",
      "127.0.0.1:0",
      "--db",
      path.join(temp, "control.db"),
      "--identity-db",
      path.join(temp, "identity.db"),
    ],
    {
      cwd: repoRoot,
      windowsHide: true,
      env: { ...process.env, METRICSPANEL_TOKEN: "", METRICSPANEL_STORAGE: "sqlite" },
    },
  );
  let output = "";
  server.on("error", (error) => console.error("spawn error:", error));
  server.stdout.on("data", (chunk) => {
    output += chunk.toString();
    const match = /Setup token: (\S+)/.exec(output);
    if (match) setupToken = match[1];
  });
  server.stderr.on("data", (chunk) => {
    output += chunk.toString();
    for (const line of chunk.toString().split("\n")) {
      try {
        const item = JSON.parse(line);
        if (item.address) endpoint = `http://${item.address}`;
      } catch {}
    }
  });
  const deadline = Date.now() + 20000;
  while ((!endpoint || !setupToken) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!endpoint || !setupToken) {
    console.error("server did not expose endpoint + setup token\n" + output);
    process.exit(1);
  }
}

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1536, height: 1024 },
  locale: "en-US",
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (error) => errors.push(`pageerror: ${error}`));
const httpErrors = [];
page.on("response", (response) => {
  // 401 (wrong password) and 403 (viewer write) are asserted intentionally.
  if (response.status() >= 400 && ![401, 403].includes(response.status())) {
    httpErrors.push(`${response.status()} ${response.url()}`);
  }
});
page.on("console", (message) => {
  if (
    message.type() === "error" &&
    !/favicon|Failed to load resource/i.test(message.text())
  ) {
    errors.push(`console: ${message.text()}`);
  }
});
const check = (condition, label) => {
  if (!condition) {
    errors.push(`assertion: ${label}`);
    console.error("FAILED:", label);
  } else {
    console.log("ok:", label);
  }
};

try {
  await page.goto(endpoint, { waitUntil: "networkidle" });
  await page.getByRole("heading", { name: /Set up MetricsPanel233/ }).waitFor({ timeout: 15000 });
  check(true, "first run shows the setup form");

  await page.getByLabel("Setup token").fill(setupToken);
  await page.getByLabel("Login").fill("admin");
  await page.getByLabel("Name").fill("Administrator");
  await page.getByLabel("Email").fill("admin@example.com");
  await page.getByLabel("Password", { exact: true }).fill("correct-horse-233");
  await page.getByLabel("Confirm password").fill("correct-horse-233");
  await page.getByRole("button", { name: "Create administrator" }).click();
  await page.waitForSelector(".app-shell", { timeout: 20000 });
  check(true, "administrator setup signs in");
  check(await page.locator("aside .workspace").innerText().then((text) => text.includes("Admin")), "sidebar shows Admin role");

  const cookies = await context.cookies();
  const session = cookies.find((cookie) => cookie.name === "metricspanel-session");
  check(Boolean(session?.httpOnly), "session cookie is HttpOnly");
  check(session?.sameSite === "Lax", "session cookie is SameSite=Lax");

  await page.getByRole("button", { name: "Users", exact: true }).click();
  await page.getByRole("heading", { name: "Users", exact: true }).waitFor();
  await page.getByRole("button", { name: "Create user", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Create user" });
  await dialog.getByLabel("Login").fill("viewer");
  await dialog.getByLabel("Name").fill("Viewer");
  await dialog.getByLabel("Email").fill("viewer@example.com");
  await dialog.getByLabel("Password", { exact: true }).fill("viewer-password-233");
  await dialog.getByRole("button", { name: "Create user" }).click();
  await page.getByRole("cell", { name: "viewer", exact: true }).waitFor({ timeout: 10000 });
  check(true, "admin creates a Viewer account");

  await page.getByRole("button", { name: "Sign out" }).click();
  await page.getByRole("heading", { name: "MetricsPanel233" }).waitFor();
  await page.getByLabel("Login").fill("viewer");
  await page.getByLabel("Password").fill("viewer-password-233");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.waitForSelector(".app-shell", { timeout: 20000 });
  check(true, "viewer signs in");
  check((await page.getByRole("button", { name: "Users", exact: true }).count()) === 0, "viewer cannot see Users navigation");
  check((await page.getByRole("button", { name: "Add panel", exact: true }).count()) === 0, "viewer cannot see Add panel");

  const forbidden = await page.evaluate(async () => {
    const response = await fetch("/api/v1/dashboards", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "forbidden", panels: [] }),
    });
    return response.status;
  });
  check(forbidden === 403, "backend rejects viewer dashboard writes with 403");

  await page.getByRole("button", { name: "Sign out" }).click();
  await page.getByLabel("Login").fill("admin");
  await page.getByLabel("Password").fill("wrong-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByText("Invalid username or password").waitFor({ timeout: 10000 });
  check(true, "wrong password shows an error");

  await page.getByLabel("Password", { exact: true }).fill("correct-horse-233");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.waitForSelector(".app-shell", { timeout: 20000 });
  check(true, "admin signs in again");

  check(httpErrors.length === 0, `no unexpected HTTP failures (${httpErrors.join(", ")})`);
  console.log(JSON.stringify({ endpoint, errors, httpErrors }, null, 2));
  if (errors.length || httpErrors.length) process.exitCode = 1;
} finally {
  await browser.close();
  if (server) server.kill();
  if (temp) await rm(temp, { recursive: true, force: true, maxRetries: 3 });
}
