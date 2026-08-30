---
level: L3
feature: 013-tenant-key-persistence
created: 2026-08-30
---

# 持久租户、API Key 生命周期与本地管理面

**原始需求：** 将 003 的进程内租户、模型路由与 API Key 迁入 MySQL，提供受限的创建与撤销 API，使重启不丢失认证状态。

## 目标

- 迁移新增 tenant、tenant model route 与 API Key 持久表；API Key 仅保存随机 key ID、prefix、SHA-256、tenant 关联、状态与安全时间摘要。
- 实现持久 TenantStore，保持 003 的常量时间认证、tenant 隔离与 model route 语义；启动时从 MySQL 加载，不从 bootstrap 配置重建数据。
- 在既有仅回环监听的服务中提供本地管理 API：创建 tenant、创建 API Key、撤销 API Key；管理 token 只从环境读取并用常量时间比较。
- 新生成的原始 Key 仅在创建成功响应中返回一次；之后不得出现在数据库、日志、trace、错误或任何查询响应中。

## 非目标

- 不实现 Web UI、远程管理面、JWT/OIDC/RBAC、Key 轮换、密码重置、自动迁移、自动 seed、Redis 配额、usage 账单或真实 Provider 联调。
- 不改变 `/v1/chat/completions`、`/health/providers` 的认证、路由、流式、fallback 或错误契约；不允许 tenant API Key 调用管理 API。
- 不执行真实 MySQL 验证或改变 010 的受控环境状态；运行时真实连接只在操作者显式配置后发生。

## 默认假设

- 持久模式显式要求 `AGENTMESH_AUTH_STORE=mysql`、`AGENTMESH_AUTH_MYSQL_DSN` 与非空 `AGENTMESH_ADMIN_TOKEN`；缺失或畸形配置在 Provider attempt 前 fail-closed，旧 bootstrap 内存模式仍可用于既有离线 demo。
- 管理 API 只接受 `127.0.0.1` 监听的请求；其 Bearer admin token 与 tenant API Key 分离，鉴权失败统一返回稳定码且不解码业务 body。
- API Key 用 crypto/rand 生成；撤销按不可猜测 key ID 精确生效，已撤销 Key 在任何 Provider attempt 前统一以 `auth_failed` 拒绝。

## 验收条件

1. migration 可重复应用且只新增本批表/索引；不含原始 Key、admin token、DSN 或 tenant prompt/message 数据，完整迁移与回滚限制记录于 `migration-plan.md`。
2. MySQL TenantStore 对未知 prefix 也走固定长度 dummy digest 的常量时间比较；禁用 tenant/Key、错误 Key 与已撤销 Key 均零 Provider 调用且不泄露存在性。
3. 服务重启后，已持久 tenant、model routes 与有效 Key 保持可用；不同 tenant 的 model/provider 路由继续隔离，不能混用或客户端覆盖。
4. 管理 API 在 tenant 鉴权与 body 解码前验证 admin token；只有成功创建响应包含一次原始 Key，其余持久化或可观察产物均不得包含它。
5. 撤销使用精确 key ID，重放保持幂等；被撤销 Key 立刻不能认证，且不影响同 tenant 的其他 Key 或 routes。
6. 离线 SQL 替身测试覆盖迁移形状、加载、创建、撤销、重启、未知 prefix、管理鉴权与 Key 不泄露；无配置的持久模式零网络尝试受控拒绝。

## 实现范围与验证

- 涉及 migration、tenant/auth store、回环管理路由、`cmd/api` 配置接线、README 与离线测试；L3 唯一专项文档为 `migration-plan.md`。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；不 push、不 tag、不启动 Docker。
