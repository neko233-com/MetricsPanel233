# MetricsPanel233 接续计划

记录日期：2026-10-13（北京时间）。今晚收尾、验证并 push 当前源码；明天从本文继续。

## 验收范围

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

## 明天第一步

1. `git status --short`、`git log -3 --oneline`，核对收尾提交的 GitHub Actions 是否全绿。若有失败，先复现、修复并重跑，不跳过检查。
2. 阅读 docs/grafana-product-parity.md、grafana-compatibility.md、grafana-resource-acl-contract.md、grafana-identity-http-contract.md。
3. 衔接真正的 resource mutation HTTP policy：按已注册路由逐一接 dashboard/folder/library/manual annotation。先真实解析目标与 body，检查相应 action，再用已有 guarded helper 在同一事务保存。未知路由、History/Query/Live 仍不能因 read 策略而放行。
4. 为权限管理、历史、数据源依赖执行、SDK proxy/chunked/Live 补完整 action matrix、过滤和撤权验证；通过 migration/readiness 后再接 CLI 多用户入口和 PC 登录/管理。

## 通知 native 接管顺序

1. 完成 Prepared webhook 实际 HTTP transport，保留真实 wire、重试、超时、不跟随重定向和发送前/ACK 检查。
2. Contact point/template/routing/provisioning 全部使用同一个有效 receiver catalog，legacy 编辑与 receiver revision 在同一事务协调；不能依靠一次迁移快照。
3. 同一 scheduler 内持久化 drain：阻止新 legacy claim，让已经开始的发送/检查点结束；生产者继续记录。最后一个 writer 事务重新协调配置和全部 unfinished outbox，完成 adoption 后才改变 owner/epoch。配置变化使旧 readiness 失效。
4. 保留原父通知 ID、冻结来源、attempt/next retry/suppression 和历史；不得根据旧 sent/notified/http status 伪造目的地 firing ACK。旧未知 resolved 排除在渲染前，仍 firing 的恢复不能被 repeat=0/旧 hash 阻断。
5. 实际测试在途 HTTP barrier、drain 中配置修改、最终事务回滚、各崩溃阶段重启、old epoch 拒绝、sole worker 和 retry 子状态，之后接 API/CLI/Windows PC 配置。

## 后续全功能缺口

继续以产品清单和逐插件矩阵验收：其余 datasource/visualization、布局编辑器、变量 ad hoc/group-by 与 rename、完整 logs/traces/profiles Explore、histogram/exemplar/discovery 和真实大数据集群、其余 receivers/template/image/external Alertmanager、admin/team/SA/external auth、preferences/starred/playlists、公共快照/expiry/rendering、App Platform CRUD/file provisioning、Live 用户隔离和插件全生命周期。

仅做 PC 浏览器验证，优先 Edge；API/存储测试不替代真实页面操作。每阶段验证后 push，全部验收通过后再正式发布和更新安装版。
