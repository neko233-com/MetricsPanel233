import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  // A few long-running dialog interactions are timing-sensitive on loaded
  // Windows hosts. One retry keeps the signal without hiding a persistent bug.
  retries: 1,
  reporter: "list",
  expect: { timeout: 10000 },
  use: {
    browserName: "chromium",
    headless: true,
    viewport: { width: 1536, height: 1024 },
  },
});
