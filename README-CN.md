# metrics233-all

[English](README.md) · **简体中文**

单机性能监控与业务监控，拆为三个独立版本的 Git submodule：

| 子仓库 | 职责 |
| --- | --- |
| [metrics233-server](https://github.com/neko233-com/metrics233-server) | 采集、SQLite 时序存储、仪表盘、查询与告警；原生单二进制，ClickHouse 可选。 |
| [metrics233-lib-go](https://github.com/neko233-com/metrics233-lib-go) | Go 标准库 SDK：链式 Gauge/Counter、批量上报、有界队列、重试、显式启动和关闭。 |
| [metrics-lib-rust](https://github.com/neko233-com/metrics-lib-rust) | Rust crate `metrics233`：链式 Gauge/Counter、有界批量、HTTPS、显式启动/刷新/关闭；Rust 1.88+。 |

```sh
git clone --recurse-submodules https://github.com/neko233-com/metrics233-all.git
cd metrics233-all
```

已有 checkout 使用 `git submodule update --init --recursive`。父仓库固定三个子仓库的提交，可复现同一套组合；子仓库也能单独使用。

服务构建与开发见 [服务端 README](metrics233-server/README-CN.md)，业务接入见 [业务监控说明](metrics233-server/docs/business-monitoring.md) 与 [Go SDK](metrics233-lib-go/README.md)。现有 `metricspanel` CLI、`METRICSPANEL_*` 环境变量和 HTTP 路由继续使用既有契约。

服务支持 **上报来源** tab，提供一键拉黑、解除与允许名单；给业务程序分发独立仅上报 Token，
不能查询或自行解封。支持飞书加密通知和持久化告警 CD，新建规则默认一小时。
契约见 [上报凭据](metrics233-server/docs/reporting-access.md)、[来源策略](metrics233-server/docs/reporting-sources.md)
和 [告警冷却](metrics233-server/docs/alert-notification-cooldown.md)。

最新五平台公开正式版本仍为 [server v0.2.6](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.6)，两轮干净构建的 7 个文件一致，归档检查与实际 Linux 归档运行验收通过；7 个官方下载文件的 SHA 同时匹配本地产物与 GitHub asset digest。
下载后使用附带的 `SHA256SUMS` 和 `PROVENANCE.json` 校验，或从固定源码构建；独立的 [Go SDK v0.1.0](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0)
支持 Go 1.23+，没有第三方依赖。

[Rust SDK v0.1.0 Git 版本](https://github.com/neko233-com/metrics-lib-rust/releases/tag/v0.1.0) 已独立验证；
crates.io 上传因 HTTP 403 受阻，当前使用固定 Git 依赖。Rust 子模块新 pin `f38673c6` 仅修正 README，v0.1.0 代码与 tag 仍为 `bda47165`：

```toml
metrics233 = { git = "https://github.com/neko233-com/metrics-lib-rust", rev = "bda4716538a366379f69300cb816e272f1db11de" }
```

后台使用独立账号密码登录，上报 Token 仅授予写入权限。数据默认保留 15 天；业务实例保留完整身份，
支持实时继承模板或独立仪表盘。玩家地区与 SQL Entity 统计采用全量虚拟列表；飞书支持卡片、可编辑有界模板和不发送预览。
Agent HTTP 发现接口说明权限、配置 schema 与 CAS 更新流程。

当前服务源码为面板可读性最终本地修正 [`ed444317`](https://github.com/neko233-com/metrics233-server/commit/ed444317090daa1c7cd6b76c70e0ca8ed4d208f0)，Go SDK `4f63688e`、Rust SDK `f38673c6` 不变。
最终 **141/141** 前端单测、TypeScript/Vite 与 `0.2.8` 嵌入 webui 构建通过；同组 **9 项浏览器无重试全部通过，29.4 秒**，
默认 overview 的 Go 堆分配内存、协程数、累计 HTTP 请求数三项自身指标均显示友好图例（含 native 派生 rate 显示），
native rate 标签不误用于原始 Counter 累计值。
私有部署 `0.2.8` 在**停服前准备发布文件回滚归档**时失败；健康的 `0.2.7` 继续服务，未换包，也未发生运行时回滚。
公开的 [Express233 归档修复](https://github.com/neko233-com/express233/commit/c80ce429972b02cbdd41861dd4170179c46ff008)
已通过隔离 Linux 真实脚本双轮验证（10 项通过、零跳过，4.301 秒），其 CI 仍待完成。
新不可变部署版本 `0.2.9` 的参数已准备、**尚未触发**；最终部署和真实图例仍待验收，最新公开五平台版本仍为 v0.2.6。

中间源码 `c79971b8` 通过 139 项前端单测、TypeScript/Vite 与嵌入 webui 构建；该轮 UI 修正的首个完整浏览器结果为 **98 项：93 通过、2 SQL 环境跳过、3 失败**；
随后修正 SDK 比较后缀、夹具请求 ID 与旧 PromQL 主视图断言，最终定向 **9 项无重试全部通过，23.6 秒**。
覆盖 stat/gauge/bargauge、逐值时间/null/0、12 序列简单图和显式单位、390/768/1280 宽度、percent/bytes 双轴、
20 序列交互、SQL Classic/V1/V2 比较与双语 workspace。窄修后未再重跑完整浏览器套件。
固定源码的正式私有流水线已成功构建并部署 `0.2.7`，同源 native token/local 门禁、完整 Go 测试、139 项前端单测与嵌入构建均通过；
现场已核实版本/源码、受保护的非 root 进程、保留的 Admin 会话、五个新鲜业务实例、15 天保留、中文运行时长说明与风险线。
这属于**中间版本验收**：实际自身指标图例仍显示原始名字，已定位为已知指标映射遗漏上述三项自身指标。
三项映射已在 `ed444317` 仅前端补齐，最终线上真实图例仍待核验。
本次源码尚无新 tag、公开五平台归档或新 CI 成功证据。

此前已公开的父仓库 `77796f9c` 固定 server `85630554`（v0.2.6）、Go SDK `4f63688e` 和 Rust SDK `f38673c6`。
其 [精确组合 CI](https://github.com/neko233-com/metrics233-all/actions/runs/39596963802) 已通过，包含 Go/Rust SDK 对固定服务端 `85630554` 的真实集成。
此前 `f83d328e` / Rust `bda47165` 的 [父仓库 CI](https://github.com/neko233-com/metrics233-all/actions/runs/39593382915) 已通过，
包含两个 SDK 对相同固定服务端的真实集成；结果保留该旧组合归属。
此前 v0.2.6 修复通知超时分类竞态；该版本最终 Linux 定向 vet/race **320 次顶层执行通过、零跳过、零 race**。
此前运行代码的浏览器 **93 通过 / 2 SQL 环境跳过**、真实数据库双轮（SQL 浏览器 **6/6**）、
最终 Docker runtime 双轮、80 次定向 Linux race 保留原源码归属；bootstrap 在 umask 022/077 下 **20 次通过、零跳过**。
[v0.2.4 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39587248305) 因通知超时竞态失败；
[v0.2.5 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39589926276) 的 Docker 已通过，整轮因被新版取代而取消。
此前 [v0.2.6 server CI](https://github.com/neko233-com/metrics233-server/actions/runs/39593305372) 两个 job 均成功：
全库 vet/race 的 20 个有测试 package、前端 120 项、发布脚本 3 项、浏览器 **93 通过 / 2 SQL 环境跳过**和 Docker 双轮均完成。
浏览器跳过不算通过；此前真实数据库验收保留原源码归属。v0.2.6 已正式公开，7 个官方下载文件均匹配已验收构建与 asset digest。

此前生产 v0.2.6 已通过部署、账号密码切换、正常重启持久化与真实登录浏览器验收；旧管理 Token 已清空，
独立上报凭据与加密密钥保持不变。1 个网关与 4 个逻辑服重启后仍每 15 秒正常上报；实时模板绑定、
15 天保留、数字运行时长和主机阈值均已核实。SQL Entity 列表完整加载 **106/106**，按平均耗时降序排列；
权威零玩家快照明确显示空地区列表。按用户要求，不执行真实测试账号登录/退出，也不再以提供测试账号作为后续前提；
未声称完成非零玩家生命周期或地区分布链路验收。
飞书真实时钟验收仅发送 1 条分组告警和 1 条分组恢复，均有成功回执；
一小时冷却内的第二周期没有重复告警或孤立恢复。线上验收与公开发布、持续负载容量分别记录；
此前失败升级及 v0.2.3 回滚保留为历史证据。
此前 v0.2.4 双构建与 Linux 归档验收不冒充新版产物结果。

server v0.2.2（`ecdf079`）[标签 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39562946865) 已全部成功：
全库 vet/race、浏览器 86 passed / 2 SQL 环境跳过、真实数据库与生产运行 Docker 双轮通过。
五平台双构建及 7 个官方下载文件哈希匹配；线上独立上报凭据的 SDK 写读、正常重启持久化、飞书真实回执均已验收。
更新后子模块组合 `c306889` 的父仓库 CI 通过。

此前 server v0.2.1 从干净提交 `f0244c3` 构建两轮，7 个交付文件逐字节一致，官方资产回下载哈希全部匹配；
实际 Linux amd64 归档通过鉴权、业务写入查询及优雅关闭。父仓库 `813c381`、SDK v0.1.0 tag 和
[server v0.2.1 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39541420544) 全部通过，包含完整 vet/race、浏览器与 Docker 检查。
独立私有部署也已通过真实 SDK 写入查询及正常重启持久化验证；范围与证据见下方验收台账。

线上域名、SSH、Express233/构建平台地址、环境配置与凭据仅放在独立私有部署仓库，不进入这四个开源仓库。发布前运行 `go run ./tools/check-public/main.go --root . --history --require-submodules --allow-synthetic-test-ips`；实际部署值可用私有 deny-file 加验，输出不会回显命中值。

[执行计划](PLAN.md) · [验收台账](docs/status.md)
