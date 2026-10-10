import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [react()],
  resolve: {
    // Core boot exports are omitted from the published SDK package exports.
    // These two host-only aliases track the pinned 13.2.3 SDK, not plugin imports.
    alias: {
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
