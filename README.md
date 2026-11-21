# StuHelper Platform

<!-- updated: 2026-11-21 -->

StuHelper 的公共 Go 框架，提供模块装配与可用性判断、HTTP 路由、动作资格、安装历史、管理员授权和操作记录汇总。产品入口由 stuhelper-backend 装配，业务模块与身份提供方通过公开接口接入。

## 本地运行与检查

使用 Go 1.27.3、Node.js 24.22.1、pnpm 10.33.4 和 actionlint 1.7.12。完整检查还需要本机 PostgreSQL 工具。精确版本以仓库声明及[统一工具链清单](https://github.com/StuHelper/stuhelper/blob/develop/architecture/toolchains.yaml)为准。

```bash
make setup
make run
```

`make run` 启动 `examples/server`。监听地址默认 `127.0.0.1:8080`，可用 `STUHELPER_LISTEN` 配置。启用模块通过 `STUHELPER_MODULES` 指定，公开示例默认只有 `platform`。配置未知模块会导致启动失败。

`/healthz` 检查进程存活，`/readyz` 检查显式标为关键的模块及其必需依赖，`/api/v1/system/capabilities` 返回可用能力。能力列表供客户端判断功能可用性。访问权限在服务端逐次检查。

公开示例未注入身份和授权适配器，访问受保护的 `/api/v1/admin/modules` 返回 503 `identity_unavailable`。已接入身份时，通用路由分别返回 401 `authentication_required`、403 `permission_denied` 或 503 依赖故障。

```bash
bash scripts/check-with-postgres.sh
```

检查脚本创建仅监听 Unix 套接字的临时 PostgreSQL 集群，自动设置 `STUHELPER_PLATFORM_TEST_DSN`、执行 `make check` 并清理集群。非 Debian 系统可以用 `STUHELPER_TEST_PG_BIN` 指定 PostgreSQL 程序目录。直接运行 `make check` 需要调用者提供隔离测试库。

`make check` 默认使用 `GOWORK=off`，检查生成一致性、仓库规则、OpenAPI 及其兼容性、导入边界、脚本单测和 Go 格式，随后执行 vet、竞态测试、构建和工作流检查。场景与测试函数登记在 [module.yaml](module.yaml)。

`make test` 仅运行 Go 竞态测试，也需要隔离测试库。契约兼容性默认与 `HEAD` 比较，可设置 `CONTRACT_BASE_REF=origin/develop` 检查候选变更。

## 模块装配

`module.Definition` 声明模块标识、版本、接口主版本、必需依赖、可选依赖、能力级依赖和生命周期。`application.New` 接收模块、路由和访问控制适配器并通过 Fx 装配。模块启停在修改配置并重启后生效。

```bash
GOWORK=off go run ./examples/extension
```

该示例额外注册一条返回合成状态的公开路由。通用 HTTP 路由共享单次请求的可用性判断并生成请求 ID，支持带自身访问控制的显式 HEAD 路由。停机时 HTTP 使用独立排空预算，为模块停止钩子留出时间。

`module.Remote.Definition` 通过 `GET /internal/v1/module` 校验模块标识、接口主版本和约定能力，后台探测维护短期可用性状态。开始探测时遇到暂时故障，宿主仍可启动，相关能力暂停。启动探测识别到配置或协议错误时，组合启动失败。

## 契约与持久化

| 契约 | 用途 |
| --- | --- |
| [platform.openapi.yaml](contracts/platform.openapi.yaml) | 健康、能力和模块状态 |
| [module.openapi.yaml](contracts/module.openapi.yaml) | 远程模块握手 |
| [eligibility.openapi.json](contracts/eligibility.openapi.json) | 动作资格规则、学校矩阵、覆盖重置和用户预检 |
| [admin.openapi.json](contracts/admin.openapi.json) | 管理角色、授权和操作记录 |

```bash
make generate
make check-generated
```

生成脚本使用 oapi-codegen 和 openapi-typescript。接口变更先修改契约，再生成 Go 与 TypeScript SDK。Web 和 Admin 各自在快照中记录所用契约版本与提供方提交。产品依赖与生产组合由正式发行记录固定。详见[根仓发行](https://github.com/StuHelper/stuhelper/releases)。

`postgres` 在独立的 `platform` schema 保存资格规则、安装事实、审计和幂等回执。迁移身份与运行身份分开。生产宿主通过数据库就绪检查后提供流量。仓库就绪检查合并并发请求，默认缓存成功结果 1 秒。

`eligibility` 以已验证的登录账号和调用方提供的业务事实求值。资料购买和会员购买分别配置，默认都要求校园资格，调整购买条件时下载规则和已有权益各自按原规则处理。只有 `publish_course` 支持本人选课条件。

资格配置来源依次为学校覆盖、全站配置和初始默认值。规则缓存最多 4,096 项，有效期最长 5 秒。当前进程写入或重置后会使缓存失效，读取失败时返回错误。

安装事实独立于模块启停。经核实从未安装的模块可初始化为从未安装，安装事实只向已安装状态推进。每次都从数据库读取，记录缺失或读取失败时按未知处理。

管理员授权、审计留存、宿主接入和验证范围见[管理角色与操作记录](docs/admin-operations.md)。项目级设计、规格与工作区说明见[StuHelper 文档导航](https://github.com/StuHelper/stuhelper/blob/develop/docs/README.md)。
