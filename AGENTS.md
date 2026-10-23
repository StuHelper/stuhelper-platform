# 工程约定

- 本仓库是 StuHelper 按业务模块拆分的一部分。架构与业务边界以 https://github.com/StuHelper/stuhelper-next 为准。
- 普通变更从最新 origin/develop 创建专用短期分支和独立 worktree，通过 PR squash 到 develop。发布走 develop 到 main 的 merge commit；tag、镜像发行与生产晋级需要 owner 明确授权。
- 普通检查使用 ubuntu-26.04。Docker/OCI 构建和 BJ1 Registry 操作固定使用 StuHelper 自建 runner。不修改其他仓库、生产数据或运行配置来完成普通开发任务。
- GitHub Actions 与部署镜像使用明确版本 tag。摘要与源码提交用于核验和追溯。当前 CI 不使用 GitHub Actions cache 或 artifacts。
- 接口先定义 OpenAPI，生成文件不得手改。模块只访问自己的数据，通过明确接口与其他模块协作。私有实现、凭据和身份原始数据不得进入公共仓库。
- 业务后端遵循 Handler → Service → Repository，SQL 仅位于 Repository。客户端使用生成接口类型，不实现服务端权限规则。
- 记录实际检查范围；框架测试通过不表示业务、真实登录、生产部署或迁移通过。发现失败先修复并重跑原检查。
- Docker 测试只使用任务专属资源，完成或失败后清理本任务创建的容器、无用镜像及缓存，不做全局清理。
- 保留其他任务的未提交修改。当前只搭框架，不实现业务；原生端与社区模块搁置。
