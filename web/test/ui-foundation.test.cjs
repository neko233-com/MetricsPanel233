const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const read = (file) => fs.readFileSync(path.join(root, file), "utf8");

test("the application mounts inside the Grafana theme provider", () => {
  const main = read("src/main.tsx");
  assert.match(main, /AppThemeProvider/);
  assert.match(main, /window\.__grafana_public_path__ = "\/"/);
  assert.ok(main.indexOf("__grafana_public_path__") < main.indexOf("createRoot("));
  assert.match(read("src/ui/theme.tsx"), /createTheme\(\{ colors: \{ mode: "dark" \} \}\)/);
});

test("product UI primitives are first-party, not Grafana re-exports", () => {
  const adapter = read("src/ui/index.tsx");
  for (const name of ["Button", "Input", "Field", "AppModal", "Alert", "Badge", "Select", "Spinner"]) {
    assert.match(adapter, new RegExp(`\\b${name}\\b`));
  }
  assert.doesNotMatch(adapter, /from "@grafana\//);
  assert.doesNotMatch(adapter, /from "@grafana\/ui"/);
  assert.match(adapter, /export function useStyles2/);
});

test("Grafana icon assets are shipped by the package and copied to dist", () => {
  const icons = path.join(
    root,
    "node_modules/@grafana/ui/dist/public/img/icons/unicons/times.svg",
  );
  assert.equal(fs.existsSync(icons), true);
  assert.match(read("vite.config.ts"), /grafanaIconAssets/);
  assert.match(read("vite.config.ts"), /dist.*build.*img.*icons/);
});

test("product dialogs use the first-party modal instead of a native dialog element", () => {
  const dialog = read("src/components/Dialog.tsx");
  assert.match(dialog, /AppModal/);
  assert.doesNotMatch(dialog, /<dialog/);
  assert.doesNotMatch(dialog, /showModal\(/);
});

test("stat, gauge, bar gauge and logs are first-party renderer registrations", () => {
  const index = read("src/render/index.ts");
  for (const kind of ["stat", "gauge", "bargauge", "logs"]) {
    assert.match(index, new RegExp(`kind: "${kind}"`));
  }
  assert.match(index, /StatRenderer/);
  assert.match(index, /LogsRenderer/);
  assert.match(read("src/render/stat/StatRenderer.tsx"), /role="meter"/);
  assert.match(read("src/render/logs/LogsRenderer.tsx"), /role="log"/);
  assert.match(read("src/grafana/GrafanaPanel.tsx"), /getRenderer\(type\)/);
});
