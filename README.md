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

**当前不是所有 Grafana 插件的替代运行时。** React 面板及 Go SDK 数据源已有原始安装包运行能力，
但 Loki / Tempo 和各插件的全部核心服务依赖仍需逐项验证。Grafana expression 数据源、
部分核心 UI 扩展点、应用依赖的部分核心服务、Angular 旧插件、文件夹 / 组织权限、library panel 和完整后端 API 尚未实现。
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

应用插件支持官方 `AppPlugin` 根页面、React 配置页和 `/a/PLUGIN_ID/` 路由。
在「插件 → 配置」保存普通 JSON、加密密钥和导航固定状态；已保存的密钥只返回字段标记，留空会保留原值。
原生配置 API / CLI 要求带上当前 `version`（首次为 0），冲突返回 409；Grafana 兼容的旧配置接口允许省略版本。
插件升级保留启用状态、子插件偏好、配置和密钥。停用应用会停止整个安装包，单独停用子插件只停止该子插件。

```sh
metricspanel plugins settings --id APP_ID
metricspanel plugins configure --id APP_ID --file examples/plugins/app-settings.json
```

示例中的版本号必须换成当前查询结果的 `version`。`secureJsonData` 可传入新密钥，`secureJsonFields` 的值设为 `false` 可清除对应密钥。
后端资源、health、Live 和应用内的数据源接收官方 `AppInstanceSettings`，包含解密配置和更新时间。
自动化测试使用真实 Go SDK 应用与内置数据源进程，验证页面路由、插件自带配置页、重启恢复、凭据隐藏和停用隔离。

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

后端插件使用 Grafana Go SDK 的 gRPC 协议 2，提供 QueryData、QueryChunkedData、CheckHealth、CallResource、SubscribeStream、RunStream 和 PublishStream；查询返回官方 DataFrame JSON / Arrow 转换结果。
运行时选择 `<executable>_<GOOS>_<GOARCH>[.exe]`，安装包必须提供当前平台可执行文件；需要系统动态库的插件还需部署相应运行库。
后端插件最多八个并发请求，不继承应用令牌 / 数据库密码。服务关闭、插件停用或卸载时关闭对应子进程。
数据源代理支持 HTTP(S)、Basic Auth 和 `jsonData.httpHeaderNameN` / `secureJsonData.httpHeaderValueN`（N=1–32）；插件查询走 `/api/ds/query`。

数据源和应用插件的密钥通过 AES-256-GCM 加密保存。**备份时同时保留 SQLite 数据库、同目录 `secrets.key` 和 `plugins/`**；
丢失密钥后必须恢复原密钥，不能用新密钥读取旧配置。插件资源只使用限于资源路径的短时 cookie，不授予数据 API 权限。
`--plugins-dir` 可指定目录；`--root-url` 用于私有签名安装包的 URL 校验。
开发插件必须显式配置 `--allow-unsigned-plugin PACKAGE_ID`，仅允许所列包。

目前仍未达到完整 Grafana 插件运行时兼容：其余核心 UI 扩展点、完整数据源配置编辑器、旧 Angular 插件和部分核心服务待补齐。
支持的能力和剩余项也会出现在 `metricspanel schema` 中。

## Grafana UI 扩展

应用插件可以使用官方 `addLink`、`addComponent`、`addFunction`、`exposeComponent`，消费方支持
`usePluginLinks`、`usePluginComponents`、`usePluginFunctions`、`usePluginComponent` 及链接 / 组件 Observable。
提供方按 `plugin.json` 扩展声明自动加载，无需先打开应用页面；每插件限额、动态 `configure` 隐藏和覆盖、
组件 `.meta`、同步 / 异步函数、所属插件的 `PluginContext` 和主题均已接通。链接上下文只读，保护工作空间数据；
组件更新配置时保留本地状态，停用、卸载或升级会移除旧注册及其弹窗 / 侧栏，旧函数引用也检查当前安装包。

工作空间目前接入面板菜单、AppChrome、顶栏操作、顶栏按钮、MegaMenu 操作及扩展侧栏六类核心位置。
面板菜单传入原始面板 ID、dashboard UID、查询 targets、变量、时间范围和当前 DataFrame。
`onClick` 助手支持 `openModal`、`openSidebar`、`closeSidebar`、`toggleSidebar`；弹窗正文收到 `onDismiss`。
其他插件自定义扩展点可以直接使用上述消费 API。其余 Grafana 核心位置和内置 exposed components 仍需逐项接入。

每五秒同步配置及安装状态；提供方最多四个并行加载、每次限二十秒；每插件每类注册最多 1024 条、
页面最多请求 1024 个扩展点。`plugins get --id ID` 可让 Agent 发现声明，实际客户端回调需要浏览器运行时。
自动化用两个提供方与独立消费方验证上下文、限额、状态保持、升级、撤销、弹窗、侧栏和手机布局。
契约参考 [Grafana UI extensions](https://grafana.com/developers/plugin-tools/reference/ui-extensions-reference/ui-extensions)，实现匹配当前共享的 13.2.3 SDK。

## 持久化注释

`/api/annotations` 支持创建、查询、完整更新、局部更新、删除和标签枚举；另有 Graphite 写入与明确面板范围的批量删除。
时间使用 Unix 毫秒，Graphite `when` 使用秒；时间点与区间都按交叠查询，标签默认为 AND，`matchAny=true` 切换 OR。
支持 `dashboardUID` / 旧版 `dashboardId` 与 `panelId`；当前工作空间使用一个本地主体，接口沿用统一 Bearer 认证。
注释和标签索引存于 SQLite WAL 控制库，两种指标后端均在重启后保留。可选 `idempotencyKey` 让同一规范化创建请求重试返回相同 ID，键对应不同内容返回 HTTP 409。

```sh
metricspanel annotations save --file annotation.json
metricspanel annotations list --dashboard-uid system --tags '["deploy"]' --start 1791590000000 --end 1791600000000
metricspanel annotations patch --id 1 --file annotation-patch.json
metricspanel annotations tags --name deploy
metricspanel annotations list --type alert --alert-uid RULE_UID
metricspanel annotations delete --id 1
```

创建文件示例：`{"dashboardUID":"system","time":1791590000000,"timeEnd":1791590060000,"text":"部署完成","tags":["deploy"],"idempotencyKey":"deploy-233"}`。
原生图表的注释按钮提供中英文创建、编辑和删除；标记与区域按当前窗口裁剪，文本使用安全的纯文本提示。
经典/V1/V2 模板的内置 Grafana 仪表盘与标签查询、变量标签、`enable`、面板 `filter.ids` 会执行；模板的 `hide` 不会隐藏事件。
SDK 面板通过公开 `PanelData.annotations` 接收官方 `arrayToDataFrame` 转换的帧；混合来源保留完整字段，注释与指标序列分别传递。
每次查询最多 1000 条，前端最多 32 个注释查询/1000 条合并事件，正文最多 8192 字节、标签最多 32 个。
告警的等待、触发、恢复、无数据/错误，以及规则修改、暂停和删除会自动生成带 `prevState`/`newState` 的时间点注释。
状态、历史、注释和标签在同一事务中提交；未改变的状态和过期执行不会重复生成，重启后可通过 `type=alert`、`alertUID` 或稳定数值 `alertId` 查询。
规则注解中的 `__dashboardUid__` 与正整数 `__panelId__` 关联模板面板；无面板关联时，可用公开标签生成的 `key:value` 标签查询。
原生图表按状态着色，注释列表以中英文显示状态；自动记录在编辑器中只读。SDK 混合注释帧保留状态字段。
自动注释跟随最近 100000 条告警状态历史保留，手工注释独立保留。升级前的历史不会回填；recording rule 不生成告警注释。
插件数据源通过公开 `AnnotationSupport` 执行默认查询、预处理、查询准备和自定义事件转换；旧版 `annotationQuery` 入口也会执行。
默认转换支持字段名（忽略大小写）、固定文本、跳过字段和标签拆分，使用官方 SDK merge 算子合并帧；旧字符串查询模型会迁移到 target。
模板查询提供面板实际时间范围、时区、变量、interval 和 `__annotation` 上下文，支持数据源 UID 变量。
流式更新保持订阅；单个查询失败会显示名称和错误，其他注释及指标继续显示。每个查询限 60 秒无更新/10000 帧行/1000 事件，共享缓存最多 200 项。
离开页面取消可订阅的数据源请求，包括 Go SDK HTTP/gRPC；旧版 Promise 的迟到结果会忽略，并向插件传递可选取消信号。
不同数据源的事件 ID 分别保留并隔离，外部事件在编辑器中只读，不自动写入本地注释表。`annotations list` 查询本地记录；agent 可用 `datasources query --id UID --file FILE` 读取数据源原始帧，前端 SDK 回调由浏览器执行。
Testify 和真实浏览器用例验证 Go SDK 帧、重启后的数据源设置、经典/V1/V2 模板、字段映射、同 ID 不同来源、错误隔离、变量和取消。
注释查询配置编辑器、周期性时间区间和完整组织权限仍需补齐。
Testify、真实 SDK 浏览器用例与 Docker 两轮重启测试覆盖持久化、幂等、区间、标签、CRUD 和手机布局。
契约参考 [Grafana annotations API](https://grafana.com/docs/grafana/latest/developers/http_api/annotations/)。
插件契约参考 [Grafana 13.2.3 查询执行器](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/annotations/executeAnnotationQuery.ts) 与 [标准注释转换](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/annotations/standardAnnotationSupport.ts)。
宿主初始化官方日志注册表，并同步 SDK 的数据源设置缓存和插件加载器；数据源服务重新加载时，设置增删改会同步新版与旧版服务。
缓存启动接口属于固定版本 SDK 的 core 实现，发布包未公开其入口；Vite 使用两个仅供宿主调用的 13.2.3 路径别名，升级 SDK 时必须重新验证这些接口。

## Dashboard 时间范围

经典 JSON、V1 和 V2 resource 的 `time` / `timeSettings` 默认时间与时区会应用到实际查询。
URL 支持 `from/to`、`time/time.window` 和 `timezone`；前者优先，中心窗口以 Unix 毫秒表示，
切换范围保留 `var-*` 参数，重载及浏览器历史可还原窗口。
自定义选择器支持 Unix 毫秒、ISO 日期和 SDK 日期运算（例如 `now-1h/h`、`now/d`），包含 IANA 时区及夏令时。
每次解析共享一个 now；固定时间不会随刷新向后移动，范围限于 31 天。

SDK `PanelProps.onChangeTimeRange` 和原生折线图拖选默认更新工作空间及 URL，并触发真实查询。
查询 request、SDK 面板时间范围、变量查询、单位日期展示和波形捕获都使用选择的边界与时区；
`$__from`、`$__to`、`$__range_ms` 保留精确毫秒，TemplateSrv.updateTimeRange 也已接通。
单面板 `timeFrom` 支持日期运算和变量，仅覆盖相对仪表盘范围；`timeShift` 也适用于固定范围，按时区和日历移动，支持 `1d/d` 等舍入。
查询快照同步到 SDK PanelProps、数据时间范围、宏、转换、图表和面板菜单；`hideTimeOverride` 隐藏时间说明。
带偏移面板的 SDK 缩放和原生拖选会先反向换算仪表盘窗口，避免查询再次应用同一偏移。
经典 `refresh` / V2 `timeSettings.autoRefresh` 和刷新选项会应用到定时器，导入模板缺省关闭；原生工作空间缺省 5 秒。
刷新控件提供关闭、指定间隔和自动模式；URL `refresh` 优先于模板，空值表示关闭，切换与历史保留参数。
刷新至少间隔 5 秒，自动模式依据窗口宽度和范围计算；隐藏页面暂停请求，恢复时补一次刷新，页面卸载清理定时器。
Grafana 标准 `timeCompare` 会追加独立的历史查询，经典/V1 面板直接保存该字段，V2 保存于 `data.spec.queryOptions.timeCompare`。
支持明确间隔和 `__previousPeriod`；查询 `timeRangeCompare: false` 退出比较，V2 保存在 `DataQuery.spec` 中。
早期 MetricsPanel 的 `compareWith` 字段继续可用；标准字段优先，显式空字符串关闭比较，避免旧别名意外启用历史查询。
比较帧使用幂等的 `-compare` refId 与 SDK `timeCompare.diffMs` 元数据，SDK 保留原始历史时间戳，原生曲线使用官方对齐函数及虚线样式。
比较的 `1d` / `1M` 按 SDK 固定时长（24 小时 / 30 天）偏移，与 `timeShift` 的日历日期运算不同；相对 raw 范围随刷新重新解析。
面板上的“缩放范围 → 当前面板”提供原生局部视图，SDK 缩放和拖选仅查询当前面板，保留 URL 和其他面板范围；刷新保持局部窗口，重置或父范围/变量变化恢复仪表盘窗口。
局部缩放是浏览器视图状态，原始模板导出不变。比较错误会显示并保留主查询结果。
十二个时间模型测试、Testify 导入测试与浏览器回归覆盖历史请求、比较 opt-out、帧元数据、曲线对齐、刷新、局部缩放及中英文手机布局。
浏览器实际导入经典/V1/V2 标准字段并检查查询与 Agent CLI 原始导出；也验证空字符串关闭和旧别名。
Grafana 13.2 的 `zoomBehavior` 属于 Scenes 运行状态，当前经典/V2 序列化未保存它；原生局部缩放保留为浏览器视图操作。
完整 Scenes 生命周期及其他未实现的时间交互仍需补齐。
参考 [Grafana 时间 URL 契约](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/create-dashboard-url-variables/)。
时间覆盖按 [Grafana 13.2 PanelTimeRange 源码](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/scene/panel-timerange/PanelTimeRange.tsx) 实现。
时间比较字段匹配 [经典模板序列化](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/serialization/transformSceneToSaveModel.ts) 与 [V2 模板序列化](https://github.com/grafana/grafana/blob/v13.2.3/public/app/features/dashboard-scene/serialization/transformSceneToSaveModelSchemaV2.ts)。

## Grafana 应用事件

`getAppEvents()` 现在返回共享的官方 SDK `EventBusSrv`，支持 typed `publish/getStream/subscribe`、
scoped bus 和旧版 `emit/on/off`，自定义事件可在应用和扩展间通信。
插件发出 `RefreshEvent` 或旧版 `refresh` 会触发实际工作空间查询；原生刷新按钮和五秒轮询也广播该事件。
并发刷新按工作空间合并，运行中的请求最多保留一次后续刷新。
原生时间范围选择广播 `TimeRangeUpdatedEvent`，包含 SDK DateTime 及原始 `now-RANGE / now`。
面板独立事件总线与应用总线双向转发 refresh、time-range 和 theme 事件，源面板不会收到重复广播；
卸载面板只清理自己的订阅，其他面板和应用继续工作。

`AppEvents.alertSuccess/alertWarning/alertError/alertInfo` 显示四种原生通知，支持标题、描述及 Error.message，
文字按纯文本展示，最多 8192 字符。错误和警告使用 `role=alert`；关闭按钮随中文 / 英文界面切换。
这是浏览器通知；服务端告警通知投递仍待实现。主题目前固定为深色；CopyPanelEvent 的原生复制动作、
原生图表的全局 hover/select，以及其他应用核心服务仍需继续接入。
独立 App 和两个 SDK 面板测试覆盖真实查询更新、重复事件隔离、卸载 / 重挂载、限流、通知及手机布局。
参考 [Grafana 事件订阅契约](https://grafana.com/developers/plugin-tools/how-to-guides/panel-plugins/subscribe-events)。

## Grafana Live 与 Agent 实时订阅

`/api/live/ws` 提供 Centrifuge JSON WebSocket 协议。数据源通道为 `ds/UID/path`，插件通道为 `plugin/ID/path`。
原始插件可通过 `getGrafanaLiveSrv()` 订阅；`DataSourceWithBackend` 的 DataFrame `meta.channel` 会自动接入实时流。
浏览器使用官方 `StreamingDataFrame` 保存有界缓冲、执行已有转换和单位格式化；实时面板持续更新，普通查询保留定时刷新。
混合面板按响应 key 合并数据；只重查普通查询，保留正在运行的实时订阅。
同一通道共用一个后端 SDK RunStream；同页面共用一个 WebSocket，最后一个订阅离开即取消。
更新数据源会取消旧配置并通知浏览器重新订阅；删除数据源、停用插件和关闭服务器会终止流。

```sh
metricspanel live channels
metricspanel live watch --channel ds/MY_DATASOURCE/path --limit 10 --duration 1m
metricspanel live watch --channel ds/MY_DATASOURCE/path --metadata '{"key":"value"}' --limit 0 --duration 0
metricspanel live publish --channel ds/MY_DATASOURCE/path --file packet.json
```

`watch` 每个初始帧 / 推送帧输出一行 JSON（NDJSON）：`type`、`channel`、`timestamp`、`data`。
默认最多 10 帧 / 1 分钟，初始帧计入限额；`0` 分别关闭帧数 / 时间限额。Ctrl+C 释放订阅。
订阅失败或连接中断返回 JSON 错误和非零退出码，Agent 可以决定是否重新连接。
`publish` 的数据结构和权限由插件决定；同一通道的订阅 metadata 必须一致。

浏览器会话 cookie 有效 15 分钟，只授权 WebSocket 路径，重连前自动刷新；CLI 使用 Bearer 升级头。
最多 256 个活动通道、每连接 128 个通道、单包 1 MiB；前端缓冲最多 10,000 行。
实时传输本身不落盘，插件推送不会自动写入指标库；采集器 / ingest 的 SQLite 和 ClickHouse 持久化不受影响。

## 分块查询与 Agent NDJSON

原生支持 Grafana 连接查询接口 `POST /apis/{pluginId}.datasource.grafana.app/v0alpha1/namespaces/default/connections/{uid}/query`。
请求采用官方 SDK DTO：`from` / `to` 支持时间表达式，查询也可提供独立 `timeRange`。默认返回普通 JSON；
设置 `Accept: text/jsonl` 后调用 SDK `QueryChunkedData`，每个 JSON 行立即 flush，Arrow 转为官方 DataFrame JSON。
`/api/ds/query` 也支持该 Accept，保留其原有请求格式。接口要求工作空间 Bearer token，并检查 Origin。

```sh
metricspanel datasources query --id metricspanel --file examples/datasources/query.json
metricspanel datasources query --id MY_DATASOURCE --file query.json --stream
```

`--stream` 每行输出 `refId`、`frameId`、`frame` 或 `error` / `errorSource`。
同一 `(refId, frameId)` 的首块包含 schema，后续块追加数据。正常 EOF 结束，没有默认帧数截断；
部分查询失败仍保留已输出的数据，完成后返回 JSON stderr 错误和退出码 1。Ctrl+C 会取消后端请求。
不支持分块 RPC 的旧插件只在尚未输出任何块时回退到 QueryData，避免重复数据。
浏览器提供官方 `BackendSrv.chunked` 原始字节 Observable，插件自行解析和追加；有限查询执行期间不会被定时刷新重启。
限额为 32 个查询、1024 个 frame、每块 8 MiB、每请求 32 MiB、后端 60 秒，最多 4 个数据源并行。
CLI `--duration` 默认 1 分钟，`0` 关闭客户端期限；服务器期限仍适用。

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
Grafana 查询 / 模板、官方签名插件、Linux Go SDK 插件查询 / 健康 / 资源 / Live、Agent NDJSON 订阅、重启及资源清理。

浏览器自动化：先构建网页和 `bin/metricspanel[.exe]`，再运行：

```sh
cd web
npx playwright install chromium
npm run test:e2e
```

每轮启动临时数据库，验证中英切换、面板保存、PromQL、采集器、资源模板、官方转换、百分比单位、重复面板、波形保存 / 检索、文本清理和移动布局，结束时删除临时数据。
GitHub Actions 自动运行 race / API / 浏览器测试与两轮 Docker 集成测试。
CI 从 [Google Cloud 官方缓存](https://docs.cloud.google.com/artifact-registry/docs/pull-cached-dockerhub-images)拉取按摘要固定的测试镜像，减少 Docker Hub 限流影响。
`compose.test.yml` 的 `METRICSPANEL_TEST_CLICKHOUSE_IMAGE`、`METRICSPANEL_TEST_MYSQL_IMAGE` 和 `METRICSPANEL_TEST_EXPORTER_IMAGE` 可覆盖测试镜像来源；生产 Compose 使用独立配置。

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
