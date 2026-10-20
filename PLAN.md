# MetricsPanel233 接续计划

记录日期：2026-10-13（北京时间）。2026-10-20 已重新明确产品目标；当前方向以 [docs/project-goals.md](docs/project-goals.md) 为准。

> 本文其余内容来自旧的“追求 Grafana OSS 全量 parity”阶段，仅保留为工程历史和可选兼容性参考。不得将其全量功能缺口或旧验收计划直接视为当前路线图。

## 当前项目目标（2026-10-20）

- **最终目标是开箱即用的单机监控后台，三合一**：替代 Grafana 面板 + 数据抓取 + 本地数据/向量存储。
- **默认交付形态是单个原生二进制**：下载或构建后直接 `serve`，不强制 Docker；Docker/ClickHouse 只作为可选扩展。
- 采集侧覆盖 Prometheus/OpenMetrics、OTLP/HTTP、MySQL/MariaDB、Redis、PostgreSQL 及常用服务指标接口。
- 存储侧同时提供时序样本和本地 64 维指标窗口向量检索，不依赖外部向量库。
- **产品 UI 由项目自有 `web/src/ui` 提供**；产品导航、页面和表单不再使用 Grafana 生态/UI。Grafana 导入/导出
  只保留为可选迁移适配。Table/TimeSeries 已切换为 `web/src/render` 自有渲染器；其余图表类型继续按框架矩阵落地。
- 账号模型为**多用户 + 角色**（Admin/Editor/Viewer），已落地；资源级 ACL 和自助改密后续补。
- 兼容能力只在服务迁移价值明确时实现；性能结论必须有可复现的负载、硬件和测量数据支撑。
- 先收敛单机核心闭环：采集/写入、保留与查询、仪表盘交互、开箱即用引导、可验证的性能基线。

## 2026-10-20 方向修正后新增

- **OTLP/HTTP metrics 接收**：`POST /v1/metrics` 和 `/otlp/v1/metrics`，支持 protobuf/JSON、gzip、Gauge/Sum/
  Histogram/Summary，Resource/Scope 属性和直方图桶转换为本地样本；单请求 16 MiB / 10,000 样本。
- **产品 UI 去 Grafana 化第一步**：`web/src/ui` 改为自有 Button/Input/Field/Select/Alert/Badge/Spinner/AppModal，
  登录、用户管理、Getting Started、通用 Dialog 和 Dashboards 操作不再依赖 `@grafana/ui`；产品导航移除 Grafana
  Plugins 页面。Grafana 主题仅保留给可选导入/插件兼容层。
- **Loki/Tempo 后端**：Loki LogQL range/instant 查询与 logs frames、Tempo TraceQL 搜索表格和 `/ready` 健康检查；
  专用编辑器和 Trace 详情仍待补。
- **自有渲染器框架**：`web/src/render` 契约、registry、自有 Table、自有 TimeSeries 核心、分帧调度器
  （`runInFrameSlices`/`nextFrame`）、增量签名工具；Table 默认自有，TimeSeries 通过 `?renderer=own`
  或 localStorage 启用，兼容桥暂为默认。`docs/renderer-framework.md` 记录覆盖矩阵与性能预算；
  `web/scripts/render-bench.mjs` 提供可复现的首帧/内存基准。

## 2026-10-20 本批落地

1. **@grafana/ui 设计系统基础**：根级 `AppThemeProvider`、`web/src/ui` 统一适配层；登录门禁、通用 Dialog、
   Dashboards 主操作已切换为 Grafana 组件。图标资源由 Vite 从 `@grafana/ui` 包内复制到 `/build/img/icons`，
   开发与生产一致。
2. **原生二进制部署**：`serve` 打印可点击 URL、数据库、存储、保留期和认证模式；首次运行提示；新增 `--open`；
   `scripts/build-release.ps1` / `.sh` 生成 Windows/Linux/macOS 归档；README 以二进制为默认路径。
3. **开箱即用**：系统概览新增 Uptime 面板；系统仪表盘首次写入幂等并有重启测试；新增 Getting Started 清单
   （接采集器 → 看数据 → 建告警），完成状态由真实数据推导，可关闭。
4. **SQLite 基线与向量收口**：新增 10 万点写入 + 24h 聚合测试、10k 向量检索基准，以及 50k 扫描上限测试；
   `docs/performance-baseline.md` 记录硬件、命令、结果与容量建议。
5. **文档同步**：AGENTS.md、docs/project-goals.md、docs/architecture.md、docs/development.md 已改为
   `@grafana/ui` 适配层政策；Docker 降级为可选。
6. **多用户与角色（`--auth=local`）**：接入 `internal/identity` + `internal/instance` 会话；首次启动打印一次性
   Setup token，管理员创建后失效；`POST /login`、`/api/auth/status|me|logout`、HttpOnly SameSite=Lax 会话、
   24h 轮换、登录限流、Bearer token 兼容；Admin/Editor/Viewer 路由矩阵，未知写操作 fail-closed 到 Admin；
   `/api/admin/users` 支持列表、创建、改角色、禁用；前端登录/首次设置/用户菜单/Users 管理页，Viewer 隐藏写操作。
   浏览器验证覆盖 setup、管理员登录、创建 Viewer、Viewer 403、错误密码、HttpOnly、退出与重新登录。

7. **渲染器收口**：按确认切回 `@grafana/ui` 的 `Table` / `TimeSeries`，恢复官方 footer、分页、图例、
   log/percent stacking 和对比曲线语义；此前 7 个渲染器相关 e2e 全部通过。补回 `.grafana-sdk-table` /
   `.grafana-table-viewport` CSS。
8. **Linux 容器验证**：`scripts/docker-verify.ps1` 交叉编译 Linux 二进制，构建 42.3 MiB 的 Alpine 运行时镜像，
   在容器内执行 setup、登录、Viewer 403、健康检查；`finally` 删除本次容器、卷、镜像、构建缓存、
   `web/test-results` 和临时目录。镜像远低于 1 GiB 预算。

`internal/instance` 的 `TestOrganizationLiveRealTransportSameChannelIsolation` 在当前 Windows 环境独立失败
（已确认与本次路由改动无关）。

下一批：资源级 ACL（当前角色是工作区全局）、密码重置/个人资料、审计日志、剩余产品页迁移到统一适配层，
以及把 `--auth=local` 作为 Docker 与发布包的默认推荐路径。

## 旧目标验收范围（已归档）

- Grafana 13.2.3 开源版全部功能，Enterprise 功能不在范围内。
- Go 1.27、Vite、React 19；仅 Windows PC 浏览器，不做手机版，不恢复进程页面。
- 简化面板操作，支持自定义多面板、快照、社区导入、插件和原生 agent CLI。
- MySQL、Redis、常见大数据采集及 Prometheus/OpenMetrics 接入均需支持。
- 集成测试用一个容器运行全部服务；CI 连续执行两次测试。
- 全功能尚未完成，不能以“完全替代 Grafana”发布。源码可以分阶段 push。

## 今晚保存的进度

1. 当前资源读取策略：8 个 dashboard/folder/search/export GET 入口，按实际资源 ACL 过滤后再分页；隐藏未授权父目录信息；展开每个递归库面板前核对真实 owner。响应最多缓冲 32 MiB；先完成全部身份回调，再在一个控制库快照中复核全部资源 fence，避免发送已撤权内容。
2. Basic 密码认证：首次 Argon2 后生成私有、请求内的 PasswordProof，清除私有绑定中的明文密码和别名。后续核对实际凭据、别名归属、组织、成员 generation 和当前角色。失效后不可恢复；不宣称检测未观察到的完整原始数据库回滚。
3. Library guarded 写入：实际 incarnation/version、旧新 owner、调用方模型克隆及删除时真实连接检查。尚未接到完整 HTTP action policy。
4. 手工 annotation guarded 写入：创建、patch、删除和 panel 批量删除；真实 idempotency 目标和 dashboard owner；新增版本/incarnation ledger，触发器覆盖 annotation/tag/alert relation 变化。自动告警投影不能经这些手工接口修改。Store.Open 初始化附加 schema，不自动迁移资源 ACL 或启用多用户。
5. 此前已 push 的通知管道（41e4d02）：生产者事务冻结 receiver/profile/template/member 状态；同一通知 scheduler 按实际目的地 firing ACK 过滤 resolved，验证 SMTP 部分成功、重启和 mixed group。生产仍由 legacy queue 执行，native 接管尚未开放。
6. 已有本地浏览器 session 登录 POST、轮换、退出及当前用户/device API；没有实际 PC 登录页面或完整管理流程。

权限代码仍为显式 opt-in。CLI 生产入口保持现有单工作区模式；不能把存储 helper 当作完整 HTTP 授权。

## 验证与部署记录

- 收尾源码整库 `CGO_ENABLED=1 go test -race ./... -count=1` 全部通过（instance 76.662s、plugins 122.312s、server 134.376s、store 150.766s）；`go vet ./...`、前端单元测试、TypeScript/Vite 构建及 Windows webui 二进制构建均通过。
- 独立复核已修复两个真实读取问题：嵌套私有 library 泄露、最后身份回调撤权后仍发送旧内容。三个复现用例已纳入永久测试；相关 HTTP race 复核通过。
- 前一源码 41e4d0229f13964f103b50fe6e266a4b9c3d88dd 的完整 CI 已通过：[运行记录](https://github.com/neko233-com/MetricsPanel233/actions/runs/38420454923)。收尾提交 push 后触发新 CI（PC 浏览器和单容器双次集成），写下本记录时尚未取得结果；明天必须核对该提交自己的 CI，不能引用旧结果代替。入口：[GitHub Actions](https://github.com/neko233-com/MetricsPanel233/actions)。
- 本机现有安装版仍为 v0.1.1，服务地址 http://127.0.0.1:7333；没有替换安装二进制或发布新 tag。启动 PID 会变化，以实际端口/health 为准。
- 稳定安装 bin/metricspanel.exe SHA256：860B2FC846DD9F80C3912F462F09818DD582D493ECE00BA089FFEE00C496C8CF。
- bin/metricspanel-permissions-candidate.exe 仅供构建检查，不是安装版。保留 data/metrics.db、密钥、插件和既有备份，勿用候选版直接覆盖。

## 旧目标待办（不自动纳入当前路线图）

1. `git status --short`、`git log -3 --oneline`，核对收尾提交的 GitHub Actions 是否全绿。若有失败，先复现、修复并重跑，不跳过检查。
2. 阅读 docs/grafana-product-parity.md、grafana-compatibility.md、grafana-resource-acl-contract.md、grafana-identity-http-contract.md。
3. 衔接真正的 resource mutation HTTP policy：按已注册路由逐一接 dashboard/folder/library/manual annotation。先真实解析目标与 body，检查相应 action，再用已有 guarded helper 在同一事务保存。未知路由、History/Query/Live 仍不能因 read 策略而放行。
4. 为权限管理、历史、数据源依赖执行、SDK proxy/chunked/Live 补完整 action matrix、过滤和撤权验证；通过 migration/readiness 后再接 CLI 多用户入口和 PC 登录/管理。

## 旧目标通知队列事项（按当前目标重新评估）

1. 完成 Prepared webhook 实际 HTTP transport，保留真实 wire、重试、超时、不跟随重定向和发送前/ACK 检查。
2. Contact point/template/routing/provisioning 全部使用同一个有效 receiver catalog，legacy 编辑与 receiver revision 在同一事务协调；不能依靠一次迁移快照。
3. 同一 scheduler 内持久化 drain：阻止新 legacy claim，让已经开始的发送/检查点结束；生产者继续记录。最后一个 writer 事务重新协调配置和全部 unfinished outbox，完成 adoption 后才改变 owner/epoch。配置变化使旧 readiness 失效。
4. 保留原父通知 ID、冻结来源、attempt/next retry/suppression 和历史；不得根据旧 sent/notified/http status 伪造目的地 firing ACK。旧未知 resolved 排除在渲染前，仍 firing 的恢复不能被 repeat=0/旧 hash 阻断。
5. 实际测试在途 HTTP barrier、drain 中配置修改、最终事务回滚、各崩溃阶段重启、old epoch 拒绝、sole worker 和 retry 子状态，之后接 API/CLI/Windows PC 配置。

## Grafana 全量兼容历史缺口（非默认产品目标）

继续以产品清单和逐插件矩阵验收：其余 datasource/visualization、布局编辑器、变量 ad hoc/group-by 与 rename、完整 logs/traces/profiles Explore、histogram/exemplar/discovery 和真实大数据集群、其余 receivers/template/image/external Alertmanager、admin/team/SA/external auth、preferences/starred/playlists、公共快照/expiry/rendering、App Platform CRUD/file provisioning、Live 用户隔离和插件全生命周期。

仅做 PC 浏览器验证，优先 Edge；API/存储测试不替代真实页面操作。每阶段验证后 push，全部验收通过后再正式发布和更新安装版。
