# Project status and progress protocol

Updated: 2026-10-20. This file is the durable progress ledger. Update it whenever a progress unit completes.

## Reporting protocol (permanent)

After each completed progress unit, report in this order:

1. **目标 (Goal)** — the outcome this unit was meant to deliver.
2. **做了什么 (Done)** — commits, files, behavior changes and evidence.
3. **接下做什么 (Next)** — ordered next units, each with an acceptance check.
4. **总进度 (Overall %)** — weighted overall completion plus the per-workstream table below.
5. **风险 / 阻塞 (Risks / blockers)** — what could invalidate the claim.
6. **验证 (Verification)** — commands actually run and their results.

Rules:

- Update the ledger and percentage in the same change as the work.
- Never report a unit as done without executable evidence; distinguish local, browser, Docker and CI evidence.
- Percentages are progress estimates, not release promises. State the weighting basis.
- Keep the report in the user's language (Chinese in this project).

## Completion ledger

Weights reflect product risk and user-visible value.

| Workstream | Weight | Progress | Basis and remaining work |
| --- | --- | --- | --- |
| Product shell and first-party UI | 15% | 70% | `web/src/ui` primitives, bilingual pages and forms exist; the left-navigation + right-content shell redesign and ReactBits motion layer are not started |
| Renderers | 20% | 50% | Table, TimeSeries, Stat, Gauge and BarGauge are first-party registry entries; Logs, Traces, Heatmap and Vector remain; Worker decimation remains |
| Collection | 15% | 80% | Prometheus/OpenMetrics, OTLP/HTTP, MySQL, Redis, PostgreSQL and service exports work; broader exporter coverage and edge-case conformance remain |
| Storage and query | 15% | 75% | SQLite default, ClickHouse optional, retention and 64-dimension vectors; rollups, block min/max and large-range planning remain |
| Auth and security | 15% | 85% | Local accounts, Setup token, sessions, roles, rate limiting, headers, public-deployment guide; periodic security review remains |
| Ops and delivery | 10% | 80% | Single binary, Docker verification, cleanup scripts, tag-only CI with a green full run; release checklist and upgrade notes remain |
| Docs and agent contract | 10% | 85% | Product goals, architecture, renderer matrix, agent guide and external skill references; keep in sync with each behavior change |
| Performance | 10% | 40% | Frame budget and incremental model reuse exist; LTTB/min-max downsampling, Worker transfer and large-range benchmarks remain |

**Weighted overall: 67%** (sum of weight × progress). Treat this as ±5 points until the renderer matrix and
downsampling land.

## Definition of done by workstream

### Renderers

- Each kind has a registry entry, a typed contract and a browser test that asserts real paint or DOM output.
- Product pages never import `@grafana/ui`; the compatibility bridge remains only for imported resources, plugins and
  the explicit `?renderer=compat` fallback.
- Responsive containment (390/768/1280), dynamic panel size and empty/error states are covered.

### Performance

- A reproducible benchmark reports first-frame time, draw time, decimation ratio and heap for a fixed large frame.
- Downsampling preserves peaks and gaps; zooming re-queries instead of silently discarding detail.
- Budgets are recorded in `docs/performance-baseline.md`; no universal latency claim without measurements.

### Product shell

- Collapsible left navigation, right content region, keyboard focus order and mobile collapse work at 390/768/1280.
- Routing, permissions and deep links are unchanged; the shell uses `web/src/ui` primitives.
- ReactBits, if introduced, is limited to decorative motion and respects reduced-motion preferences.

### Collection, storage and security

- New collectors and query paths ship with protocol tests and a failure-mode test (timeout, auth, malformed input).
- ClickHouse-only behavior states its SQLite fallback or limitation.
- Security-relevant changes update `docs/public-deployment.md` and the Admin/Editor/Viewer matrix.

## Recent delivered units

| Date | Unit | Evidence |
| --- | --- | --- |
| 2026-10-20 | Agent contract, tag-only CI, external skill reference | `docs/agent/*`; CI run 39436800402 green for `a51c275` |
| 2026-10-20 | First-party TimeSeries default with compat fallback | Full browser suite 68 passed / 2 skipped; `?renderer=compat` fallback |
| 2026-10-20 | Responsive acceptance for charts and tables | 390/768/1280 width tests, dynamic panel height, narrow table containment |
| 2026-10-20 | Stat/Gauge/BarGauge renderer registry entries | `StatRenderer` shared reducer/empty states; browser test asserts stat text, meter ARIA values, bar fraction and no page overflow |

## Next units (ordered)

1. **Logs renderer** — virtualized Loki frames with label filtering and search. Acceptance: 10k-row frame stays
   interactive, empty/error states covered.
2. **Traces renderer** — Tempo search table first, trace detail second. Acceptance: search, duration/status columns
   and deep-link parameters.
3. **Worker downsampling** — LTTB or min/max with transferables; acceptance: benchmark at 100k points and visual
   peak preservation.
4. **Shell redesign** — collapsible left navigation and right content, preserving routing, permissions and the
   responsive suite. Acceptance: 390/768/1280, keyboard-only navigation, no layout overflow.
5. **Vite+ evaluation** — isolated comparison of build, lint, test and CI cost. Acceptance: documented decision, no
   migration until parity is proven.
