# MetricsPanel233 执行计划

更新时间：2026-10-20。

> **本文件是所有后续开发的唯一执行清单。** 每次改动必须先对应到本文件中的一个条目；完成时更新勾选、证据和总进度，并按固定格式汇报：目标、做了什么、接下做什么、总进度百分比、风险、验证。进度明细同步到 [docs/status.md](docs/status.md)。

## 产品目标（固定）

开箱即用的单机监控后台，三合一：

- 替代 Grafana 面板：自有 UI、仪表盘、Explore、告警。
- 数据抓取：Prometheus/OpenMetrics、OTLP/HTTP、MySQL/MariaDB、Redis、PostgreSQL、Linux/业务 exporter。
- 本地时序 + 向量存储：SQLite 默认，ClickHouse 可选；64 维指标窗口向量检索。

默认交付单个原生二进制，`serve` 零配置可用；Docker/ClickHouse 可选。产品 UI 使用 `web/src/ui` 与
`web/src/render`，不追求 Grafana 全量 parity；Grafana 导入/导出仅是可选迁移适配。

## 已验证基线

- Go 1.27、Vite 7、React 19；`serve` 单二进制；Docker 可选。
- 采集：Prometheus/OpenMetrics、OTLP/HTTP、MySQL/MariaDB、Redis、PostgreSQL、服务指标接口。
- 存储：SQLite WAL 默认；ClickHouse 可选；保留期；64 维向量检索。
- 认证：`--auth=token` 与 `--auth=local`；Setup token、会话、Admin/Editor/Viewer、限流与安全头。
- 渲染器：Table、TimeSeries、Stat、Gauge、BarGauge 已默认自有；TimeSeries 可用 `?renderer=compat` 回退。
- 验证：前端单测 104 passed；完整浏览器套件 69 passed / 2 skipped；CI run 39436800402 的 test + docker 全绿。
- CI 策略：仅 `v*` tag 或手动 dispatch 触发昂贵验证；普通 push 不触发。

## 总进度

| 工作流 | 权重 | 完成度 | 主要缺口 |
| --- | ---: | ---: | --- |
| 产品 Shell 与自有 UI | 15% | 85% | ReactBits 动效层（可选） |
| 渲染器 | 20% | 70% | Heatmap、Vector；Trace 详情 |
| 采集 | 15% | 85% | 推 + 拉双模式已完成；更多 exporter 与协议边界 |
| 存储与查询 | 15% | 75% | rollup、块级 min/max、大范围规划 |
| 认证与安全 | 15% | 85% | 周期性安全复查 |
| 运维与交付 | 10% | 80% | release checklist、升级说明 |
| 文档与 agent 契约 | 10% | 85% | 随行为变更持续同步 |
| 性能 | 10% | 40% | Worker 降采样、大范围基准 |

**加权总进度：74%**（±5 个百分点）。每完成一个执行项，更新本表与 `docs/status.md`。

## 执行队列

状态：`[ ]` 未开始，`[~]` 进行中，`[x]` 完成。按顺序执行；未完成当前项前不并行开启下一项，除非明确标注可并行。

### P0 产品闭环

- [x] **C0 — 推 + 拉双模式**
  - 范围：拉取抓取与推送接收同时可用，共享存储与角色策略。
  - 验收：Prometheus remote write、OTLP/HTTP、原生 JSON 推送与 scrape 拉取均有测试；Viewer 推送 403。
  - 证据：`POST /api/v1/write` 与 `/prometheus/api/v1/write`（snappy `prompb.WriteRequest`，32 MiB 压缩 /
    64 MiB 解码 / 100,000 样本）；`remote_write_test.go` 覆盖 snappy、原始 protobuf、畸形输入、空 `__name__`；
    角色测试覆盖 Viewer 403 / Editor 允许；文档 `docs/collection-modes.md`。

- [x] **R0 — Table / TimeSeries / Stat / Gauge / BarGauge 自有渲染器**
  - 证据：`web/src/render`，完整套件 69 passed / 2 skipped，commit `404c0d1`。
  - 验收：registry 解析、浏览器契约、390/768/1280、无 `@grafana/ui` 产品引用。

- [x] **S1 — 左侧导航 + 右侧内容 Shell**
  - 范围：可折叠左侧导航、右侧主内容区、顶栏；保留现有路由、权限、深链和移动端行为。
  - 验收：390/768/1280 无横向溢出；键盘可完成导航与折叠；Viewer 看不到写操作；现有浏览器套件全绿。
  - 证据：桌面折叠/展开、localStorage 持久化、键盘 Enter、390 移动抽屉、768/1280 无溢出；完整套件
    69 passed / 2 skipped（1 个既有变量用例首次抖动、重试通过，跟踪为 F1）。

- [x] **R1 — Logs 渲染器**
  - 范围：Loki frames、虚拟化行、标签过滤、搜索、空态/错误态。
  - 验收：10,000 行仍可交互；滚动与过滤有浏览器测试；不新增 `@grafana/ui` 产品引用。
  - 证据：`LogsRenderer` 虚拟化 10k 行、搜索、标签键/值过滤；浏览器测试断言 `data-log-count`、
    实际 DOM 行数 < 60、滚动高度、过滤结果与 1280 无横向溢出；完整套件 70 passed / 2 skipped。

- [x] **R2 — Traces 渲染器**
  - 范围：Tempo 搜索表格优先，trace 详情随后；耗时、状态、深链参数。
  - 验收：搜索与详情浏览器测试；空态/错误态；与后端 frame 契约一致。
  - 证据：`TracesRenderer` 读取 Tempo `traceID/rootServiceName/rootTraceName/startTime/durationMs`；支持搜索、
    按开始时间/耗时排序、可选 status 列与 `?traceId=` 深链；浏览器测试覆盖搜索、双向排序、深链回填与无溢出。
    完整套件 72 passed / 2 skipped。Trace 详情仍未实现，保持独立后续项。

- [ ] **R2b — Trace 详情**
  - 范围：按 traceId 获取 span 树/时间轴；先补后端 Tempo trace 查询，再做详情视图。
  - 验收：真实 Tempo fixture；span 层级、耗时与错误状态；`?traceId=` 深链。

- [ ] **R3 — Heatmap / Histogram / Vector 渲染器**
  - 范围：按渲染器矩阵补齐常用分析视图；向量视图读取 64 维字段。
  - 验收：每类一个浏览器契约；大 frame 有分帧或降采样。

- [ ] **P1 — Worker 降采样**
  - 范围：LTTB 或 min/max + transferable；与 `frame-budget`/`incremental` 协同。
  - 验收：100k 点基准；峰值与断点保持；首帧预算写入 `docs/performance-baseline.md`。

### P1 深度与交付

- [ ] **F1 — 稳定仪表盘变量解析时序**
  - 现象：`dashboard-variables` 偶发在预览或保存后面板查询中出现未解析的 `$var`。
  - 验收：`npx playwright test dashboard-variables.spec.ts --retries=0` 连续 3 次通过；面板不得在变量未就绪时发查询。

- [ ] **D1 — ClickHouse rollup 与大范围查询**
  - 范围：rollup/降采样、块级 min/max、保留期与 TTL 操作。
  - 验收：大范围查询计划与基准；SQLite 回退或明确限制。

- [ ] **C1 — 采集深度**
  - 范围：更多 exporter、OTLP histogram 边界、采集失败模式。
  - 验收：协议测试 + 超时/认证/畸形输入测试。

- [ ] **A1 — 认证与安全复查**
  - 范围：会话轮换、限流、可信代理、备份/恢复、泄露清单。
  - 验收：复查记录 + 回归测试；文档同步。

- [ ] **O1 — 发布收口**
  - 范围：版本升级说明、备份恢复演练、release checklist、tag 验证。
  - 验收：`v*` tag 触发完整 CI 全绿；发布归档可复现。

### 可选评估

- [ ] **V1 — Vite+ 评估**
  - 范围：隔离分支比较 build/lint/test/CI 成本。
  - 验收：书面决策；未证明 parity 前不迁移。
- [ ] **V2 — ReactBits 动效层**
  - 范围：仅装饰性动效；不改布局、焦点、对比度和表单行为。
  - 验收：尊重 `prefers-reduced-motion`；现有验收全绿。

## 完成定义

- 每个执行项必须有：代码、至少一个可执行验收、文档/台账更新、明确证据。
- 不得用源码阅读代替浏览器、Docker 或 CI 证据。
- 产品页面与 renderer 不得新增 `@grafana/ui` 引用；兼容桥只允许存在于 `web/src/grafana`。
- 响应式验收固定覆盖 390/768/1280；交互必须有键盘路径。
- 未完成项不得标记为完成；部分完成保持 `[~]` 并说明剩余。

## 本批已落地（2026-10-20）

| 提交 | 内容 |
| --- | --- |
| `404c0d1` | Stat/Gauge/BarGauge 进入第一方 registry；完整套件 69 passed / 2 skipped |
| `a51c275` | 图表与表格响应式验收（390/768/1280、动态高度、窄屏表格） |
| `ef500a8` | TimeSeries 自有渲染器设为默认，`?renderer=compat` 回退 |
| `e21a7ea` | 进度台账与固定汇报协议 |
| `031a8b9` | Oil UI skill 引用与边界 |
| `dcb2415` | Agent 契约；CI 改为 tag/手动触发 |

## 历史归档

2026-10-13 之前的 Grafana 全量 parity 计划、资源 ACL、通知队列和身份隔离工程笔记已由 git 历史保存，不再作为
当前路线图。需要兼容性背景时阅读 [docs/grafana-compatibility.md](docs/grafana-compatibility.md)、
[docs/grafana-product-parity.md](docs/grafana-product-parity.md) 和 [docs/agent/decisions.md](docs/agent/decisions.md)。
