import { PluginType } from "@grafana/data";

export const grafanaMeta = {
  id: "grafana",
  name: "-- Grafana --",
  type: PluginType.datasource,
  module: "",
  baseUrl: "",
  builtIn: true,
  backend: true,
  metrics: true,
  annotations: true,
  info: {
    version: "13.2.3",
    author: { name: "MetricsPanel233" },
    description: "Builtin visualization and annotation queries",
    links: [],
    logos: { small: "", large: "" },
    screenshots: [],
    updated: "",
  },
};
