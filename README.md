# StuHelper Platform

StuHelper 公共 Go 框架。当前提供模块依赖验证、Fx 生命周期、HTTP 访问边界、远程模块接口核对和 OpenAPI 类型生成。资格规则与模块安装历史使用独立 PostgreSQL schema。具体业务和真实身份适配器由业务模块提供。

## 本地运行

需要 Go 1.27.3 与 Node.js 24.22.1。

```bash
make setup
bash scripts/check-with-postgres.sh
make run
```

默认监听 127.0.0.1:8080，可通过 STUHELPER_LISTEN 修改。STUHELPER_MODULES 默认 platform。公开示例尚未安装其他业务模块，未知模块配置会失败。

`/healthz` 检查进程存活，`/readyz` 检查显式声明的关键模块及其必需依赖，`/api/v1/system/capabilities` 返回可用能力。能力列表不表示用户获得权限。管理接口在身份适配器接入前返回 identity_unavailable，不签发或接受演示管理员身份。

## 模块开发

module.Definition 声明标识、构建版本、接口主版本、必需与可选依赖、能力级依赖和生命周期。application.Modules 接收模块及其路由，application.New 统一组装。接口示例位于 examples/extension，可单独执行：

```bash
go run ./examples/extension
```

该示例只返回合成状态，不包含在默认示例中。模块启停在首期通过配置与重启生效，没有上传代码或在线热加载入口。模块拥有自己的数据，依赖方不能直接读写其他模块的表。

module.Remote.Definition 的通用连接实现位于 module/remote.go。它核对固定端点的接口版本。后台探测保存短时健康状态，暂时不可达时组合仍可启动，相关能力暂停。配置或已识别的协议错误使启动失败。远程连接按实际部署需要使用；私有业务模块也可静态编译进 backend，真实身份和业务接口在所属仓库开发。

## 契约

公共接口源文件是 contracts/platform.openapi.yaml，远程模块握手约定是 contracts/module.openapi.yaml。

```bash
make generate
make check-generated
```

使用 oapi-codegen v2.8.0 和 openapi-typescript v7.13.0，生成文件禁止手改。Web/Admin 独立保存生成快照，跨仓库检查由架构仓库负责。正式模块依赖固定已发布版本。私有 stuhelper-backend 承载产品 main 入口；本仓库的 examples/server 用于公开检查和演示。

架构、规格与工作区入口见 [StuHelper Next](https://github.com/StuHelper/stuhelper-next)。

modulehistory 提供独立于启用状态的安装事实接口。PostgreSQL 的 platform schema 保存资格规则、安装历史、幂等响应和独立审计。初始化只记录经核实从未安装的模块，不能覆盖已有事实；安装记录单向更新。记录缺失或数据库故障时按未知处理。
资格规则缓存最长 5 秒、最多 4,096 项。本进程修改后立即失效，包括提交结果不明的失败；其他实例在 5 秒内失效。过期缓存读取失败时返回不可用，不使用旧值。安装历史每次读取数据库，不缓存从未安装状态。

审计独立保存 12 个月，幂等响应保存 24 小时。Cleanup 每轮最多删除 100 条审计、100 条资格响应和 100 条授权响应，限时 5 秒；配置更新不受历史条数限制。当前配置和安装事实随部署数据保存。

运行身份只获得 platform schema 的 USAGE、表 SELECT/INSERT/UPDATE/DELETE 和序列 USAGE；audit、eligibility_requests 与 admin_requests 禁止 UPDATE，admin_roles 禁止 UPDATE 和 DELETE。迁移身份单独管理。make check 必须提供隔离的 STUHELPER_PLATFORM_TEST_DSN，CI 自动创建临时 PostgreSQL 集群。


## 管理角色与操作记录

admin 提供不可变角色定义、学校／群／业务模块授权、即时授权求值和跨模块操作记录汇总查询。接口为 contracts/admin.openapi.json，公开求值入口为 admin.Service.Evaluate。角色模板不自动授予能力，platform 内置自身审计提供方，其他模块经公开接口接入；响应列出已查询和缺失模块。

宿主通过 admin/httpapi.Handler.Register 装配路由，并提供当前身份及会话／CSRF 校验。公开示例未装配管理员身份。权限、数据库配置、AO 场景及 backend 后续接入步骤见[管理角色实现说明](docs/admin-operations.md)。

资格动作 resources.purchase 与 membership.purchase 默认要求 campus_eligibility，登录由调用方已验证身份保证。两个动作独立配置全站规则与学校覆盖，购买条件调整不改变下载规则或既有权益。配置 API 的动作目录由 eligibility OpenAPI 0.2.0 与生成 SDK 描述。
