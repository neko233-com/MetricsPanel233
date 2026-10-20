# Repo map

| Path | Responsibility |
| --- | --- |
| `cmd/metricspanel` | CLI, `serve`, setup/admin bootstrap, release build flags |
| `internal/server` | HTTP routes, auth wrapper integration points, query dispatch, OTLP, Loki/Tempo, security headers |
| `internal/localauth` | First-run setup, login/session use, role matrix, user administration |
| `internal/identity` | Users, credentials, browser sessions, organizations, memberships |
| `internal/instance` | Instance-scoped request and permission boundaries |
| `internal/store` | SQLite control/data plane, ClickHouse backend, retention and vectors |
| `internal/collector` | Prometheus scrape and native MySQL/Redis/PostgreSQL/service collectors |
| `internal/promcompat` | Prometheus HTTP API and PromQL adapter |
| `internal/expressions` / `internal/sqlframes` | Expression graph and SQL frame conversion |
| `internal/grafana` / `internal/plugins` | Optional import/plugin compatibility |
| `web/src/ui` | First-party product primitives |
| `web/src/render` | First-party renderer framework: registry, Table, TimeSeries, frame budget |
| `web/src/grafana` | Compatibility adapters and the optional Grafana renderer bridge |
| `web/src/features` | Product pages |
| `docs/agent` | Agent operating contract |
| `tests/integration` | Docker-backed storage/collector/browser-adjacent checks |

## Commands

```sh
# API only
go run ./cmd/metricspanel serve

# UI + API development
cd web && npm run dev:workspace

# Full browser suite (slow; local verification, not push CI)
cd web && npx playwright test

# Targeted browser smoke
cd web && node scripts/auth-smoke.mjs
cd web && node scripts/ui-smoke.mjs
cd web && node scripts/render-bench.mjs

# Linux container smoke with cleanup
pwsh -File scripts/docker-verify.ps1 -Port 17333
```

## Key generated/ignored paths

`web/dist`, `web/node_modules`, `bin/`, `data/`, `.codely-cli/`, `web/test-results/` and `web/playwright-report/`
are local artifacts. The embedded UI is built with `-tags webui`; do not hand-edit `web/dist`.
