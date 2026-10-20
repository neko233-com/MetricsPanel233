import { createTheme, ThemeContext, type GrafanaTheme2 } from "@grafana/data";
import type { ReactNode } from "react";

// One theme instance keeps emotion class names and color tokens stable across
// re-renders. @grafana/ui hooks such as useStyles2 require a provider.
export const appTheme: GrafanaTheme2 = createTheme({ colors: { mode: "dark" } });

export function AppThemeProvider({ children }: { children: ReactNode }) {
  return (
    <ThemeContext.Provider value={appTheme}>{children}</ThemeContext.Provider>
  );
}
