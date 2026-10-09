# MetricsPanel233

[English](README.en.md) · 中文

更简单的自托管监控工作空间：**Go 1.27 + Vite 7 + React 19 + 原生 Agent CLI**。
一个服务提供指标采集、持久化存储、查询和面板。界面可切换中文 / English。

适合 MySQL exporter、Go / Java / Python 业务后端及其他 Prometheus 文本指标。
SQLite 模式无需额外服务；ClickHouse 模式采用压缩列式时序存储、向量化聚合、按天分区、TTL 和持久化 HNSW 波形向量索引。

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

网页“仪表盘 → 导入 JSON”或 CLI 可以导入 Grafana Classic、V1 / V2 资源 JSON：

- 保留原始 JSON、targets、refId、instant / range、legendFormat、24 列 gridPos、fieldConfig 和 transformations；最多 500 面板，每面板 32 查询。
- timeseries / graph、stat / singlestat、table、gauge、bargauge、text 和 row。HTML / Markdown 文本经过 DOMPurify 清理。
- 使用 **Grafana 官方 `@grafana/data`** 的标准转换算子、reducer、单位、阈值颜色和 value mapping；未知转换会明确报错。
- query / custom / constant / textbox / interval 变量；`label_values`、`label_names`、`metrics`、`query_result`、regex / sort、多选 / All、重复面板。
- `$__interval`、`$__rate_interval`、`$__range` 等宏，以及 regex / raw / pipe / csv / json 等变量格式；支持 `var-NAME` URL 参数。
- 原始资源可原样导出，再由 Grafana 本体使用；安装的 React 面板插件通过官方 SDK 加载，未安装的插件显示具体错误。
- 导入测试包含固定版本的社区 Node Exporter Full 模板。面板按可见区域加载，官方渲染依赖只在模板页加载。

[Go runtime 示例](examples/dashboards/go-runtime.json) 展示真实运行指标、instant 表格转换、仪表和曲线：

```sh
metricspanel dashboards save --file examples/dashboards/go-runtime.json
```

为 dashboard-as-code 工具提供 `POST /api/dashboards/db`、`GET / DELETE /api/dashboards/uid/{uid}`、`GET /api/search`，
支持版本冲突检查 / overwrite。提供数据源发现、health 和 `/api/datasources/proxy/uid/metricspanel/...` 查询代理。
这些接口同样要求工作空间 Bearer token。当前 dashboard API 使用根文件夹。

**当前不是所有 Grafana 插件的替代运行时。** 自定义 panel/data-source 插件、Loki / Tempo、Grafana expression 数据源、
Grafana Live / chunked streaming、应用插件页面、Angular 旧插件、annotations、文件夹 / 组织权限、library panel 和完整后端 API 尚未实现。
V2 的 Grid / AutoGrid 会转换为网格；Rows 展开，Tabs 按文档顺序显示；条件布局可见性和 row repeat 尚未执行。
field override 的单位、阈值、value mapping 等支持；自定义绘图选项（堆叠、双轴等）尚未完全执行。
因此“完全兼容整个 Grafana 开源生态”仍是后续目标，不能把当前版本声称为完全兼容。

## 向量波形分析

在“指标查询”中执行指标查询，点击“保存当前窗口”，再选择参考窗口并“查找相似窗口”。
窗口被转换为 **64 维 Float32 向量**，与原始指标分开持久化；比较图把两个窗口对齐到参考窗口时间轴。
`shape` 消除均值和尺度差异，寻找相似波形；`raw` 保留实际量级。默认只比较同一指标；只匹配相同归一化和分析方式。
计数器可以选择 `rate`，以每秒速率比较，包含重置处理。

```sh
metricspanel patterns capture --metric metricspanel_memory_bytes --range 30m --normalization shape
metricspanel patterns list --limit 1000
metricspanel patterns search --id PATTERN_ID --metric metricspanel_memory_bytes --limit 10
metricspanel patterns search --id PATTERN_ID --exact
metricspanel patterns get --id PATTERN_ID
metricspanel patterns delete --id PATTERN_ID
```

`--start` / `--end` 使用 Unix 毫秒；固定起止时间和输入数据可复现相同内容 ID，重复保存不会增加逻辑记录。
窗口至少 64 秒，最多 31 天；至少 48 / 64 个分桶有观测数据。最多连续 8 个空桶可线性插补，只用于向量分析，原始指标不变。
这是波形向量，未调用 LLM embedding。距离使用 L2，越小越相似，**不是概率或自动异常判定**。

ClickHouse 26.8 使用 `Array(Float32)` 和持久化 HNSW，`--exact` 关闭近似索引，扫描候选向量；
默认近似检索会重算候选距离，筛选较严格时数据库可能回退到精确扫描，或返回更少结果。
SQLite 采用精确 top-K 扫描，限制每次最多 50,000 个匹配向量。向量库独立保留，直到主动删除，不随原始指标 TTL 清理。
ClickHouse 数据卷保存向量和索引；SQLite 模式把向量放在同一个 WAL 数据库中。两种模式均验证了重启持久化。
向量捕获目前使用原生指标 / 标签查询，尚未直接将任意 PromQL 派生结果转成向量。

## Grafana 插件与数据源

网页「插件」支持安装、启用 / 停用、卸载插件和管理数据源。可从官方目录指定准确版本，也可以上传原始 ZIP。
默认验证 Grafana 官方 PGP 签名及所有文件的 SHA-256；相同安装包重复安装保留原安装时间，不增加目录。
React 面板通过共享的 Grafana 13.2.3 `data` / `runtime` / `ui` SDK 运行，支持 AMD 与 SystemJS 包。
已用未修改的官方 Clock 3.2.4 签名安装包验证实际渲染、时钟更新、页面刷新和移动布局。

```sh
metricspanel plugins catalog --id grafana-clock-panel --plugin-version 3.2.4
metricspanel plugins install --file grafana-clock-panel-3.2.4.zip
metricspanel plugins list
metricspanel plugins get --id grafana-clock-panel
metricspanel plugins disable --id grafana-clock-panel
metricspanel plugins enable --id grafana-clock-panel
metricspanel datasources save --file examples/datasources/prometheus.json
metricspanel datasources list
metricspanel datasources health --id remote-prometheus
```

后端插件使用 Grafana Go SDK 的 gRPC 协议 2，提供 QueryData、CheckHealth、CallResource；查询返回官方 DataFrame JSON / Arrow 转换结果。
运行时选择 `<executable>_<GOOS>_<GOARCH>[.exe]`，安装包必须提供当前平台可执行文件；需要系统动态库的插件还需部署相应运行库。
后端插件最多八个并发请求，不继承应用令牌 / 数据库密码。服务关闭、插件停用或卸载时关闭对应子进程。
数据源代理支持 HTTP(S)、Basic Auth 和 `jsonData.httpHeaderNameN` / `secureJsonData.httpHeaderValueN`（N=1–32）；插件查询走 `/api/ds/query`。

数据源密钥通过 AES-256-GCM 加密保存。**备份时同时保留 SQLite 数据库、同目录 `secrets.key` 和 `plugins/`**；
丢失密钥后必须恢复原密钥，不能用新密钥读取旧配置。插件资源只使用限于资源路径的短时 cookie，不授予数据 API 权限。
`--plugins-dir` 可指定目录；`--root-url` 用于私有签名安装包的 URL 校验。
开发插件必须显式配置 `--allow-unsigned-plugin PACKAGE_ID`，仅允许所列包。

目前仍未达到完整 Grafana 插件运行时兼容：Live / chunked streaming、应用配置页面、完整插件配置编辑器、旧 Angular 插件和部分核心服务待补齐。
支持的能力和剩余项也会出现在 `metricspanel schema` 中。

## 性能、持久化与边界

- ClickHouse 同时提供**列式时序分析**和 **HNSW 指标窗口向量检索**，用于数值波形分析。
- 批量写入（1–10,000 样本），确认异步批量插入落盘后返回；MergeTree 开启 fsync。
- 参数化标签过滤，时间 / 指标排序键，ZSTD / Gorilla 压缩；基础聚合在 ClickHouse 执行。
- SQLite WAL + 事务；进程重启保持已确认样本和配置；Docker 数据卷保持容器重建后的数据。
- PromQL 目前把命中的样本加载到受控内存再执行，上限 100 万样本 / 10,000 序列，尚无查询下推。
- labels / label values / series 发现只查询序列元数据，不传输原始指标样本；最多 10,000 个匹配序列。
- 原生查询最长 31 天、200 序列、2,000 分桶，SQLite 原生查询最多读取 250,000 样本。
- 每次抓取 / 推送最多 4 MiB、10,000 样本；最多 8 个并发抓取。
- 当前为单实例设计，未验证集群、百万级序列或长期大规模生产负载，不能承诺特定吞吐。

性能测试会报告当前机器的批量确认时间和查询耗时，以实测为准。
Docker 测试追加 4 个并发写入端：SQLite 10 万样本、ClickHouse 100 万样本，记录批量 p50 / p95 和 24 小时聚合时间。
另外保存 10,000 个 64 维窗口向量，通过 `EXPLAIN` 确认 HNSW 被使用，与精确查询比较 recall@10，并在重启 ClickHouse 后再次检查索引查询计划。
这验证有界工作负载，不代表百万活跃序列或长时间生产负载。
`metricspanel stats` 的 series 指仍在保留期内的序列；写入速率是过去 60 秒平均值。

## 持久化告警与记录规则

网页「告警」支持创建、编辑、暂停、手动执行和查看每个标签实例的状态 / 历史。
后台每条规则独立调度，最多四条并发查询；规则、等待触发 / 恢复计时和最近 100000 条状态变化写入 SQLite WAL 控制数据库。
无论指标使用 SQLite 还是 ClickHouse，重启应用后计时继续。修改规则会重置实例，并记录恢复事件；原生更新需要携带 GET 返回的 `version`。

```sh
metricspanel alerts save --file examples/alerts/runtime-memory.json
metricspanel alerts list
metricspanel alerts evaluate --id runtime-memory
metricspanel alerts history --id runtime-memory --limit 100
metricspanel alerts get --id runtime-memory
metricspanel alerts get --id runtime-memory > rule.json
metricspanel alerts save --id runtime-memory --file rule.json
metricspanel alerts import-grafana --file grafana-rule.json
```

`condition=presence` 使用 Prometheus 语义：返回的时序触发，包括值为 0 的时序。
`condition=nonzero` 使用布尔值，网页默认使用这种方式：`up == bool 0`、`memory_bytes > bool 1073741824`。
状态包括 Normal / Pending / Firing / Recovering / NoData / Error，无数据与错误策略可选择正常、触发、专用状态或保持上次状态。
Prometheus 兼容 `/prometheus/api/v1/rules` 与 `/alerts` 可发现规则及活动实例。
记录规则指定 `record` 指标名，将 PromQL 结果写回当前指标数据库，可供模板查询。

Grafana provisioning 的规则 GET / POST / PUT / DELETE 及规则组 GET 可用。
支持映射到 `metricspanel` 的 Prometheus instant / range 查询，以及严格 reduce（last/min/max/mean/sum/count）、threshold、单查询引用 math、单个 classic condition。
原始数据查询图保留用于导出；未知数据源、节点、循环依赖、通知设置和恢复阈值明确拒绝。
通过原生 API 修改表达式 / 判断方式 / 记录指标后，旧 Grafana 查询图会清除；CLI 可直接读取并更新含 `runtime` 的 GET 结果。
这部分尚未完全覆盖 Grafana 告警语义：多查询引用 / 跨标签集合 math 联合、复合 classic 条件、规则组原子更新、注解模板、外部通知 / Alertmanager 尚待实现。

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
Grafana 查询 / 模板、官方签名插件、Linux Go SDK 插件查询 / 健康 / 资源接口 / 重启及资源清理。

浏览器自动化：先构建网页和 `bin/metricspanel[.exe]`，再运行：

```sh
cd web
npx playwright install chromium
npm run test:e2e
```

每轮启动临时数据库，验证中英切换、面板保存、PromQL、采集器、资源模板、官方转换、百分比单位、重复面板、波形保存 / 检索、文本清理和移动布局，结束时删除临时数据。
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
