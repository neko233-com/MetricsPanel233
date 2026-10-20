# Verification

Run the narrowest check that covers the change; do not infer browser or Docker acceptance from a unit test.

## Fast loop

```sh
cd web
npm run test:unit
npm run build
cd ..
go vet ./...
go test ./internal/localauth ./internal/server ./internal/store ./cmd/metricspanel -count=1
```

Targeted browser checks after a webui build:

```sh
cd web
node scripts/auth-smoke.mjs
node scripts/ui-smoke.mjs
node scripts/render-bench.mjs
```

## Full browser suite

```sh
cd web
npx playwright test
```

The suite is intentionally not triggered by every push. It covers dashboards, variables, alerts, plugins, OTLP/
collector forms, Import, Table and TimeSeries contracts, plus responsive containment at 390/768/1280 widths and
dynamic panel height. Fix persistent failures; a single retry is configured for known long-dialog timing sensitivity.

## Docker Linux

```sh
pwsh -File scripts/docker-verify.ps1 -Port 17333
```

The script builds a small Linux runtime image, verifies first-run setup/login/roles/health against the container,
then removes its container, volume, image, build cache, test artifacts and temporary directory. Keep the local
increment under 1 GiB.

## Release CI

`.github/workflows/ci.yml` runs only on `v*` tags or manual dispatch. Push/PR events do not start the expensive race,
browser and Docker jobs. Tag releases still run the full matrix.

## Cleanup expectations

- `web/test-results/` and `web/playwright-report/` are local only.
- `bin/`, `web/dist/`, `data/`, `.env`, `*.db`, `identity.db` and `.codely-cli/` stay out of git.
- After Docker verification, labeled containers/volumes/images must be zero.
