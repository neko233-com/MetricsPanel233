# Documentation

This folder keeps cross-cutting architecture and product contracts. The root [README](../README.md) is the English project entry point; [README-CN.md](../README-CN.md) is its Chinese translation.

## Product direction

- [Project goals](project-goals.md) — target users, scope, performance evidence and Grafana SDK boundaries.

## Development and architecture

- [Architecture overview](architecture.md) — runtime flow, source ownership and generated frontend assets.
- [Development guide](development.md) — prerequisites, the one-command local loop, release builds and validation levels.
- [SQLite baseline](performance-baseline.md) — reference hardware, reproducible commands, timings and sizing guidance.
- [Public deployment](public-deployment.md) — TLS reverse proxy, trusted proxies, firewall, backups and leakage checks.

## Grafana compatibility

- [Compatibility matrix](grafana-compatibility.md) — supported and missing Grafana 13.2.3 OSS surfaces.
- [Grafana compatibility inventory](grafana-product-parity.md) — selected interoperability behavior and optional gaps; not a product roadmap.
- [Identity design](grafana-identity-design.md) and [identity HTTP contract](grafana-identity-http-contract.md) — identity boundaries and endpoint behavior.
- [Resource ACL contract](grafana-resource-acl-contract.md) — resource authorization scope and current limitations.
- [Receiver design](grafana-receivers-design.md) — alert receiver behavior and compatibility notes.

When a substantial subsystem gains a stable cross-package contract, give it a focused page and add it here. Keep executable behavior in source and tests; use documentation for ownership, invariants, public contracts and known gaps.
