# Collection modes: pull and push

MetricsPanel233 accepts both collection models at the same time, writing into the same metric store and sharing the
same authentication and role policy.

## Pull

Pull jobs fetch an endpoint on an interval.

```sh
metricspanel targets add --name node --url http://127.0.0.1:9100/metrics --interval 15s
metricspanel targets add --name mysql --url http://127.0.0.1:9104/metrics --interval 30s
metricspanel targets add --file examples/collectors/redis.json
```

- Prometheus text, OpenMetrics 1.0 and delimited protobuf, including classic histograms and summaries.
- Native MySQL/MariaDB, Redis, PostgreSQL, ClickHouse, Elasticsearch, Hadoop/YARN/HDFS, Hive/Kafka, Spark and Flink.
- Limits: 10,000 samples and 4 MiB per scrape, eight concurrent scrapes.
- Management: `POST /api/v1/targets`, `PUT /api/v1/targets/{id}`, `POST /api/v1/targets/{id}/scrape`.

## Push

### Prometheus remote write

`POST /api/v1/write` (alias `POST /prometheus/api/v1/write`).

```yaml
remote_write:
  - url: https://metrics.example.com/api/v1/write
    authorization:
      credentials: <workspace-token>
    queue_config:
      max_samples_per_send: 10000
```

- Body: snappy-compressed `prompb.WriteRequest` (`Content-Encoding: snappy`).
- Raw protobuf without `Content-Encoding` is accepted for simple senders.
- Limits: 32 MiB compressed, 64 MiB decoded, 100,000 samples per request, stored in 10,000-sample batches.
- `__name__` becomes the metric name and is removed from labels; series without `__name__` are skipped.
- `X-Accepted-Samples` reports the accepted count; success is `204 No Content`.
- Invalid snappy/protobuf returns `400`; unsupported encoding returns `415`; oversized bodies return `413`.
- Remote write v2 (`io.prometheus.write.v2.Request`) is not implemented; use the v1 endpoint.

### OTLP/HTTP

`POST /v1/metrics` (alias `/otlp/v1/metrics`), protobuf or JSON, gzip supported. See the OTLP section in the README for
resource/scope label mapping and histogram expansion. Limits: 16 MiB and 10,000 samples per batch.

### Native JSON

`POST /api/v1/ingest`:

```json
{"samples":[{"name":"business_orders","labels":{"service":"api"},"value":233,"timestamp":1730000000000}]}
```

Timestamps are Unix milliseconds; `0` uses server time. This is the simplest path for scripts and the agent CLI.

## Roles

Remote write, OTLP and native JSON are Editor operations. Viewer credentials receive `403`; Admin is also allowed.
All three routes require the same authentication as the rest of the API, including the workspace token on non-loopback
listeners and local-account sessions/Basic auth in `--auth=local` mode.

## Verification

```sh
go test ./internal/server -run RemoteWrite -count=1
go test ./internal/localauth -count=1
```

The receiver tests cover snappy and raw protobuf bodies on both paths, malformed snappy/protobuf, unsupported
encodings, empty `__name__` series and stored sample values. The role tests cover Viewer denial and Editor access for
both write paths.
