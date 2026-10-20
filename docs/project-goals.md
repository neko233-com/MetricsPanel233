# Project Goals

## Product statement

Build a zero-configuration, single-node monitoring appliance for small teams: **collection, storage and dashboards in
one service**. The product replaces the usual Grafana + separate scraping + separate vector-search stack; it does not
require a Grafana server, Grafana UI, plugin ecosystem or external vector database.

The target workflows are:

1. **Collection** — Prometheus/OpenMetrics scraping, OTLP/HTTP metrics, and native MySQL, Redis, PostgreSQL and common
   service/database collectors.
2. **Storage** — durable time-series samples plus local 64-dimensional metric-window vectors with similarity search.
3. **Product surface** — dashboards, Explore, alerts, collectors, vector search and user administration in one
   first-party UI and agent CLI.

Grafana import/export remains an optional migration adapter. Full Grafana parity, Grafana's plugin ecosystem and
Grafana's UI architecture are not product goals.

## Product scope

### Core goals

- Collect Prometheus text and OpenMetrics metrics, scrape compatible exporters, accept OTLP/HTTP metrics and business
  metric writes.
- Connect directly to MySQL/MariaDB, Redis, PostgreSQL and the supported service/database metrics APIs without
  installing a separate exporter.
- Store and retain time-series data plus local metric-window vectors on a single service, with simple local operation
  and a higher-volume ClickHouse option.
- Query metrics and build useful dashboards, Explore views, alerts and vector searches through one first-party
  interface and agent CLI.
- Make the normal path easy to run and operate on one machine; keep local development fast and reproducible.
- Support local accounts with Admin, Editor and Viewer roles for small teams, without requiring an external identity
  provider or multi-tenant control plane.
- Measure performance using repeatable workloads before making throughput, latency, memory or scale claims.

### Compatibility goals

- Support Prometheus protocols, exporters and PromQL behavior where they serve the core product.
- Import or export selected Grafana dashboard formats when that materially helps migration.
- Keep any retained Grafana SDK, plugin or resource compatibility behind replaceable adapters.
- Implement other Grafana behaviors only when they support a concrete migration or core monitoring workflow.

### Out of scope by default

- Reproducing every Grafana OSS screen, HTTP route, plugin API and administration workflow.
- Enterprise Grafana behavior.
- A horizontally scaled, multi-tenant or highly available control plane.
- Compatibility work that does not improve the single-node Prometheus dashboard and storage experience.

These are default boundaries, not a restriction on future product decisions. A broader scope needs an explicit goal update.

## Authentication and roles

Two authentication modes share the same HTTP API and storage:

- `--auth=token` (default) keeps the original single-workspace Bearer token. Loopback without a token remains
  unauthenticated; any non-loopback listener requires a token of at least 16 characters.
- `--auth=local` adds users, passwords and browser sessions. It is the recommended mode for a team and can still accept
  a configured Bearer token for CLI automation.

Local accounts live in a separate SQLite identity catalog (`identity.db` beside the control database), never in the
metric store. The first start prints a one-time setup token; creating the administrator invalidates it. Session cookies
are HttpOnly and SameSite=Lax, become Secure when `--root-url` is HTTPS, rotate every 24 hours, and login/setup attempts
are rate-limited per peer address. Passwords are hashed by the existing identity catalog; plaintext is never logged or
returned.

The role matrix is global to the single workspace: Viewer reads, Editor mutates dashboards, folders, library panels,
annotations, alerts, collectors, patterns and silences, and Admin additionally controls plugins, datasources, contact
points, notification policy, mute timings and user accounts. Unknown mutations fail closed to Admin. Resource-level
ACLs and password-reset/self-service profile flows remain future work; they are not implied by the current roles.

## UI and dependency policy

The product surface, navigation and workflows are owned by MetricsPanel233. **Product pages use the first-party
primitives in `web/src/ui`** (buttons, inputs, fields, selects, alerts, badges, modals, spinner) and the project's own
layout/CSS. Product features must not import `@grafana/ui` directly. Grafana UI packages are confined to the optional
import/plugin compatibility layer under `web/src/grafana`.

- `web/src/ui/index.tsx` owns the product primitives and their dark theme tokens.
- Charts and tables are product components in `web/src/render`. The default time-series and table renderers are own
  uPlot/HTML implementations; imported Grafana resources may still use compatibility adapters behind the
  `web/src/grafana` boundary.
- Grafana plugin hosting, import/export and model conversion stay behind the compatibility boundary. They are optional
  adapters, not the product architecture.
- The product navigation no longer exposes Grafana plugin management. Grafana pages and SDK surfaces are candidates
  for removal, not for expansion.

### Current migration state

The login/setup pages, common dialog, user management, dashboards list, Getting Started and Table use first-party
implementations. The first-party uPlot TimeSeries renderer is available with `?renderer=own`/localStorage while the
compatibility bridge remains the default until log axes, percent stacking and multi-axis parity are complete. The
Grafana theme provider remains only for compatibility modules. Remaining work: finish TimeSeries parity, then
Stat/Gauge/Logs/Traces/Vector and removal of unused Grafana compatibility pages from the default bundle.

## Performance evidence

“High performance” must be demonstrated with a reproducible workload. Record at least:

- Hardware, operating system, Go and database versions, storage mode and retention settings.
- Sample count, active-series cardinality, label shape, scrape/write rate and query concurrency.
- Ingestion throughput, query latency percentiles, peak memory, CPU use and disk growth.
- Cold-start and warm-query behavior, plus persistence after the restart scenarios relevant to the change.

Keep the workload and measurement commands with the benchmark or integration suite. Do not turn a bounded test result into a production-scale guarantee. The project has not established universal latency or throughput thresholds yet; set those only after collecting a baseline on a declared reference machine.

## Acceptance boundary

A feature is complete for this project when it advances the single-node collection + storage + dashboard workflow, is
implemented in the first-party product surface, preserves the documented storage/API behavior, and has evidence at the
relevant level. Grafana compatibility evidence is required only for the specific import/export adapter being changed;
it does not imply full Grafana parity or a Grafana UI dependency.
