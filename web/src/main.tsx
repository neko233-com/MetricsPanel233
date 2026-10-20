import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { AppThemeProvider } from "./ui/theme";
import "./styles.css";

// @grafana/ui resolves icons lazily from this absolute root. Without it,
// icons resolve relative to routes such as /d/<uid>/<name>.
window.__grafana_public_path__ = "/";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppThemeProvider>
      <App />
    </AppThemeProvider>
  </StrictMode>,
);
