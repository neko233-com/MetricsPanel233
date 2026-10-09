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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -tags webui -trimpath -ldflags="-s -w" -o /out/metricspanel ./cmd/metricspanel && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/go-service ./examples/go-service

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
