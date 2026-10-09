# Third-party components

The web renderer uses Grafana's public `@grafana/data` and `@grafana/schema` packages (13.2.3), licensed under Apache-2.0.
Their source and copyright notices are distributed in the npm packages and available at <https://github.com/grafana/grafana/tree/main/packages/grafana-data>.
Grafana is a trademark of Grafana Labs. MetricsPanel233 is an independent project.

Text panels use DOMPurify (Apache-2.0 OR MPL-2.0) and Marked (MIT). React, RxJS, Vite and other dependencies retain their respective licenses.
The Prometheus query engine and exposition parser use the upstream Apache-2.0 Go modules listed in `go.mod`.

The Node Exporter Full compatibility fixture is from rfmoz/grafana-dashboards (Apache-2.0):
<https://github.com/rfmoz/grafana-dashboards/blob/master/prometheus/node-exporter-full.json>.
The pinned Git blob is `b14bc0c87591ece21ee853b114eb183a73f0ba87`. The fixture is unmodified and its original licensing remains applicable.
Other dashboard examples and tests are authored for MetricsPanel233.
