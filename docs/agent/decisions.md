# Decisions

## 2026-10-20 — final product target

The product is an out-of-the-box single-node monitoring appliance: **collection + time-series/vector storage +
dashboards**. It replaces the usual Grafana + separate scraper + separate vector database stack for small teams.
It is not a Grafana clone and does not require a Grafana server, Grafana UI or plugin ecosystem.

## 2026-10-20 — UI ownership

Product shell, forms, dialogs and navigation use `web/src/ui`. Grafana packages are confined to optional
import/plugin compatibility. The product navigation no longer advertises Grafana plugin management.

## 2026-10-20 — renderer migration is staged

The first-party renderer framework is in `web/src/render`:

- Own Table is the default and passes the table browser contracts.
- Own TimeSeries passed the time-series contract in isolation but still has edge cases in workspace time-range
  interactions. It is opt-in (`?renderer=own` or localStorage), while the compatibility bridge remains default.
- Do not flip the default until the full browser suite is green with the own renderer.
- `frame-budget.ts` and `incremental.ts` exist for cooperative slicing and model reuse; Worker decimation is next.

## 2026-10-20 — no push-triggered CI

The full validation job is expensive. It runs only for `v*` tags or manual dispatch. Local verification is the
normal loop; tag releases run the full race/browser/Docker matrix.

## 2026-10-20 — collector scope

Prioritize the game-backend set: MySQL/MariaDB, Redis, Linux/Prometheus exporters and business-service exports.
Loki/Tempo adapters exist but are not the primary product target until the core dashboard loop is complete.
