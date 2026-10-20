# Agent guide

This directory is the working contract for coding agents. Read the narrowest page first; source and executable tests
remain authoritative.

## Read order

1. [Repo map](repo-map.md) — where each behavior lives.
2. [Contracts](contracts.md) — product scope, auth, collection, storage and renderer boundaries.
3. [Verification](verification.md) — which checks to run and when.
4. [Decisions](decisions.md) — why the product is not a Grafana clone and why renderers are staged.

## Working rules

- Keep changes scoped; preserve unrelated working-tree changes.
- Product UI uses `web/src/ui`; product renderers use `web/src/render`. Do not add `@grafana/ui` imports to product
  features or pages. Grafana packages remain in the optional `web/src/grafana` import/plugin layer.
- Never commit local databases, keys, `bin/`, `web/dist/`, `node_modules/` or `.codely-cli/`.
- Keep credentials out of source, fixtures, logs, docs and command output.
- Record public behavior changes in `docs/` and `PLAN.md`.
- Full CI runs only for `v*` tags or manual dispatch. Do not add push/PR triggers for the expensive suite.

## Target scope

The product is a single-node appliance for small teams:

- Collect Prometheus/OpenMetrics, OTLP/HTTP, MySQL/MariaDB, Redis, PostgreSQL and common service exports.
- Store time-series samples plus local metric-window vectors.
- Provide dashboards, Explore, alerts, collectors and user administration in one first-party UI and agent CLI.

The renderer target is the common game-backend monitoring set: time series, table/stat/gauge, MySQL/Redis/Linux and
business exporter metrics. Full Grafana visualization parity is not a requirement.
