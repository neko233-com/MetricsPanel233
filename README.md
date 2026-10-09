# MetricsPanel233

[English](README.en.md) · 中文

更简单的自托管监控工作空间：**Go 1.27 + Vite 7 + React 19 + 原生 Agent CLI**。
一个服务提供指标采集、持久化存储、查询和面板。界面可切换中文 / English。

适合 MySQL exporter、Go / Java / Python 业务后端及其他 Prometheus 文本指标。
SQLite 模式无需额外服务；ClickHouse 模式采用压缩列式存储、向量化聚合、按天分区和 TTL。

## 快速开始

需要 Go 1.27.0、Node.js 24（或 Vite 支持的 Node 版本）。

```sh
cd web
npm ci
npm run build
cd ..
go build -tags webui -o bin/metricspanel ./cmd/metricspanel
./bin/metricspanel serve
```

Windows：`go build -tags webui -o bin/metricspanel.exe ./cmd/metricspanel`，随后运行 `./bin/metricspanel.exe serve`。
打开 **http://127.0.0.1:7333**。内置采集器每 5 秒记录真实的内存、协程、HTTP 请求和运行时间。
网页打包进 Go 二进制，生产运行不需要 Node。

### Docker + ClickHouse

```sh
cp .env.example .env
# 修改 .env 中两个密码，然后运行：
docker compose up -d --build
```

访问相同地址，输入 `METRICSPANEL_TOKEN`。`docker compose down` 保留命名数据卷；
`docker compose down -v` 会删除数据，**只在确实需要清空时使用**。
非 loopback 监听要求至少 16 字符的 token。跨机器 HTTP 请通过 HTTPS 反向代理访问。

手动连接 ClickHouse：

```sh
export CLICKHOUSE_URL=http://127.0.0.1:8123
export CLICKHOUSE_USER=metricspanel
export CLICKHOUSE_PASSWORD=your-password
./bin/metricspanel serve --storage clickhouse --db data/control.db
```

PowerShell 设置环境变量使用 `$env:CLICKHOUSE_URL='http://127.0.0.1:8123'`。
ClickHouse 存指标；SQLite 存采集器、仪表盘、模板变量和模板原始 JSON，两个存储都要持久化。
`--retention-days 30` 是默认保留期，SQLite 每小时清理，ClickHouse TTL 在后台合并时清理。
已有 ClickHouse 表的 TTL 不会因修改启动参数自动重写；变更时执行受控的 `ALTER TABLE ... MODIFY TTL`。

## 采集 MySQL 和业务指标

```sh
metricspanel targets add --name mysql --url http://localhost:9104/metrics --interval 15s
metricspanel targets add --name backend --url http://localhost:8080/metrics --interval 5s
metricspanel targets list
metricspanel targets scrape --id 1
```

MySQL 通过官方 `mysqld_exporter` 采集，使用 `.my.cnf` 配置账号。
账号需要对应 exporter collector 的 MySQL 权限；可参考 [官方配置](https://github.com/prometheus/mysqld_exporter)。
[Go 业务服务示例](examples/go-service/main.go) 同时展示 `/metrics` 抓取和 JSON 主动上报。
Docker 自动化测试使用真实 MySQL 8.4 + 官方 exporter，测试账号仅用于隔离测试。

```json
{"samples":[{"name":"business_orders","labels":{"service":"checkout"},"value":233}]}
```

保存为 `samples.json`，运行 `metricspanel ingest --file samples.json`，或直接 POST `/api/v1/ingest`。
`timestamp` 使用 Unix 毫秒，省略 / 0 使用服务端时间。同序列同时间戳重复写入覆盖旧值。
NaN / Inf 无法存入 JSON；exporter 的非有限样本会跳过。支持经典 histogram / summary，暂不接收原生 histogram。

## Agent CLI

CLI 输出默认就是 JSON；错误写入 stderr，退出码 1。`--json` 也可显式指定。

```sh
metricspanel schema
metricspanel health
metricspanel stats
metricspanel metrics
metricspanel query --metric business_orders --range 30m --aggregation sum
metricspanel query --expr 'sum(rate(business_http_requests_total[5m]))' --range 1h
metricspanel query --metric business_orders --labels '{"service":"checkout"}'
metricspanel dashboards export --id system > dashboard.json
metricspanel dashboards save --file dashboard.json
metricspanel dashboards save --file grafana-template.json
metricspanel dashboards export --id IMPORTED_ID --format grafana > original-grafana.json
```

`METRICSPANEL_URL` / `--server` 指定服务地址；`METRICSPANEL_TOKEN` 指定 Bearer token。
`targets set` 更新完整配置（`--id --name --url --interval --enabled`）。
`targets delete --id ID`、`dashboards delete --id ID` 删除配置，指标保留到保留期结束。

## Grafana 生态兼容范围

提供 **Prometheus 数据源 API + 官方 PromQL 引擎**。Grafana 的 Prometheus 数据源 URL 指向：

```text
http://127.0.0.1:7333/prometheus
```

配置 Bearer Authorization 请求头后可在 Grafana 中查询；支持 instant / range query、labels、label values、series、metadata、targets 和 buildinfo。
PromQL 聚合、正则 selector、rate、histogram_quantile、子查询和常用函数由官方引擎执行。

网页“仪表盘 → 导入 JSON”或 CLI 可以导入 classic Grafana JSON：

- Prometheus targets，最多每面板 8 条查询；timeseries / graph、stat / singlestat、table。
- 折叠 row 和旧式 rows；query / custom / constant / textbox / interval 变量。
- `label_values(selector,label)`、多选 / All，以及 `$__interval`、`$__rate_interval`、`$__range` 等宏。
- 保留原始 Grafana JSON，可原样导出，再由 Grafana 本体使用。

**当前不是所有 Grafana 插件的替代运行时。** 自定义 panel/data-source 插件、Grafana 后端 API、Loki / Tempo、
告警、annotations、transformations、重复面板、复杂变量表达式和布局语义尚未完整实现。
gauge / bargauge 暂以 stat 呈现。导入器报告支持范围之外的特性，避免静默丢失。
因此“完全兼容整个 Grafana 开源生态”仍是后续目标，不能把当前版本声称为完全兼容。

## 性能、持久化与边界

- ClickHouse 是**列式数据库，向量化执行分析查询**，不是 embedding 语义向量库。
- 批量写入（1–10,000 样本），确认异步批量插入落盘后返回；MergeTree 开启 fsync。
- 参数化标签过滤，时间 / 指标排序键，ZSTD / Gorilla 压缩；基础聚合在 ClickHouse 执行。
- SQLite WAL + 事务；进程重启保持已确认样本和配置；Docker 数据卷保持容器重建后的数据。
- PromQL 目前把命中的样本加载到受控内存再执行，上限 100 万样本 / 10,000 序列，尚无查询下推。
- 原生查询最长 31 天、200 序列、2,000 分桶，SQLite 原生查询最多读取 250,000 样本。
- 每次抓取 / 推送最多 4 MiB、10,000 样本；最多 8 个并发抓取。
- 当前为单实例设计，未验证集群、百万级序列或长期大规模生产负载，不能承诺特定吞吐。

性能测试会报告当前机器的批量确认时间和查询耗时，以实测为准。
`metricspanel stats` 的 series 指仍在保留期内的序列；写入速率是过去 60 秒平均值。

## 验证

```sh
go test ./... -count=1
go vet ./...
CGO_ENABLED=1 go test -race ./... -count=1
go test -tags=integration ./tests/integration -count=2 -v -timeout=25m
```

Windows：`pwsh -File scripts/test-docker.ps1 -Repeat 2`；race 测试需本机 C 编译器。
集成测试使用固定 Compose 项目 `metricspanel233-tests`、随机宿主机端口、自动释放的并发锁；
每轮先清理旧测试资源，结束后删除该项目容器、网络、数据卷和两个测试应用镜像。
不清理其他项目或公共基础镜像，BuildKit 缓存会复用。若进程被强制杀死，下次执行会清理旧项目。

测试涵盖真实 Go 抓取 / 主动上报、MySQL exporter、两种后端的重复写入、应用重启、ClickHouse 重启、
Grafana 查询 / 模板以及资源清理。

浏览器自动化：先构建网页和 `bin/metricspanel[.exe]`，再运行：

```sh
cd web
npx playwright install chromium
npm run test:e2e
```

每轮启动临时数据库，验证中英切换、面板保存、PromQL、采集器、模板变量和移动布局，结束时删除临时数据。
GitHub Actions 自动运行 race / API / 浏览器测试与两轮 Docker 集成测试。

开发：后端 `go run ./cmd/metricspanel serve`；网页 `cd web && npm run dev`。

## API 与结构

`metricspanel schema` 返回机器可读 API 清单。原生 API `/api/v1`，Prometheus 兼容 API `/prometheus/api/v1`。
所有 API 与自身 `/metrics` 使用同一 Bearer token；网页 token 仅保存在 sessionStorage。
默认只监听 loopback。采集 URL 支持局域网 exporter，因此应将有权配置采集器的 token 视为管理权限。

```text
cmd/metricspanel       原生 CLI 与服务入口
internal/collector    抓取、解析与调度
internal/store        SQLite / ClickHouse 指标存储 + 配置持久化
internal/promcompat   官方 PromQL 引擎适配与兼容 API
internal/grafana       Grafana JSON 导入
web/src               React 应用、双语、SVG 图表
examples/go-service   可运行的 Go 业务接入示例
tests/integration     真实 Docker 全链路验收
```

Apache-2.0。指标格式遵循 [Prometheus 文本协议](https://prometheus.io/docs/instrumenting/exposition_formats/)，
分析存储使用 [ClickHouse MergeTree](https://clickhouse.com/docs/engines/table-engines/mergetree-family/mergetree)。
