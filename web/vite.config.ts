import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { cpSync, createReadStream, existsSync, statSync } from "node:fs";
import { extname, normalize, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const grafanaIcons = fileURLToPath(
  new URL("./node_modules/@grafana/ui/dist/public/img/icons", import.meta.url),
);
// Local overrides fill assets Grafana serves from its application build but
// does not publish in the SDK package (for example the spinner icon).
const localIcons = fileURLToPath(
  new URL("./public/build/img/icons", import.meta.url),
);
const distIcons = fileURLToPath(new URL("./dist/build/img/icons", import.meta.url));

// @grafana/ui resolves Icon sources at runtime instead of bundling them.
// Serve the package's own icon set in development and copy it into dist so
// the embedded Go binary ships the same assets.
function grafanaIconAssets(): Plugin {
  const prefix = "/build/img/icons/";
  return {
    name: "metricspanel-grafana-icons",
    configureServer(server) {
      server.middlewares.use(prefix, (req, res, next) => {
        let relative = "";
        try {
          relative = normalize(decodeURIComponent((req.url || "").split("?")[0]))
            .replace(/^([/\\])+/, "");
        } catch {
          next();
          return;
        }
        let file = "";
        for (const root of [grafanaIcons, localIcons]) {
          const candidate = resolve(root, relative);
          if (
            candidate.startsWith(resolve(root) + sep) &&
            existsSync(candidate) &&
            statSync(candidate).isFile()
          ) {
            file = candidate;
            break;
          }
        }
        if (!file) {
          next();
          return;
        }
        res.setHeader("Content-Type", extname(file) === ".svg" ? "image/svg+xml" : "application/octet-stream");
        res.setHeader("Cache-Control", "public, max-age=86400");
        createReadStream(file).pipe(res);
      });
    },
    closeBundle() {
      cpSync(grafanaIcons, distIcons, { recursive: true });
      if (existsSync(localIcons)) {
        cpSync(localIcons, distIcons, { recursive: true, force: true });
      }
    },
  };
}

export default defineConfig({
  plugins: [react(), grafanaIconAssets()],
  resolve: {
    // Core boot exports are omitted from the published SDK package exports.
    // Host-only aliases track the pinned 13.2.3 SDK, not plugin imports.
    alias: {
      "metricspanel/sdk-expression-source": fileURLToPath(
        new URL(
          "./node_modules/@grafana/runtime/dist/esm/services/dataSource/expressionDs.mjs",
          import.meta.url,
        ),
      ),
      "metricspanel/sdk-source-settings": fileURLToPath(
        new URL(
          "./node_modules/@grafana/runtime/dist/esm/services/dataSource/settings.mjs",
          import.meta.url,
        ),
      ),
      "metricspanel/sdk-source-loader": fileURLToPath(
        new URL(
          "./node_modules/@grafana/runtime/dist/esm/services/dataSource/dataSource.mjs",
          import.meta.url,
        ),
      ),
    },
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:7333", ws: true },
      "/metrics": "http://127.0.0.1:7333",
      "/prometheus": "http://127.0.0.1:7333",
      "/public/plugins": "http://127.0.0.1:7333",
    },
  },
});
