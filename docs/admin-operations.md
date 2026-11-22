---
type: development
status: InReview
updated: 2026-11-22
---

# 管理角色与操作记录

## 实现范围

Platform 提供角色模板、不可变角色定义、学校／群／业务模块授权、有效授权查询与跨模块操作记录。模块各自保存原始审计，通过公开查询接口返回脱敏摘要。规则见[管理角色与配置操作规格](https://github.com/StuHelper/stuhelper/blob/develop/docs/spec/admin-operations.md)。

管理接口以 `/api/v1/platform/admin` 为前缀。契约版本为 0.4.0，Go SDK 位于 `sdk/admin`，TypeScript 类型位于 `sdk/admin.gen.ts`。[管理操作 OpenAPI](https://github.com/StuHelper/stuhelper-platform/blob/develop/contracts/admin.openapi.json)

## 宿主与客户端接入

宿主可用 `admin/httpapi.NewRoutes` 或 `Handler.Routes` 生成 `transport.Route`，也可通过 `Handler.Register` 挂载到 `http.ServeMux`。认证适配器校验当前会话及写请求的 CSRF，错误响应携带与 `X-Request-ID` 对应的 `request_id`。

管理接口和所属业务 Service 从可信认证结果取得 `account.Principal`。业务 Service 将对象的真实学校、群和模块范围传入 `admin.Service.Evaluate`。服务端按当前规则检查每次操作，业务模块继续负责对象归属、资格和字段权限。

StuHelper v0.1.31 的生产组合包含 Platform v0.2.1、Backend v0.1.46 与 Admin v0.1.11。Backend 注入当前会话与 Casdoor 账号身份，装配角色、授权和操作记录接口。已装配的业务模块通过所属查询接口接入汇总。[根仓发行](https://github.com/StuHelper/stuhelper/releases/tag/v0.1.31)

Backend develop 使用当前会话、`content_school_id`、生效资格规则和本人事实装配 `GET /api/v1/platform/eligibility-check`。普通登录用户可预检所属站点学校的动作。匿名请求返回 401，跨校及账号被拒绝的请求返回 403。[Backend 宿主装配](https://github.com/StuHelper/stuhelper-backend/pull/164)

预检逐项返回满足、缺失和暂不可用的条件。缺少课程上下文时，选课条件标为暂不可用。业务端仍按当前事实和目标检查每次实际操作。

Admin 的角色管理页支持创建角色、查询有效授权、逐条授予与撤销，并标示平台管理员拥有全部能力。操作记录页支持来源、操作者、目标、动作与时间筛选，提示缺失模块。[Admin 访问控制页面](https://github.com/StuHelper/stuhelper-admin/tree/v0.1.11/apps/web-antd/src/features/access)

## 角色与授权

Casdoor 目标组织的有效 `IsAdmin` 提供平台管理员身份。平台管理员拥有全部能力与对象范围，包括敏感能力。其他管理员通过精确的能力标识和范围获得授权。有效账号失去平台管理员身份后按显式授权求值。账号失效时拒绝访问，身份依赖故障时返回不可用。

平台、学校、内容审核、资料、群和财务六类模板提供角色创建建议。响应的 `label_key` 采用 `platform.admin.role_template.<id>`，供客户端翻译内置模板名称。`name` 承载默认名称或自定义角色名。财务模板只建议配置读取能力。

只有平台管理员可以创建角色。角色定义上限和单账号有效授权上限各为 1,000。需要调整能力时创建新角色 ID，再替换旧授权。角色定义不可修改或删除，创建角色也不会直接产生授权。

显式授权同时匹配学校、群、模块三个维度。`*` 代表某一维度的全部范围，群标识不会推导学校权限。查询和修改使用各自的能力标识。

普通委托人需要拥有完整目标范围的 `platform.authorization.manage` 和拟授予的每项能力。只有平台管理员可委托 `platform.*` 能力。自授永远被拒绝。

授予、撤销和单账号原子替换须提交操作原因、`expected_revision` 和幂等键。角色替换将新增与撤销放进同一事务，只递增一次修订号。批量接口为最多 50 个不同账号提供 `dry_run` 预览和逐项执行。

变更前检查当前权限和预期修订号，旧修订返回 409。授权、审计和幂等回执一并提交。相同操作者与幂等键重放相同请求时，服务复核权限后返回原结果。同键对应不同请求时返回 409。

角色列表向具备任意范围 `platform.authorization.read` 的管理员分页返回角色定义。管理员反查只返回与查询条件和操作者范围相交的授权摘要。单页总计最多 1,000 个可见授权，超过上限时返回 400，可收窄范围或减少每页人数。

本人可查询自己的有效授权。查询他人需有相应范围的读取权限。服务先读取显式授权，释放数据库连接后再查询目标身份。被停用目标仍可由有权管理员查询显式授权，以便清理残留权限。

`authorization_mode=platform_admin_all` 表示平台管理员拥有全部能力，`explicit_grants` 表示按响应中的显式授权判断展示。修订号只记录显式授权变动。授权求值每次调用当前身份适配器并读取所需授权，撤销提交后发起的请求读取新授权。

角色能力名由命名空间与非空分段组成，例如 `platform.audit.read`。迁移移除格式无效的能力，清理没有有效能力的角色及相应授权，并更新受影响账号的显式授权修订号。

## 操作记录与分页

操作记录查询先将请求范围与 `platform.audit.read` 授权求交，再向获准的提供方请求数据。显式选择未获授权的模块返回 403。每页重新检查身份与授权。筛选变化返回 400，授权范围变化返回 403 并要求重新查询。

`sources` 按原始记录模块筛选，`module` 表示对象所属的业务授权范围。查询还支持目标、操作者、动作和起止时间，时间区间为左闭右开。记录包含来源、对象、操作者、时间、动作、范围、原因和结果。

结果允许 `success`、`denied`、`failed` 和 `unknown`。缺失原因用空字符串，无法确认的结果用 `unknown`。缺少可靠学校或群范围的记录只向有相应全局范围的账号开放。原始认证材料、交易载荷和内部扩展字段由提供方保留。

提供方在所属模块仓库内过滤授权范围和查询条件，按时间、来源、记录 ID 降序返回统一记录。记录 ID 在来源内保持唯一，字符串按字节比较。每页默认 50 条，最多 100 条。

汇总查询最多读取 16 个提供方，每个最多返回页大小加一条。单提供方限时 250 毫秒，整次查询限时 5 秒。提供方须响应取消，并在首轮开始时固定成员集合，避免回填记录和延迟提交记录进入后续页。

游标绑定操作者、筛选、页大小、提供方集合和授权范围。提供方快照令牌最多 256 字节，游标最多 16,384 字节。快照只记录可见性，后续页沿用同一快照和全局排序位置。

响应的 `queried_modules` 列出此次获准的提供方。某个提供方未配置、超时、快照失效或返回异常时，响应设置 `partial=true`，并在 `missing_modules` 和 `missing_reasons` 中标记。后续页继续保留该标记，获取恢复后的记录需从第一页开始。

Backend 应将应参与但暂不可用的模块显式注册为缺失提供方。主动查询获准但未注册的来源也会报告缺失。未指定来源时，`partial=false` 表示本次实际选择的提供方均成功。

## 持久化与清理

迁移身份通过 `postgres.Migrate` 按 `platform.schema_migrations` 增量执行 DDL。运行身份仅访问本模块数据。配置、角色、授权和安装事实随部署数据长期保存。审计保留 12 个月，授权与资格幂等回执以及操作记录快照保留 24 小时。

平台使用 PostgreSQL 事务 ID 和可见性快照排除延迟提交。快照保存在 `audit_snapshots`，共有 4,096 个槽位，按操作者划分 64 个桶。每项可见性文本最多 1 MiB，桶满时替换最早的快照。

查询释放只读事务的数据库连接后再保存快照，支持只有一个连接的连接池。清理审计记录与递增清理修订号在同一事务提交，分页遇到该修订号变化时将平台来源标为缺失。

`Cleanup` 每轮最多分别清理 100 条审计、100 条授权回执、100 条资格回执和 100 项过期快照，限时 5 秒。过期回执即使尚未清理也不能命中。`RetentionStatus` 查询各表过期积压，`RetentionCleaner` 可接入 Fx 生命周期。

运行身份需有 `audit_snapshots` 的增删改查权限、`audit_snapshot_slot` 的 USAGE 及 `audit_retention` 的读取与更新权限。审计表删除用于保留期清理。原始审计、回执和角色定义禁止更新，角色定义的删除只由迁移处理。

## 验证记录

独立 Platform v0.2.1 的 CI 通过生成、契约、边界、竞态测试和构建检查，数据库测试使用临时 PostgreSQL 集群。相关场景及实际函数以仓库根目录的 `module.yaml` 为准。[发布提交的 Platform 检查](https://github.com/StuHelper/stuhelper-platform/actions/runs/44614914173)

| 场景 | 测试范围 |
| --- | --- |
| AO-01、AO-04 | 授权范围求交、委托边界、查询和修改能力分别验证 |
| AO-02 | 并发修订冲突、重放防多写、回执过期和权限撤销 |
| AO-03 | 单账号原子替换、批量预览、逐项保存点隔离和幂等 |
| AO-05、PLAT-ADMIN-ALL-01 | 平台管理员敏感能力、跨范围委托和目标身份变更 |
| AO-06、PLAT-AUDIT-01 | 稳定翻页、延迟提交、失效与清理、提供方异常和缺失响应 |
| PLAT-ADMIN-01 | 角色能力名迁移、身份查询、授权审计事务和数据库隔离 |
| PLAT-CONFIG-01、PLAT-HISTORY-01、PLAT-PURCHASE-01 | 资格配置和缓存、用户动作预检、安装事实、独立购买规则 |

Admin 的浏览器用例用模拟接口覆盖按学校授予和撤销、平台管理员标识与缺失模块提示。生产槽位、匿名验收和真实账号清单由 Ops 维护。[Admin 浏览器用例](https://github.com/StuHelper/stuhelper-admin/blob/v0.1.11/tests/e2e/access.spec.mjs)

## 待完成与待验证

- Admin v0.1.11 的角色页面尚未调用原子换角色和批量授权接口，内置模板仍显示响应中的默认名称。对应界面交付与翻译覆盖待验证。
- Admin v0.1.11 的动作资格快照仍为 0.2.0，页面按学校和动作读取、提交规则。Platform 0.3.0 已提供学校矩阵、覆盖重置和预检接口。[Admin 资格契约来源](https://github.com/StuHelper/stuhelper-admin/blob/v0.1.11/contracts/platform-eligibility.source.json)
- Backend v0.1.46 缺少预检依赖，已认证且参数有效的请求返回 503。生产组合的用户动作预检和学校覆盖重置待验收。
- 生产真实管理员授权、跨模块记录覆盖及部分结果展示待按上线验收清单核对。
