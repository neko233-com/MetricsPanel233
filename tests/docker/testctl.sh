#!/bin/sh
set -eu

start() {
  service="$1"; shift
  "$@" >"/data/$service.log" 2>&1 &
  echo "$!" >"/run/metricspanel-test/$service.pid"
}
start_service() {
  case "$1" in
    clickhouse) start clickhouse su-exec clickhouse clickhouse-server --config-file=/etc/clickhouse-server/config.xml ;;
    mysql) start mysql mariadbd --user=mysql --datadir=/data/mysql --skip-networking=0 --bind-address=127.0.0.1 --port=3306 --socket=/run/mysqld/mysqld.sock --max-connections=30 --innodb-buffer-pool-size=32M ;;
    redis) start redis redis-server --bind 127.0.0.1 --port 6379 --requirepass integration-cache-password --save '' --appendonly no ;;
    postgres) start postgres su-exec postgres postgres -D /data/postgres -c listen_addresses=127.0.0.1 -c shared_buffers=16MB -c max_connections=20 ;;
    mysql-exporter) start mysql-exporter mysqld_exporter --config.my-cnf=/etc/test-exporter.cnf --web.listen-address=127.0.0.1:9104 ;;
    sqlite) start sqlite env METRICSPANEL_STORAGE=sqlite metricspanel serve --listen 0.0.0.0:7333 --db /data/sqlite/control.db ;;
    clickhouse-app) start clickhouse-app env METRICSPANEL_STORAGE=clickhouse CLICKHOUSE_URL=http://127.0.0.1:8123 CLICKHOUSE_USER=metricspanel CLICKHOUSE_PASSWORD=integration-only-password metricspanel serve --listen 0.0.0.0:7334 --db /data/control/control.db ;;
    go-sqlite) start go-sqlite env GO_SERVICE_LISTEN=127.0.0.1:8080 METRICSPANEL_URL=http://127.0.0.1:7333 go-service ;;
    go-clickhouse) start go-clickhouse env GO_SERVICE_LISTEN=127.0.0.1:8081 METRICSPANEL_URL=http://127.0.0.1:7334 go-service ;;
    fixtures) start fixtures metrics-fixture ;;
    *) echo "Unknown service: $1" >&2; exit 1 ;;
  esac
}
stop_service() {
  service="$1"
  if [ -f "/run/metricspanel-test/$service.pid" ]; then
    pid=$(cat "/run/metricspanel-test/$service.pid")
    kill "$pid" 2>/dev/null || true
    for attempt in $(seq 1 100); do
      if ! kill -0 "$pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
    rm -f "/run/metricspanel-test/$service.pid"
  fi
}
wait_http() {
  for attempt in $(seq 1 120); do
    if wget -qO /dev/null "$1"; then return; fi
    sleep 0.5
  done
  echo "Service did not start: $1" >&2; exit 1
}
case "${1:-serve}" in
  restart)
    service="$2"
    touch "/run/metricspanel-test/restarting-$service"
    stop_service "$service"
    start_service "$service"
    rm -f "/run/metricspanel-test/restarting-$service"
    ;;
  exec)
    service="$2"; shift 2
    case "$service" in
      sqlite) export METRICSPANEL_URL=http://127.0.0.1:7333 ;;
      clickhouse-app) export METRICSPANEL_URL=http://127.0.0.1:7334 ;;
      clickhouse) ;;
      *) echo "Unknown execution scope" >&2; exit 1 ;;
    esac
    exec "$@"
    ;;
  health)
    METRICSPANEL_URL=http://127.0.0.1:7333 metricspanel health >/dev/null
    METRICSPANEL_URL=http://127.0.0.1:7334 metricspanel health >/dev/null
    MYSQL_PWD=integration-exporter-password mariadb --protocol=TCP --connect-timeout=2 --host=127.0.0.1 --user=exporter --batch --execute='SELECT 1' >/dev/null
    for service in clickhouse mysql redis postgres mysql-exporter sqlite clickhouse-app go-sqlite go-clickhouse fixtures; do
      kill -0 "$(cat "/run/metricspanel-test/$service.pid")"
    done
    ;;
  serve)
    mkdir -p /run/metricspanel-test /run/mysqld /run/postgresql /data/sqlite /data/control /data/clickhouse /data/mysql /data/postgres
    chown clickhouse:clickhouse /data/clickhouse
    chown mysql:mysql /data/mysql /run/mysqld
    chown postgres:postgres /data/postgres /run/postgresql
    cleanup() {
      trap - EXIT TERM INT
      for service in go-sqlite go-clickhouse fixtures mysql-exporter sqlite clickhouse-app redis postgres mysql clickhouse; do stop_service "$service"; done
      wait || true
    }
    trap cleanup EXIT
    trap 'cleanup; exit 0' TERM INT
    if [ ! -d /data/mysql/mysql ]; then
      mariadb-install-db --user=mysql --datadir=/data/mysql --auth-root-authentication-method=normal --skip-test-db >/data/mysql-init.log 2>&1
    fi
    if [ ! -f /data/postgres/PG_VERSION ]; then
      su-exec postgres initdb -D /data/postgres --auth-local=trust --auth-host=scram-sha-256 >/data/postgres-init.log
    fi
    start_service mysql
    for attempt in $(seq 1 120); do
      if mariadb-admin --socket=/run/mysqld/mysqld.sock ping --silent; then break; fi
      sleep 0.5
    done
    mariadb --socket=/run/mysqld/mysqld.sock <<'SQL'
CREATE DATABASE IF NOT EXISTS business;
CREATE USER IF NOT EXISTS 'exporter'@'127.0.0.1' IDENTIFIED BY 'integration-exporter-password';
GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO 'exporter'@'127.0.0.1';
SQL
    start_service postgres
    for attempt in $(seq 1 120); do
      if su-exec postgres pg_isready -q; then break; fi
      sleep 0.5
    done
    su-exec postgres psql -v ON_ERROR_STOP=1 -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='metrics') THEN CREATE ROLE metrics LOGIN PASSWORD 'integration-postgres-password'; END IF; END \$\$;" >/dev/null
    start_service clickhouse
    start_service redis
    start_service mysql-exporter
    wait_http http://127.0.0.1:8123/ping
    start_service sqlite
    start_service clickhouse-app
    start_service go-sqlite
    start_service go-clickhouse
    start_service fixtures
    while :; do
      for service in clickhouse mysql redis postgres mysql-exporter sqlite clickhouse-app go-sqlite go-clickhouse fixtures; do
        if [ -f "/run/metricspanel-test/restarting-$service" ]; then continue; fi
        if ! kill -0 "$(cat "/run/metricspanel-test/$service.pid")" 2>/dev/null; then
          echo "Test service exited: $service" >&2
          tail -n 40 "/data/$service.log" >&2
          if [ -f /var/log/clickhouse-server/clickhouse-server.err.log ]; then tail -n 30 /var/log/clickhouse-server/clickhouse-server.err.log >&2; fi
          exit 1
        fi
      done
      sleep 1
    done
    ;;
  *) echo "Usage: testctl serve|health|restart SERVICE|exec SERVICE COMMAND" >&2; exit 1 ;;
esac
