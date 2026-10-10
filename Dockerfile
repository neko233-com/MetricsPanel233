ARG TEST_CLICKHOUSE_IMAGE=clickhouse/clickhouse-server:26.8-alpine
ARG TEST_EXPORTER_IMAGE=prom/mysqld-exporter:v0.20.0
FROM node:24.14.0-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.0-bookworm AS backend
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod for attempt in 1 2 3; do go mod download && break; if [ "$attempt" = 3 ]; then exit 1; fi; done
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -tags webui -trimpath -ldflags="-s -w" -o /out/metricspanel ./cmd/metricspanel && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/go-service ./examples/go-service && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/metrics-fixture ./tests/docker

FROM alpine:3.23 AS runtime-base
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN addgroup -g 10001 metricspanel && adduser -D -u 10001 -G metricspanel metricspanel && mkdir /data && chown metricspanel:metricspanel /data

FROM runtime-base AS example
COPY --from=backend /out/go-service /usr/local/bin/go-service
USER metricspanel
EXPOSE 8080
ENTRYPOINT ["go-service"]

FROM runtime-base AS runtime
COPY --from=backend /out/metricspanel /usr/local/bin/metricspanel
USER metricspanel
EXPOSE 7333
VOLUME /data
ENTRYPOINT ["metricspanel"]
CMD ["serve","--listen","0.0.0.0:7333","--db","/data/control.db"]
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s CMD metricspanel health || exit 1

# All integration cases use one container with runtime binaries only.
FROM ${TEST_EXPORTER_IMAGE} AS test-exporter
FROM ${TEST_CLICKHOUSE_IMAGE} AS integration
USER root
RUN apk add --no-cache mariadb mariadb-client redis postgresql postgresql-client su-exec tini && \
    mkdir -p /run/metricspanel-test /run/mysqld /run/postgresql && \
    rm -rf /usr/share/doc /usr/share/man /usr/share/mariadb/test
COPY --from=backend /out/metricspanel /out/go-service /out/metrics-fixture /usr/local/bin/
COPY --from=test-exporter /bin/mysqld_exporter /usr/local/bin/mysqld_exporter
COPY tests/docker/testctl.sh /usr/local/bin/testctl
COPY tests/docker/clickhouse-test.xml /etc/clickhouse-server/config.d/metricspanel-test.xml
COPY tests/docker/clickhouse-users.xml /etc/clickhouse-server/users.d/metricspanel-test.xml
COPY tests/docker/exporter-single.cnf /etc/test-exporter.cnf
RUN chmod 755 /usr/local/bin/testctl && chmod 600 /etc/test-exporter.cnf
EXPOSE 7333 7334 8123
ENTRYPOINT ["tini","--","testctl"]
CMD ["serve"]
HEALTHCHECK --interval=2s --timeout=5s --start-period=20s --retries=60 CMD testctl health
