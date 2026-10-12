# Grafana 13.2.3 接收器实现设计

状态：设计，尚未实现多集成接收器。核对日期：2026-10-12。现有 webhook、模板、分组、静默、抑制代码保持冻结；本文件不表示新增渠道已经可用。

## 固定版本与源码复用

Grafana OSS `v13.2.3` 固定 `github.com/grafana/alerting v0.0.0-20260805100035-e1a167a201a8`。Grafana 根模块虽然声明 Alertmanager `v0.33.0`，实际用 replace 指向 `github.com/grafana/prometheus-alertmanager v0.25.1-0.20260225120258-18275ca76b0c`；alerting 模块自己的声明是 AM `v0.28.2`，也使用同一 fork。MetricsPanel233 使用上游 AM `v0.34.1`。[Grafana 固定依赖](https://raw.githubusercontent.com/grafana/grafana/v13.2.3/go.mod)、[alerting 固定依赖](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/go.mod)。

官方可复用入口是 `receivers/<kind>/<version>.Factory`，提供 `ValidateConfig` 和 `NewNotifier`；`receivers.Manifest` 绑定类型、各版本 schema 与工厂。公共参数包含模板、图片提供器、HTTP/邮件发送器、组织 ID、日志和版本号。`notify.BuildReceiverIntegrationsWithManifests` 是 Grafana 的组装入口，但不适合直接接管本项目的持久队列。[工厂契约](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/factory.go)、[组装源码](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/notify/factory.go)。

不能把该模块当作已经验证可直接导入的 AM 0.34.1 插件。依赖模块内的 replace 不会替根模块生效；已核实一个具体 API 差异：Grafana `templates.TmplText` 给 `notify.GetTemplateData` 传入 go-kit 日志接口，上游 AM 0.34.1 该参数是 `*slog.Logger`。AM 0.34.1 的 `types.Alert` 仍是 `alert.Alert` 的别名，不能把这个别名误报成不兼容原因。更多差异需下一批在隔离模块编译验证，当前仅完成源码审读。[Grafana 模板调用](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/templates/template_data.go)、[AM 0.34.1 模板辅助函数](https://raw.githubusercontent.com/prometheus/alertmanager/v0.34.1/notify/util.go)、[AM 类型别名](https://raw.githubusercontent.com/prometheus/alertmanager/v0.34.1/types/types.go)。

本项目 LICENSE 当前是 Apache-2.0，官方 grafana/alerting 模块是 AGPL-3.0，部分文件同时保留上游 Apache 来源及 Grafana 修改声明。直接导入或复制修改官方实现需要明确对应源码、修改记录、许可与发布材料；本设计不改 LICENSE，也不认定只保留本项目 Apache 声明就已满足官方代码复用条件。[官方许可](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/LICENSE)。

实现路径分两层：核心 receiver/队列迁移先独立落地；协议实现优先评估固定官方工厂的适配版本。适配版本必须继续使用 AM 0.34.1，不能用全局 replace 把已验证的抑制、模板和其他模块一起降到 Grafana fork。若适配成本或许可发布路径不成立，就按同样协议与 settings 契约独立实现对应 adapter；不把返回成功的占位器列为原生支持。

## 固定目录、设置与密钥

官方注册了 24 个 canonical 类型。其中 22 个当前 `v1` 版本可新建；`LINE` 当前仍是 `v1`，但 deprecated 且 `CanCreate=false`；`wechat` 只有 `v0mimir1`，也不允许新建。目录必须保留大小写、版本、别名、弃用与可创建标记，不能把枚举数量当成已实现数量。[注册表](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/notify/schema.go)、[类型键](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/schema/known_types.go)。

下表列当前版本的协议字段；密钥列来自对应配置解密与 schema。实现时以每版本的完整 schema 校验必填项、类型、选项、模板字段和条件，不以本表的简写替代验证。除了旧 webhook 的兼容视图，密钥字段从 public settings 移入加密 secure_settings，GET 只返回 secure_fields。具有凭据的 URL、任意私密 headers 也按本地加密规则处理，不能因为官方个别字段未标 Secure 就泄漏。

| 类型 / 当前版本 | 公共或内容设置 | 官方密钥路径 / 特殊点 | 固定配置源码 |
| --- | --- | --- | --- |
| prometheus-alertmanager / v1 | url（多地址）、basicAuthUser | basicAuthPassword；发送 AM `/api/v2/alerts` 数组 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/alertmanager/v1/config.go) |
| dingding / v1 | msgType、title、message | url（带机器人凭据） | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/dingding/v1/config.go) |
| discord / v1 | title、message、avatar_url、use_discord_username、use_embed_description | url；有图片/附件协议 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/discord/v1/config.go) |
| email / v1 | addresses、singleEmail、subject、message | 联系点无 SMTP 密钥；另一个发送配置持有 SMTP 认证/TLS | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/email/v1/config.go) |
| googlechat / v1 | title、message、hide_open_button、hide_version_info | url | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/googlechat/v1/config.go) |
| jira / v1 | api_url、project、summary、description、labels、priority、issue_type、reopen_transition、resolve_transition、wont_fix_resolution、reopen_duration、dedup_key_field、fields | user、password、api_token；Basic/PAT 互斥；查询/创建/更新/恢复多步骤 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/jira/v1/config.go) |
| kafka / v1 | kafkaRestProxy、kafkaTopic、description、details、username、apiVersion、kafkaClusterId | password；是 REST Proxy v2/v3，不是原生 Kafka wire producer | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/kafka/v1/config.go) |
| LINE / v1 | title、description | token；别名 line；deprecated，不允许新建 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/line/v1/config.go) |
| mqtt / v1 | brokerUrl、clientId、topic、message、messageFormat、username、qos、retain、tlsConfig | password、tlsConfig.caCertificate/clientCertificate/clientKey；QoS 0–2 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/mqtt/v1/config.go) |
| oncall / v1 | url、httpMethod、maxAlerts、authorization_scheme、title、message | username、password、authorization_credentials；官方当前显示名为 Grafana IRM | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/oncall/v1/config.go) |
| opsgenie / v1 | apiUrl、message、description、autoClose、overridePriority、sendTagsAs、responders | apiKey；恢复关闭与 responder 结构需保留 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/opsgenie/v1/config.go) |
| pagerduty / v1 | severity、details、class、component、group、summary、source、client、client_url、url | integrationKey；密钥在实际协议请求体中 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/pagerduty/v1/config.go) |
| pushover / v1 | priority、okPriority、retry、expire、device、sound、okSound、uploadImage、title、message | userKey、apiToken；优先级和图片协议 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/pushover/v1/config.go) |
| sensugo / v1 | url、entity、check、namespace、handler、message | apikey | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/sensugo/v1/config.go) |
| slack / v1 | endpointUrl、recipient、text、title、username、icon_emoji、icon_url、mentionChannel、mentionUsers、mentionGroups、color、footer | url、token；incoming webhook 与 bot API 两种路径 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/slack/v1/config.go) |
| sns / v1 | api_url、topic_arn、target_arn、phone_number、subject、message、attributes；sigv4 内 region/profile/role_arn | sigv4.access_key、sigv4.secret_key；AWS 签名及凭据链 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/sns/v1/config.go) |
| teams / v1 | url、message、title、sectiontitle | URL 可能包含密钥，本地必须加密；不能把其他渠道 webhook JSON 当作 Teams card | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/teams/v1/config.go) |
| telegram / v1 | chatid、message_thread_id、message、parse_mode、disable_web_page_preview、protect_content、disable_notifications | bottoken；整数/string ID 不经 float64 舍入 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/telegram/v1/config.go) |
| threema / v1 | gateway_id、recipient_id、title、description | api_secret | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/threema/v1/config.go) |
| victorops / v1 | messageType、title、description | url | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/victorops/v1/config.go) |
| webex / v1 | message、room_id、api_url | bot_token | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/webex/v1/config.go) |
| webhook / v1 | url、httpMethod、maxAlerts、authorization_scheme、title、message、headers、payload、tlsConfig、hmacConfig | username、password、authorization_credentials；TLS 的三项证书/密钥、hmacConfig.secret | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/webhook/v1/config.go) |
| wechat / v0mimir1 | api_url、corp_id、message、message_type、agent_id、to_user、to_party、to_tag、send_resolved、http_config | api_secret 和旧 http_config 密钥；不允许新建 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/wechat/v0mimir1/config.go) |
| wecom / v1 | endpointUrl、agent_id、corp_id、msgtype、message、title、touser | url、secret；机器人 webhook 与 APIAPP 两种路径 | [config](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/wecom/v1/config.go) |

`teams` 另有旧 `v0mimir1`/`v0mimir2`，分别接受别名 `msteams`/`msteamsv2`。其他 manifest 中声明的旧 Mimir 版本也要保留其 schema；不能把旧版本 settings 悄悄改成 v1，也不能只因类型别名相同便使用当前工厂。旧版本导入、修改和执行在对应 adapter 通过测试之前明确返回不支持。[Teams manifest](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/teams/schema.go)、[版本 schema](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/schema/schema.go)。

v1 的公共 `http_config` 当前是 OAuth2 客户端凭据配置：client_id、client_secret、token_url、scopes、endpoint_params，以及 token 端点的 tls_config/proxy_config。其 secure 路径包括 `http_config.oauth2.client_secret` 与嵌套 TLS 证书/密钥。它和旧 Mimir 完整 HTTP client config 不是一个格式。目录和 validator 应分别处理。[v1 HTTP 配置](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/http/config.go)、[OAuth2 配置](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/http/oauth2.go)。

## 原生模型与 API

逻辑接收器与具体集成分开：

```text
Receiver { uid, name, version:int, updated_at, integrations:[] }
Integration { uid, type, config_version:string, revision:int,
              disabled, disable_resolve_message, settings:RawMessage,
              secure_fields:map[string]bool }
IntegrationInput adds secure_settings:map[string]string, clear_secrets:[]string
```

receiver version 是整组配置的 CAS；config_version 是官方协议 schema 版本；revision 是不可变配置/密钥快照版本。settings 使用 RawMessage/UseNumber，保留大整数和嵌套 fields；已知未实现字段返回明确错误，不能接收后丢弃。密钥缺省保留、明确清空才删除，整组保存和删改集成在一个事务完成。AAD 包含组织、receiver UID、integration UID、revision；密钥不能跨组织或跨集成复用密文。

拟新增原生 receiver CRUD、catalog、test 和只读 preview；写操作必须带 receiver version，删除也要带版本。Grafana AppPlatform 映射 `Receiver.spec.title` 与 integrations 的 uid/type/version/settings/secureFields/disableResolveMessage。旧 provisioning contact-points 仍按 integration UID 一条记录，name 指向所属逻辑 receiver。导入必须通过类型/版本工厂验证。[Grafana Receiver spec](https://raw.githubusercontent.com/grafana/grafana/v13.2.3/apps/alerting/notifications/pkg/apis/alertingnotifications/v1beta1/receiver_spec_gen.go)。

建议原生路由固定为 `/api/v1/alerting/receivers` GET/POST、`/{uid}` GET/PUT/DELETE、`/{uid}/test` POST、`/preview` POST，以及 `/api/v1/alerting/receiver-catalog` GET。创建 version=0，PUT/DELETE 必须携当前整数版本；404/409/400 沿用通知错误格式。test 返回父 dispatch 和每 integration 的子投递列表；preview 不入队、不联网、隐藏密钥。`/api/v1/alerting/notifications/{id}` 的旧 ID 留给兼容记录，新子投递使用独立路径 `/api/v1/alerting/receiver-deliveries/{id}`，避免数字 ID 发生歧义。

catalog 至少返回 canonical type、aliases、versions、currentVersion、canCreate、deprecated、fields、secure paths、implemented、supported_features。目录中的未来渠道可显示为未实现，不能创建一个只会返回成功或通用 webhook 的假渠道。

SMTP 是单独的发送配置，不把服务器密码复制到每个 email integration。拟用 org-owned、版本化 SMTP profile 存储 host、认证、from/ehlo、StartTLS 策略、TLS、content types、静态 headers/BCC；密钥加密，profile revision 被投递引用。Grafana 全局 SMTP 配置的导入由实例管理员配置共同 profile，再显式授权组织使用；普通组织编辑不能读取实例 SMTP 密钥。这一隔离方式属于本项目设计，不声称与 Grafana 全局 SMTP 配置文件格式完全相同。

## 旧单 webhook 的迁移

每个旧 contact point 迁移为同 UID 的 receiver，含一个同 UID 的 webhook integration。native policy、每规则 override 和 group receiver UID 无需改名。迁移记录以 org-local schema revision 标记，事务和 UID 唯一约束保证重启幂等。

旧重复 display name 不自动合并：自动合并会改变 UID 路由和通知数量。保留 native UID 引用；Grafana 通过歧义名字导入仍明确报错，后续由普通编辑操作改成唯一名字。新建逻辑 receiver 的路由名字要求唯一；迁移过程中不能添加会使旧库无法打开的全局 unique(name) 约束。

旧 outbox 的 ID、状态、重试次数、原始 JSON、模板 bundle/render snapshot 和历史记录保留。迁移可给它建立单一子投递映射，但执行仍走 legacy-webhook 模式：保持旧默认 payload、稳定 header/ID，以及当前 destination/credentials 修复后手动重试的行为。旧无模板记录不自动切换 Grafana 默认模板。

旧 contact-point API 保留单集成视图；升级为多个集成后，旧写入口必须拒绝无法完整表达该 receiver 的修改，不能用一次旧 PUT 删除其他集成。Grafana integration UID 的单项更新则应在所属 receiver 的 CAS 内执行。

## 表与持久化边界

| 新表或变更 | 键与主要内容 | 目的 |
| --- | --- | --- |
| alert_receivers | uid PK、name、version、config、updated_at | 逻辑 receiver CAS；组织采用已有独立数据库 |
| alert_receiver_integrations | uid PK、receiver_uid、position、type、config_version、current_revision、disabled | UID 稳定，重排不改变去重身份 |
| alert_receiver_integration_revisions | integration_uid+revision PK、public_config、sealed_secrets、content_hash、created_at | 不可变设置和凭据版本；不能随 receiver 删除级联丢掉在途快照 |
| alert_notification_dispatches | id PK、group_key/generation、route_options、data_snapshot、template_bundle_hash、state | 一次分组通知的父记录，固定目标 integration 集合 |
| alert_notification_deliveries | id PK、dispatch_id、integration_uid、revision、state、attempts、next_at、lease_token/lease_until、receipt | 独立 claim、重试、成功确认；unique(dispatch_id,integration_uid) |
| alert_notification_delivery_operations | delivery_id+operation_key PK、sealed_request、state、receipt、attempts | 多地址、SMTP 多收件人、图片上传、Jira 等多步骤的检查点 |
| alert_notification_integration_groups | group_key+integration_uid+channel_epoch PK、last_sent_at/hash、resume_needed、inflight_id | 分组间隔、重复间隔按渠道独立，不以父记录成功替代子渠道成功 |
| alert_notification_instance_receipts | integration_uid+channel_epoch+rule_uid+instance_key+started_at PK、last_firing_receipt、notified | 恢复只发给同一 active period 已成功通知的渠道；不依赖普通历史 |
| schema_migrations | org-local migration key、applied_at | 保证旧数据迁移只执行一次 |

至少建立 delivery 的 `(state,next_at,id)` 到期索引、`(integration_uid,channel_epoch,rule_uid,instance_key,id,state)` 顺序索引、dispatch/integration 唯一键及所有 snapshot 引用索引。单次 claim/reconcile 批次与操作数有界；每 receiver 最多 32 integrations、每子投递最多 32 operations；多收件人超限在保存/prepare 时明确失败，不丢掉后面的收件人。继续限制两名外部投递 worker，不能一次 receiver fanout 启动所有远程连接。

父数据快照和模板 source bundle 不能含联系方式密钥。某些协议把凭据写进 body（如 PagerDuty/Pushover），实际 prepared request 必须加密存储；只给 URL/headers 加密仍不够。HTTP status、固定错误码、耗时等公开状态与 provider receipt 分开，receipt 只保存后续步骤需要的非密钥标识。

FK 和清理以实际引用为准：pending/sending/failed 可手动重试的子投递、未结束操作、仍活跃且已通知的 period 都保留关联配置、密钥、模板和去重记录。父 dispatch 不能被普通 outbox 的 10k 历史删除级联清掉。活跃 period 与未完成投递的锚点独立保留；终态历史按现有 10k 策略清理。集成删除先 tombstone 并取消未开始的投递，正在发送的结果仍通过原 lease CAS 完成。

## 调度、快照、成功与恢复

1. 当前 route/group 过滤继续共用已验证的 suppression：先按当前 active 数据检查 inhibition、silence、mute timing，再生成可见成员。父 dispatch 固定路线选项、数据、模板 bundle、integration UID/revision 和目标集合。
2. 每 integration 使用自己的 last hash/sent time/成员成功状态。子渠道 A 成功、B 失败时，B 的重试不重新运行 A。真正的新成员、恢复、抑制解除或配置变更生成新的 dispatch，按各渠道自己的历史决定是否需要发送。
3. 恢复记录在 enqueue 时可以存在，但执行资格要等先前 firing 子投递的确认。最终未成功的渠道不收到 resolved；不能因为 resolved 入队时尚无 firing ACK 就丢弃随后会成功的恢复。已在抑制期间恢复的成员继续合并取消，禁止后补过期事件。
4. 集成 type 和 schema version 对 UID 不可直接改写；删建使用新 UID。收件目标改变开启新的 channel_epoch；仅模板或认证轮换不误判为已经通知新目标。原目标 ACK 可决定原 active period 的恢复资格；删除/停用渠道属于显式取消，不继续偷偷发送。
5. 新投递的 public config、原模板 source、渲染内容和 secret revision 一起固定。自动重试不能把 v1 Slack 改成 Teams，也不能渲染刚修改的模板。凭据修复采用明确的 retry-with-current-credentials 操作，只允许同一协议和目标，记录新凭据引用；默认 retry 使用已固定版本。旧 legacy-webhook 单独保持现有语义。
6. HTTP/SMTP/MQTT 执行前再次验证 lease 和 suppression；网络已经开始后不能撤回。HTTP 请求体/SMTP MIME/MQTT payload 固定，认证时间戳、AWS 签名和 HMAC 可按每次尝试生成，不把时间签名当作数据快照变化。
7. 成功 ACK 与进程崩溃之间仍有不可消除的歧义，整体是 at-least-once。HTTP/SMTPMessage-ID/provider dedup key 保持稳定；只有对端明确支持时才依赖它去重。数据库 CAS 并不提供网络 exactly-once。[AM 分渠道 pipeline](https://raw.githubusercontent.com/prometheus/alertmanager/v0.34.1/notify/notify.go)。

## 适配器执行契约

拟新增 `internal/notificationreceiver`，注册表按 type+config_version 查找 adapter，不引入新的 Alertmanager 调度器：

```go
type Adapter interface {
    Validate(public json.RawMessage, secretFields map[string]bool) error
    Prepare(ctx context.Context, input FrozenInput) (PreparedOperations, error)
    Send(ctx context.Context, operation PreparedOperation, credentials CredentialRef) (DeliveryResult, error)
}
// DeliveryResult: retryable, public_code, status_code, retry_at, receipt.
// PreparedOperation: stable operation_key; immutable/sealed request reference.
```

官方 `notify.Notifier.Notify` 的 bool 是 recoverable，不是 success。adapter 以 err==nil 判成功，以明确的协议状态和 recoverable 信息决定错误重试，不能把 bool=false,nil 当成失败，也不能把 bool=true,error 当成成功。

复用官方 `NewNotifier` 时要注入我们持久化的 sender；官方 `WebhookSender` 支持响应 validation callback，不能只凭 HTTP 200 判成功。默认 EmailSender 的 SendEmail 忽略 context，Send 只返回总数/聚合错误，没有持久化的逐收件人 ACK，因此不能直接承担本项目 SMTP 队列。[HTTP sender 接口](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/webhook.go)、[邮件实现](https://raw.githubusercontent.com/grafana/alerting/e1a167a201a8/receivers/email_sender.go)。

模板仍用本项目同步、有步骤和数据规模限制的 renderer。官方 notifier 内部再次执行用户模板会破坏这一保证：要先逐个冻结全部模板字段，适配官方工厂时将已渲染文本安全编码为常量表达式，避免标签中的 `{{...}}` 被第二次执行；或在兼容版本内把渲染入口换成受限接口。仅给 text/template 包 goroutine+timeout 不算执行边界。每渠道操作总输出也需要上限，不能把多字段各自限额相加成无界输出。

Common transport 保留 1–30 秒的操作超时并限制总流程时长，小于 lease。SMTP 用可取消 Dial/连接 deadline，取消时关连接；4xx 暂时错误重试，5xx 终止。HTTP 对 network/5xx/408/425/429 重试，读取并限制 Retry-After；其他状态与 provider validation 分别分类，持久化错误不能带 URL token、认证值或原始 response body。

Slack bot 的 HTTP 200 `ok:false`、Kafka REST 的单条记录错误、SNS 的签名/错误文档、Telegram API 的语义错误都必须真实判断。SMTP singleEmail=false、Alertmanager 多地址、Slack 图片上传/Jira 查询与状态变更需要 operation ACK；单个 integration 内的部分成功也不能在重启后全部重新发送。图片能力还依赖实际 renderer/images provider，不因存在上传字段就宣称截图已经实现。

## 下一批可直接实施的函数和顺序

第一批只做多 webhook 核心与迁移，避免同时引入 24 种网络协议：

- `Store.initReceivers` / `migrateLegacyContactPointsTx`：追加表、幂等迁移、旧投递单子映射，保留原 render snapshot。
- `Store.SaveReceiver` / `DeleteReceiver` / `ReceiverRevisionsTx`：整组 CAS，密钥保留/清空，UID 和版本校验、tombstone。
- `snapshotReceiverDispatchTx` / `enqueueIntegrationDeliveriesTx`：固定目标集合，绑定已有 bundle，唯一键消除重复入队。
- `ClaimIntegrationDelivery` / `ValidateIntegrationClaim` / `FinishIntegrationDelivery`：独立 lease token、分渠道顺序、重试与 ACK；复用 current suppression，不另写一套 matcher。
- `reconcileIntegrationGroupsTx` / `recordFiringReceiptTx` / `filterResolvedForIntegrationTx`：把旧单个 Notified/lastHash 拆成按渠道的成功记录。
- `pruneReceiverArtifactsTx`：根据未完成子投递与活跃 period 引用清理 revision/模板/操作，保留人工重试所需资料。
- `receiverRoutes` / `receiverProvisioningRoutes`：native CRUD/catalog/test/preview，Grafana group 与 integration 视图；原 contact-point 兼容写入口防止数据丢失。

第二批完成完整 webhook v1 和公共 OAuth2/TLS：POST/PUT、认证互斥、maxAlerts、custom payload/vars、HMAC、证书；旧默认 payload 保持兼容。custom payload 可是任意文本，不能强行作为 JSON 重编码或注入 notificationId，稳定 ID 走请求 header。

第三批接邮件与常见 HTTP 渠道，再按协议小批扩展。每批登记实际 implemented 类型/版本/字段及测试证据；全部 24 个类型名称不等于所有功能通过。旧不可新建版本完成导入执行后仍遵守官方 canCreate=false。

## 验收

Windows 本地 httptest/SMTP fixture 先验证，桌面浏览器测试用真实 Node 接收端；需要协议服务时复用已有单一小 Docker fixture，不能为各渠道新建一套容器。

核心必须覆盖旧库/旧已渲染投递/旧无模板投递迁移，重复重启，跨组织密钥与 UID 隔离，A 成功+B 503+B 恢复不重发 A，同类型两个 integration 重排不重发，partial SMTP/多地址成功恢复，旧 credential/body/template 冻结与明确 refresh，单渠道 disableResolve，未成功渠道不收到 resolved，正常恢复与抑制期间恢复，组成员变化/重复间隔/抑制解除并发，删改 receiver 与 worker lease 竞态，历史清理后 ACK 和手动重试仍可用。

协议适配按固定官方单元样例及真实捕获请求断言格式、限长、数值精度、凭据和 provider 语义；preview、GET、错误、历史、模板数据均不能出现密钥。官方模块的编译适配、完整默认模板/命名空间函数、图片、旧 Mimir schema、SMTP/MQTT/SNS 等各自列明确结果，未验收项继续标未实现。
