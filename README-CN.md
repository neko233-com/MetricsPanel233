# metrics233-all

[English](README.md) · **简体中文**

单机性能监控与业务监控，拆为两个独立版本的 Git submodule：

| 子仓库 | 职责 |
| --- | --- |
| [metrics233-server](https://github.com/neko233-com/metrics233-server) | 采集、SQLite 时序存储、仪表盘、查询与告警；原生单二进制，ClickHouse 可选。 |
| [metrics233-lib-go](https://github.com/neko233-com/metrics233-lib-go) | Go 标准库 SDK：链式 Gauge/Counter、批量上报、有界队列、重试、显式启动和关闭。 |

```sh
git clone --recurse-submodules https://github.com/neko233-com/metrics233-all.git
cd metrics233-all
```

已有 checkout 使用 `git submodule update --init --recursive`。父仓库固定两个子仓库的提交，可复现同一套组合；子仓库也能单独使用。

服务构建与开发见 [服务端 README](metrics233-server/README-CN.md)，业务接入见 [业务监控说明](metrics233-server/docs/business-monitoring.md) 与 [Go SDK](metrics233-lib-go/README.md)。现有 `metricspanel` CLI、`METRICSPANEL_*` 环境变量和 HTTP 路由继续使用既有契约。

可直接下载五平台 [server v0.2.1 正式归档](https://github.com/neko233-com/metrics233-server/releases/tag/v0.2.1)，
使用附带的 `SHA256SUMS` 和 `PROVENANCE.json` 校验；独立的 [Go SDK v0.1.0](https://github.com/neko233-com/metrics233-lib-go/releases/tag/v0.1.0)
支持 Go 1.23+，没有第三方依赖。

server v0.2.1 从干净提交 `f0244c3` 构建两轮，7 个交付文件逐字节一致，官方资产回下载哈希全部匹配；
实际 Linux amd64 归档通过鉴权、业务写入查询及优雅关闭。父仓库 `328c8ca` 的 CI 和 SDK v0.1.0 tag CI 已通过；
[server v0.2.1 CI](https://github.com/neko233-com/metrics233-server/actions/runs/39541420544) 与私有线上部署分别验收，
尚未完成的项目见下方验收台账，不能用归档发布代替线上成功。

线上域名、SSH、Express233/构建平台地址、环境配置与凭据仅放在独立私有部署仓库，不进入这三个开源仓库。发布前运行 `go run ./tools/check-public/main.go --root . --history --require-submodules --allow-synthetic-test-ips`；实际部署值可用私有 deny-file 加验，输出不会回显命中值。

[执行计划](PLAN.md) · [验收台账](docs/status.md)
