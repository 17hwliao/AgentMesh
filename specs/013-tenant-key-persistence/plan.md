# 013 计划：持久认证与本地 Key 生命周期

## 构造与存储边界

新增 `migrations/003_tenant_api_keys.sql`，由操作者在 MySQL 8 上显式应用；持久模式绝不自动执行 DDL。新增 MySQL tenant store，直接实现既有 `tenant.Store` 的认证和路由读取，并以独立 lifecycle repository 管理 tenant/Key 写入，避免管理写路径进入 Gateway。

`AGENTMESH_AUTH_STORE` 未设置时仍调用 003 的 Bootstrap 内存 store；值为 `mysql` 时，启动前要求 `AGENTMESH_AUTH_MYSQL_DSN` 和 `AGENTMESH_ADMIN_TOKEN`。缺失、未知 mode、无效 token 配置或连接前校验失败以稳定配置码停止，零 Provider attempt；运行期数据库异常一律 fail-closed，不回退到 bootstrap 或其他 tenant。

MySQL 认证按 prefix 查询单条启用或禁用 Key/tenant 摘要，并始终对固定长度 digest 做常量时间比较；未知 prefix 使用 dummy digest。每次请求读取持久 Key 状态，不缓存认证结果，因此成功撤销后下一请求立即失效。模型 route 也从持久表按 tenant/model 读取，保持 Resolver 的 provider allow-list 及 002 mock/真实 Provider 互斥规则。

## 管理入口与顺序

主程序继续只允许 `127.0.0.1:PORT`。根 mux 将 `/admin/` 管理路由置于 tenant-authenticated Gateway 之外：管理 middleware 先解析并常量时间比较 Bearer admin token；失败立即返回 `admin_auth_failed`，**不读取或解码业务 body**。只有该验证成功后，处理器才以大小限制解码 JSON 并调用 lifecycle repository。

最小端点为 `POST /admin/tenants`（tenant ID 与 routes）、`POST /admin/api-keys`（tenant ID）和 `DELETE /admin/api-keys/{key_id}`。创建 Key 用 crypto/rand 生成并在同一成功响应中仅返回一次 `key_id` 和原始 Key；写库前只派生 SHA-256/prefix。撤销精确按 key ID 更新，已撤销重放保持成功但不影响其他 Key。错误响应不含原始 Key、hash、prefix、DSN、admin token 或数据库原文。

## 数据、迁移与兼容

迁移创建 `tenants`、`tenant_model_routes`、`api_keys`：tenant ID 主键与 enabled/timestamps；route 以 `(tenant_id, model, ordinal)` 保持顺序并限制 provider 名称；Key 以不可猜测 UUID key ID 为主键、唯一 prefix、BINARY(32) SHA-256、tenant 外键、enabled/revoked/created 摘要。唯一 prefix 冲突时生成器重试，绝不以覆盖已有 Key 解决。

启动检查只连接/加载持久模式；现有 quota 和 usage 表及 010 工作区均不改动。内存 Bootstrap 配置与既有 stage-1 demo 继续可用；持久 store 不从环境 seed tenant，避免重启时覆盖已保存身份。

## 验证与批次

- T001：编写 003 migration、持久类型/查询与配置选择；SQL 替身测试 schema、未知 prefix、禁用状态、重启加载和无配置零连接。
- T002：实现 lifecycle repository 与回环管理 middleware/路由；测试 admin 鉴权先于 body 解码、Key 仅一次响应、创建/撤销/重放与无泄露。
- T003：接线 `cmd/api`、Resolver 和 README；端到端离线 HTTP 测试持久认证/路由/撤销后零 Provider 调用，并复核旧 bootstrap demo。
- T004：全量格式/build/vet/test/diff、Adaptive、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。

章程：暂未建立；本计划不阻塞执行。
