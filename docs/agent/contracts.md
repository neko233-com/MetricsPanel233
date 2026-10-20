# Contracts

## Product boundary

One binary, one workspace, one SQLite control database. The product is collection + storage + dashboard, not a
distributed multi-tenant platform. Grafana import/export is optional migration support.

## Authentication

- `--auth=token` (default): single workspace Bearer token; non-loopback listeners require at least 16 characters.
- `--auth=local`: users, passwords and browser sessions; first run prints a one-time setup token.
- Roles are workspace-wide: Viewer reads, Editor mutates monitoring resources, Admin controls users, datasources,
  plugins and notification configuration. Unknown mutations fail closed to Admin.
- Session cookie: HttpOnly, SameSite=Lax, Secure under HTTPS, rotates every 24 hours.
- `--trusted-proxies` only trusts `X-Forwarded-For` from the listed CIDRs for rate limiting.
- Public endpoints: `/api/health` (minimal), static SPA assets, login/setup. Everything else requires auth.

## Collection

- Prometheus/OpenMetrics scrape with 10,000 samples / 4 MiB per scrape, eight concurrent scrapes.
- OTLP/HTTP `POST /v1/metrics` (alias `/otlp/v1/metrics`), protobuf or JSON, gzip, 16 MiB / 10,000 samples.
  Resource attributes become labels; histograms expand to `_bucket`/`_count`/`_sum`.
- Native MySQL/MariaDB, Redis, PostgreSQL and supported service/database metrics APIs.
- Collector secrets are encrypted; responses expose presence flags only.

## Storage and vectors

- SQLite WAL is the zero-configuration default for control data and metrics.
- ClickHouse is optional for high-volume metrics and HNSW vector search.
- SQLite vector search is exact with a 50,000 candidate cap; ClickHouse uses approximate HNSW.
- Retention is a configuration boundary; changing ClickHouse TTL requires an explicit `ALTER TABLE`.

## Renderers

- Product primitives live in `web/src/ui`; product renderers in `web/src/render`.
- Table and TimeSeries are the default first-party renderers.
- TimeSeries covers lines, comparison, legend, tooltip, zoom, log axes, percent stacking, multiple axes and
  annotations. Use `?renderer=compat` or localStorage `metricspanel-renderer=compat` to restore the compatibility
  bridge for imported-resource edge cases and troubleshooting.
- Stat, gauge and bar gauge are first-party registry entries sharing `StatRenderer`; text/Markdown remains a
  product-owned HTML path in the panel host.
- The Grafana bridge is allowed only for imported resources and plugins under `web/src/grafana`.

## Deployment

Bind loopback behind an HTTPS reverse proxy. Set `--root-url` to the public HTTPS origin. Back up the control
database, `identity.db`, `secrets.key` and `plugins/` together. Do not expose the HTTP port directly to the internet.
See `docs/public-deployment.md`.
