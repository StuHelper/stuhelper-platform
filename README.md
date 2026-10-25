# StuHelper Platform

StuHelper 公共 Go 框架。当前提供模块依赖验证、Fx 生命周期、HTTP 访问边界、远程模块接口核对和 OpenAPI 类型生成。具体业务、真实身份适配器和持久化尚未实现。

## 本地运行

需要 Go 1.27.3 与 Node.js 24.22.1。

```bash
make setup
make check
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
