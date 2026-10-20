# SQLite single-node baseline

This page records one reproducible baseline for the default zero-configuration backend. It is a bounded measurement,
not a production guarantee. Re-run it on the hardware and workload you intend to operate.

## Reference machine

| Item | Value |
| --- | --- |
| CPU | AMD Ryzen 9 9950X (16 cores / 32 threads) |
| OS | Windows (amd64) |
| Go | 1.27 |
| Storage | SQLite WAL, default page settings |
| Build | `go test` benchmark, no race detector |

Record your own `goos`, `goarch`, CPU and Go version from the benchmark header. Disk type (NVMe vs. network share)
matters more than CPU for sustained ingestion.

## Commands

```sh
# 100,000 samples (20 services) plus a 24-hour, 60-second aggregation
go test ./internal/store -run TestBaselineIngestAndAggregate100k -count=1 -v

# Steady-state timings; -benchtime=1x keeps the run short
go test ./internal/store -run "^$" \
  -bench "BenchmarkSQLiteAggregate24Hours|BenchmarkSQLitePatternSearch10k" \
  -benchtime=1x -count=1
```

The benchmark code lives in `internal/store/sqlite_bench_test.go`. Each benchmark opens a temporary database, so it
does not touch `data/metrics.db`.

## Observed result

Run on the reference machine above:

| Workload | Result |
| --- | --- |
| Ingest 100,000 samples in 1,000-sample batches | 0.43 s (about 233k samples/s) |
| 24-hour aggregation, 60-second buckets (1,440 points) | 92 ms |
| Exact pattern search over 10,000 saved 64-dimension windows, top 10 | 6.8 ms |

These numbers cover a warm process and a local disk. They exclude HTTP overhead, PromQL evaluation, concurrent
writers, retention pruning and cold-start I/O.

## Vector search behavior

- SQLite always performs an exact scan: it loads candidate payloads, computes L2 distance and keeps a bounded top-K
  heap. There is no approximate index.
- Candidates are filtered by normalization, aggregation and optional metric/label equality before scanning.
- A single search is capped at 50,000 candidate patterns. Above that the request fails with an explicit error instead
  of silently returning partial results; add a metric or label filter, or use ClickHouse with its HNSW index.
- ClickHouse uses approximate HNSW search and supports `--exact`; SQLite has no approximate mode to disable.

## Sizing guidance

Use these as starting points for small teams, then measure your own workload:

| Deployment | Recommendation |
| --- | --- |
| Up to ~100 collectors, 30-day retention | SQLite on local SSD is sufficient; keep the database and `secrets.key` on the same fast volume |
| Up to ~1M active series, bursty writes | Measure first; consider ClickHouse when sustained write rate or disk growth becomes the bottleneck |
| More than 50,000 saved patterns | Use ClickHouse for HNSW, or filter pattern searches by metric/label |
| Multiple concurrent dashboard users | SQLite serializes writes; reads stay concurrent, but heavy ingest plus large queries compete for one disk |

Back up the SQLite control database, its adjacent `secrets.key`, and the plugin directory together. See the
[storage section](../README.md#storage-and-performance) for operation details.
