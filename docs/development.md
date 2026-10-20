# Development

Product scope and the Grafana dependency boundary are defined in the [project goals](project-goals.md).

## Requirements

- Go 1.27
- Node.js 24 and npm
- Docker Desktop or Docker Engine for the Docker-backed integration suite

Install the web dependencies once:

```sh
cd web
npm ci
```

## Fast local loop

From the web directory, run:

```sh
npm run dev:workspace
```

The command starts go run ./cmd/metricspanel serve and Vite together. Open the UI at http://127.0.0.1:5173; the API listens on http://127.0.0.1:7333. Vite proxies API, metrics, Prometheus and plugin asset requests to the Go service. React and CSS edits use HMR without rebuilding the embedded bundle. Press Ctrl+C to stop both processes.

For a frontend-only server, use npm run dev; it expects the Go API to be running separately. The production executable embeds the frontend, so rebuild the web bundle before a webui-tagged Go build.

## Validation levels

Start with the smallest check covering the changed behavior:

```sh
# Frontend unit tests and type-check/build
cd web
npm run test:unit
npm run build

# Go unit and package tests
cd ..
go test ./... -count=1
go vet ./...
```

The full CI workflow additionally runs Go race tests, a production webui binary build, Playwright browser tests and Docker integration tests twice. Run Docker integration when the change affects real database/exporter behavior or container orchestration:

```sh
go test -tags=integration ./tests/integration -count=2 -v -timeout=25m
```

Playwright tests need a production frontend bundle and CLI binary first; see the [CI workflow](../.github/workflows/ci.yml) and the root README's automated verification section for the complete setup.

For focused browser checks after a webui build:

```sh
cd web
node scripts/ui-smoke.mjs     # legacy token mode: shell, theme, modal, icon assets
node scripts/auth-smoke.mjs   # local accounts: setup, roles, 403, session, logout
```

`scripts/auth-smoke.mjs` starts the server with `--auth=local` in a temporary directory, reads the one-time setup token from stdout and verifies the full browser flow.

## Linux container verification

```powershell
pwsh -File scripts/docker-verify.ps1 -Port 17333
```

The script cross-compiles the Linux binary, builds a minimal Alpine runtime image (about 42 MiB), starts it with
`--auth=local` and SQLite, verifies first-run setup, login, roles, a Viewer 403 and `metricspanel health`, then removes
the container, volume, image, build cache, `web/test-results` and its temporary directory in a `finally` block.
The image is checked against a 1 GiB budget before it starts. It does not touch unrelated Docker resources.

## Change workflow

1. Identify the public route, CLI command or UI action and trace it to the owning package.
2. Check existing tests and focused contract pages before changing persistence, security or Grafana compatibility behavior.
3. Make the smallest change that preserves existing data and API compatibility.
4. Add or update focused tests when behavior changes, and update docs when public behavior or a compatibility claim changes.
5. Run the relevant validation level and report exactly what it covers.

Product UI builds on the shared `@grafana/ui` adapter in `web/src/ui`; import components from there rather than
adding new hand-written primitives. Grafana plugin hosting and resource conversion stay behind the compatibility
boundary described in the project goals.

## Release binaries

`serve` is the supported single-node deployment path. Build self-contained archives for the common targets with:

```powershell
pwsh -File scripts/build-release.ps1 -Version 0.1.1
```

```sh
VERSION=0.1.1 ./scripts/build-release.sh
```

Both scripts build the frontend once, then cross-compile with `CGO_ENABLED=0` and the `webui` tag. Output goes to
`dist/`. Docker is not required for the release path.

Do not commit local databases, encryption keys, .env files, plugin packages, generated web/dist assets or compiled binaries.
