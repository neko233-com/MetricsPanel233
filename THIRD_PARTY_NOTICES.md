# Third-party components

The web renderer uses Grafana's public `@grafana/data`, `@grafana/runtime`, `@grafana/ui` and `@grafana/schema` packages (13.2.3), licensed under Apache-2.0.
Their source and copyright notices are distributed in the npm packages and available at <https://github.com/grafana/grafana/tree/main/packages/grafana-data>.
Grafana is a trademark of Grafana Labs. MetricsPanel233 is an independent project.

Text panels use DOMPurify (Apache-2.0 OR MPL-2.0) and Marked (MIT). React, RxJS, Vite and other dependencies retain their respective licenses.
The Prometheus query engine and exposition parser use the upstream Apache-2.0 Go modules listed in `go.mod`.

The Node Exporter Full compatibility fixture is from rfmoz/grafana-dashboards (Apache-2.0):
<https://github.com/rfmoz/grafana-dashboards/blob/master/prometheus/node-exporter-full.json>.
The pinned Git blob is `b14bc0c87591ece21ee853b114eb183a73f0ba87`. The fixture is unmodified and its original licensing remains applicable.
Other dashboard examples and tests are authored for MetricsPanel233.

The plugin compatibility fixture `internal/plugins/testdata/grafana-clock-panel-3.2.4.zip` is the unchanged official Grafana Clock 3.2.4 package:
<https://grafana.com/api/plugins/grafana-clock-panel/versions/3.2.4/download>.
Its SHA-256 is `b2dc870d732f39edf4a37fd20ad0e0fa9efa3730efff46b2a5b23213e3b0507f`.
The package contains its original MIT license and copyright notice (Grafana, 2016), signed manifest and bundled dependency notices.
Public signature verification keys are pinned from <https://grafana.com/api/plugins/ci/keys>.
The backend host uses Grafana's Apache-2.0 `grafana-plugin-sdk-go`; other Go modules retain the licenses included in their source distributions.

Grafana Live transport uses the MIT-licensed `centrifugal/centrifuge` Go server, `centrifugal/protocol` and `centrifuge` JavaScript client.
The agent subscription client uses the ISC-licensed `coder/websocket` library.
Sources and included notices: <https://github.com/centrifugal/centrifuge>, <https://github.com/centrifugal/protocol>,
<https://github.com/centrifugal/centrifuge-js> and <https://github.com/coder/websocket>.
