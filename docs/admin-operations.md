---
type: development
status: InReview
updated: 2026-11-08
---

# 管理角色与操作记录

## 实现范围

本模块实现角色模板、不可变角色定义、学校／群／业务模块授权、有效授权求值，以及跨模块操作记录汇总查询。依据为 stuhelper-next 的 admin-operations、platform-architecture 和 data-retention 规格。

platform 拥有统一操作记录查询。各业务模块继续拥有原始审计，并与业务写入同事务提交。platform 通过公开提供方接口读取脱敏摘要，不直接访问其他模块数据库，也不复制认证材料或交易载荷。

## 接口与装配

OpenAPI 0.2.0 位于 contracts/admin.openapi.json。Go SDK 位于 sdk/admin，TypeScript 类型位于 sdk/admin.gen.ts。生成与兼容性检查纳入 make check。

宿主使用 admin/httpapi.Handler.Register 注册 /api/v1/platform/admin 下的路由。Authenticate 必须校验当前会话，并在写请求中校验 CSRF。缺少身份适配器时返回不可用，HTTP 请求不能声明平台管理员身份。

公开 Go 接口为 admin.Service.Evaluate(ctx, principal, capability, scope)。principal 使用 account.Principal，由可信认证结果提供。对象的学校、群和业务模块范围由所属业务 Service 提供，不能直接信任请求参数。

Identity.PlatformAdmin 每次查询当前身份与账号状态。Casdoor 目标组织的有效 IsAdmin 是平台管理员身份来源。身份撤销应返回拒绝，身份依赖故障返回不可用。

## 授权规则

平台管理员身份提供 platform.* 管理能力，用于初次配置授权。业务能力仍按显式角色授权判断。财务查询、退款、完整身份字段和材料查看等敏感能力不由初始模板附带授予。

模板包括平台、学校、内容审核、资料、群和财务六类，仅返回建议能力。财务模板只建议配置读取；具体业务能力标识由所属模块确认和接入，模板名称不产生任何授权。

角色定义只能由平台管理员创建。角色定义不可修改和删除；调整能力时创建新角色 ID，再撤销旧授权并授予新角色。角色定义上限 1000，每个管理员有效授权上限 1000，达到上限返回 conflict。

能力必须精确匹配。学校、群和业务模块三个维度分别匹配，* 表示该维度全部范围。三个维度必须同时满足，群标识不会自动推导出学校权限。查询能力不能用于业务修改。

授权需要目标范围的 platform.authorization.manage。普通授予者还必须拥有被授予的每个能力及完整目标范围。只有平台管理员可授予 platform.* 能力。所有管理员均不能给自己增加授权。

授权集合以管理员为单位使用递增修订号。授予、撤销必须提交 expected_revision，冲突返回 409。重复授权由数据库唯一约束拒绝。不可变角色防止通过修改已持有角色扩大自己的能力。

授权求值不缓存角色与范围。撤销提交后发起的新请求立即读取新状态。已经开始的查询使用一致性事务快照；所属业务模块须在实际操作前调用授权接口，不能将一次判断长期保存为通行凭据。

授权写事务串行完成当前授权检查、修订检查、授权写入、审计和幂等回执。相同操作者及幂等键的相同请求返回原结果，不同请求返回 409。返回旧回执前仍检查操作者当前权限。

## 查询与审计

查询他人有效授权需要查询范围的 platform.authorization.read；本人可查看自己的显式授权。响应按请求范围收窄，不返回范围外授权。修订号属于整个管理员授权集合。

操作记录查询将请求范围与当前 platform.audit.read 授权求交集，再向提供方传入允许的范围并分页。显式选择未授权模块返回 403；无模块筛选时仅查询已装配且获准的提供方。每页重新查询身份和授权。

查询支持目标对象、操作者、动作和起止时间筛选，时间区间为左闭右开。sources 按原始模块筛选，使用逗号分隔；module 继续表示对象的业务授权范围。例如 sources=platform 可查询平台发起的其他模块授权变更。

单条记录 source 为原始模块，包含对象、操作者、时间、动作、原因、结果和范围。结果允许 success、denied、failed、unknown；缺失原因返回空字符串，缺失结果不能推测为成功。范围不明的历史记录仅向相应全局范围开放。

审计保留 12 个月，授权幂等回执至少保留 24 小时，由有界清理释放。每轮最多清理 100 条审计、100 条授权回执和 100 条资格回执，限时 5 秒。当前角色与授权随部署数据保存。


## 提供方与稳定分页

admin.AuditProvider.ReadAudit 接收可信操作者、已授权范围集合、时间／操作者／对象／动作筛选、全局排序位置、提供方快照和读取上限。backend 在 Service.AuditProviders 注册模块适配器，platform 自身提供方内置。

提供方必须在所属模块的 Repository 中完成范围与条件过滤，然后按时间、来源模块、记录 ID 的降序读取。记录 ID 在模块内唯一且不可变，字符串按字节比较。返回统一 Record，不暴露原始 JSON 或任意扩展字段。

每页默认 50 条，上限 100。汇总最多读取 16 个提供方，每个最多读取页大小加一条，再按同一规则合并。调用顺序固定，单提供方限时 250 毫秒，整次查询限时 5 秒，提供方必须响应 context 取消。

首轮确定时间上界，每个提供方返回固定成员集合的快照令牌。后续读取使用相同快照和全局排序位置。快照不能包含首轮以后提交的记录，即使记录时间被回填；快照过期须报错，不能静默重建。

游标绑定操作者、筛选、页大小、提供方集合和授权范围。筛选变化返回 400，授权范围变化返回 403并要求重新查询。每个快照令牌上限 256 字节，游标上限 16384 字节，不保存服务端分页会话。

platform 原始审计记录写入事务 ID，并用 PostgreSQL 可见性快照排除延迟提交。保留期清理在同一事务递增审计清理修订号；分页期间发生清理会使平台快照失效，响应标记 platform 缺失。

响应 queried_modules 列出本次获准的提供方。提供方故障、超时、快照失效或违反范围／排序要求时，整个提供方批次被丢弃，partial=true，missing_modules 列出缺失模块。没有可用记录时仍明确标记部分结果。

同一分页过程持续保留缺失模块，不在后续页突然补入恢复的数据。恢复后需从第一页重新查询。partial=false 仅说明已装配且获准的提供方均返回成功；backend 应将预期存在但暂不可用的模块注册为 nil，使缺失可见。

0.2.0 扩展了记录 source 和 result 的取值，旧客户端须重新生成 SDK。旧游标不再有效，页面应重新发起查询。当前 PR 对 develop 新增管理接口；相对先前本地 0.1.0 契约的两项响应枚举变化属于明确的消费者迁移范围。

## 数据库与后续接入

迁移身份执行 postgres.Migrate，新增表和审计列，不覆盖旧业务数据。audit_retention 保存清理修订号，运行身份需要 SELECT／UPDATE；audit 的 recorded_xid 由数据库默认写入。

运行身份只访问 platform schema，禁止 UPDATE／DELETE admin_roles，禁止 UPDATE audit、eligibility_requests 和 admin_requests。audit 的 DELETE 用于保留期清理。

backend 后续须更新依赖、运行身份授权和 platformapp 装配，注入当前身份接口并注册新路由。sessionapp 保留认证职责，将管理能力与范围判断委托给 Evaluate；SubjectSchools 改为使用实际有效授权范围。

各业务适配器继续通过自己的公开授权接口调用 Evaluate，并提供真实对象范围。查询与修改分别使用能力标识。现有业务状态、资格规则和字段权限检查继续由业务 Service 执行。

## 验证与待完成事项

本地隔离 PostgreSQL 18.6 已通过 make check（契约与 origin/develop 比较），包括生成一致性、OpenAPI 校验、导入边界、go vet、竞态测试、构建和工作流检查。真实数据库测试使用独立库及运行身份，覆盖越界拒绝和审计失败回滚。

| 场景 | 当前验证 |
| --- | --- |
| AO-01 | 群、学校、模块范围分别拒绝越权；角色不附带其他业务能力 |
| AO-02 | 并发修改仅一个修订成功，冲突返回 409；相同请求重放不重复写入 |
| AO-04 | 查询能力不能用于退款或修改认证 |
| 授权边界 | 越权委托、自我提权、平台能力授予、范围外查询、撤销立即生效 |
| 数据一致性 | 授权唯一约束、授权与审计原子提交、数据库身份隔离 |
| 跨模块查询 | 多提供方稳定排序、回填及延迟提交、范围过滤、撤销翻页、故障／超时与保留期清理缺失标记 |
| AO-03 | 待完成：批量预览与逐项执行结果，本轮未提供批量接口 |

backend 授权适配、Web/Admin 消费者快照与页面、跨仓候选组合验证、真实账号验收均待后续任务。公开示例未装配真实管理员身份，本轮不涉及部署。

## backend 审计适配器字段映射

下表以候选工作树现有写入代码为依据。对象和操作者使用内部标识。缺少独立操作者或可靠范围时，需要所属模块补充记录或限制为全局查询，不能把记录所有者自动当成管理操作者。

| 提供方 | 原始表及字段映射 | 缺失与接入要求 |
| --- | --- | --- |
| platform | audit：id、target、actor、created_at、action、reason、result、school/group_id/module | 已内置；历史缺失范围为全局 |
| catalog | catalog.audit.data：ID、Subject、Actor、Time、Type、SchoolID | 原因与结果未单列 |
| reviews | reviews.audit.data：ID、Subject、Actor、At、Type、SchoolID | 原因与结果未单列 |
| moderation | moderation.audit.data：ID、Subject、Actor、At、Type、SchoolID | 原因与结果未单列；不能把审核决定直接等同执行结果 |
| resources | resources.audit.data：ID、ResourceID、Actor、Time、Type、SchoolID | 原因与结果未单列 |
| verification | verification.audit：id、time；data.object/actor/type/school_id | 原因与结果未单列；禁止返回认证材料和身份原值 |
| media | media.audit.data.event：id、subject、time、type；data.actor 为 issuer/subject | 没有学校／群、原因和结果字段，需模块补充可靠范围 |
| storage | storage.audit.data.event：id、subject、time、type、data.object_id；外层 actor | 对象优先采用 object_id；范围、原因和结果未单列 |
| storage-contribution | contribution.audit.data.event：id、subject、time、type；外层 actor | event.data.state 是业务状态，不能直接作为操作结果 |
| billing | billing.audit：seq、reference、actor_issuer/actor_subject、created_at、kind | issuer/subject 是目标账户；范围、原因与结果未单列，禁止返回交易载荷 |
| qq-binding | qq_binding.audit：id、owner、actor、created_at、kind、reason | 结果及学校／群范围未单列；owner 是目标账号 |
| open-platform | open_platform.events：id、object_id、actor、created_at、kind | operation 是操作／幂等标识，不是原因或结果 |
| profiles | profiles.closure_events：id、closure_id、actor_issuer/actor_subject、created_at、action | 关闭任务之外的 profiles.events 没有独立操作者，不能直接当成管理审计 |
| identity | identity.audit：id、issuer/subject、created_at、action | issuer/subject 标识关联账号，没有独立管理操作者或对象字段，接入前须明确事件语义 |
| favorites | favorites.events：id、owner_key、created_at、kind、operation | owner_key 是所有者，operation 是操作标识；未记录独立管理操作者与对象，接入前补齐 |

以上适配器由 backend 装配，但查询和数据映射在各模块公开接口内完成，不由 backend 或 platform 直接跨 schema 读取。原因缺失用空字符串，结果无法证明时用 unknown；操作者无法证明的记录不冒充已知管理员。

所有模块适配器都需提供稳定快照、范围先过滤、取消响应和有界读取。缺少学校或群范围时不能向学校／群管理员返回记录。模块有自己的附加敏感审计权限时，适配器仍须执行该权限。

候选 groups 与 information-query 尚无对应实现，不能声明群审计已接入。notifications.items／receipts 是通知与幂等数据，当前没有原始管理审计表，不应将消息内容转换为管理审计。
