# metrics233-all agent guide

This repository pins three independently versioned public Git submodules:

- `metrics233-server`: Go monitoring server, first-party React UI, storage and collectors. Read its `AGENTS.md` and narrow `docs/agent` contracts before changing it.
- `metrics233-lib-go`: standard-library-only Go instrumentation and push SDK. Read its README and lifecycle contract before changing it.
- `metrics-lib-rust`: bounded Rust reporting SDK (crate name `metrics233`; registry publication is a separate acceptance gate); read its README and explicit lifecycle contract before changing it.

Use Simplified Chinese for collaboration. Preserve unrelated changes and stage only the requested delivery. Keep the user-owned `CODELY.md` untracked.

Run Go/npm commands inside their owning module. The aggregate root is not a Go application. Do not copy server source back to this root, and do not replace submodules with tracked source directories. Publish child commits before updating aggregate gitlinks. Verify remote commit IDs and a fresh recursive checkout.

Production hostnames, SSH configuration, control-plane addresses, environment files and credentials belong in a separately controlled private deployment repository. Never add it as an aggregate submodule. Public examples use loopback or example domains. Run `go run ./tools/check-public/main.go --root .` before public delivery, with the optional private deny-file when available. Never print matching secret values.

CI remains expensive and runs only for `v*` tags or manual dispatch. Server, SDK, Docker, browser, release CI and online deployment are separate acceptance layers. Keep `PLAN.md` and `docs/status.md` honest about the exact layer achieved.
