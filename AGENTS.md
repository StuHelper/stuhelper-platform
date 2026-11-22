# 工程约定

<!-- updated: 2026-11-21 -->

- 本仓库维护 StuHelper 公共 Go 框架和平台接口。业务模块拥有自己的数据、迁移与公开接口。产品装配和生产配置由各所属仓库维护。[平台架构](https://github.com/StuHelper/stuhelper/blob/develop/docs/design/platform-architecture.md)
- 从最新 `origin/develop` 创建短期分支，任务工作树放在 `workspace/worktrees/<任务>/<仓库名>`，保留其他任务的改动。PR squash 合入 develop。发行及生产晋级按根仓授权流程执行。
- 普通检查使用 GitHub 托管 `ubuntu-26.04` 和固定版本的共享工作流。本仓 CI 通过 `scripts/check-with-postgres.sh` 运行完整检查。Docker 与 BJ1 Registry 任务使用 StuHelper 自建 runner，并使用任务专属资源。
- 锁文件固定依赖，正式 Go 检查使用 `GOWORK=off`。Actions 工作流与部署镜像使用明确版本 tag，提交和摘要用于核验。缓存规则随当前工作流和[CI 规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/ci-runners.md)维护。
- 接口先定义 OpenAPI，再用 `make generate` 更新生成文件。Handler 负责请求与响应，Service 负责编排规则，Repository 负责数据访问。身份原值与凭据留在获准私有环境。
- 以 [module.yaml](module.yaml) 的 `scenario_tests` 登记当前场景及真实测试函数。分别报告仓内检查、跨仓组合、生产部署和真实账号验收。检查失败时修复并重跑原检查，再验证受影响阶段。

## 文档与沟通

- 用现有主题说明当前行为、实现进度与验证范围。待确认的产品选择保留 InReview。正文改动同步更新 `updated`，段落不超过 200 个可见字符。
- 使用准确、自然的中文，先说明结果，再列依据和待确认事项。术语和界面国际化遵循统一规格。[术语表](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/terminology.md) · [国际化规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/internationalization.md)
- 长期技术文档使用相关写作技能。在 Goal Mode 下减少进度沟通。跨仓、许可证、交付及验证规则见[根仓工程约定](https://github.com/StuHelper/stuhelper/blob/develop/AGENTS.md)。
