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

线上域名、SSH、Express233/构建平台地址、环境配置与凭据仅放在独立私有部署仓库，不进入这三个开源仓库。发布前运行 `go run ./tools/check-public/main.go --root . --history --require-submodules --allow-synthetic-test-ips`；实际部署值可用私有 deny-file 加验，输出不会回显命中值。

[执行计划](PLAN.md) · [验收台账](docs/status.md)
