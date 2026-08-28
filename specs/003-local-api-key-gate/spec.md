---
level: L3
feature: 003-local-api-key-gate
created: 2026-08-28
---

# C 组：内存租户、API Key 与模型路由门禁

## 原始需求

落实 `tenants`/`api_keys` 的最小概念模型，使业务调用按 API Key 归属 tenant、按 tenant/model 选择已允许的 Provider 路由，并在 Provider attempt 前拒绝未授权或预留配额失败。

## 目标

- 定义可注入 TenantStore/APIKeyStore；内存实现保存 tenant 状态、模型→Provider 顺序、Key prefix、Key SHA-256、状态和 tenant ID，绝不保存明文 Key。
- 启动时用一次性 `AGENTMESH_BOOTSTRAP_API_KEY` 只初始化内存记录并立即派生 prefix/hash；两个 CLI 仍只从 `AGENTMESH_API_KEY` 读取原始 Key。
- chat/provider-health 在读 body、选路或 Provider attempt 前以 Bearer Key 常量时间比较；认证成功只把 tenant identity 放入 Context。
- Gateway 按已认证 tenant 和请求 model 选择允许的 Provider 顺序；客户端不能自行选择 tenant、Provider 或覆盖路由。
- 预留稳定 `quota_exhausted` 拒绝码和接口位置，但本阶段不接 Redis、不扣减额度、不声称已实现配额。

## 非目标

- 不实现 MySQL、Redis Lua、Key 创建/轮换/撤销 API、多进程共享、多 Key 持久化、JWT/Casbin、管理面、usage/账单、真实 Provider 调用或 Docker。
- 不允许未认证 mock bypass；`/healthz` 是唯一公开且不含配置的 liveness 端点。

## 默认假设

- Bootstrap 配置还要求 `AGENTMESH_BOOTSTRAP_TENANT_ID` 与 `AGENTMESH_BOOTSTRAP_MODEL_ROUTES`；routes 是严格 JSON，键为 model、值为不重复的 `ark`/`ollama`/`mock` 顺序。
- 原始 Key 仅在当前进程环境出现；测试/T003 以 crypto/rand 每次生成，不在 fixture、日志、响应、README 或 Git 写值。
- 缺失、畸形、错误或禁用 Key 统一 HTTP 401 `auth_failed`；不允许的 model 为 403 `model_not_allowed`；`quota_exhausted` 仅保留 contract，不由本阶段发出。

## 验收条件

- 内存记录可证明只含 hash+prefix；缺失、畸形、错误或禁用 Key 都在 body 解码和 Provider 调用前拒绝，比较走常量时间入口。
- 有效 Key 的 tenant Context、model route 和 mock SSE 正常；tenant A 无法使用 tenant B 的 model/provider 路由。
- `/health/providers` 必须认证且只返回当前 tenant 已允许 Provider 的名称/状态；`/healthz` 仍固定公开。
- 认证、model 或预留 quota gate 任一拒绝时 Provider 调用数为 0，错误不含 Key、hash、prefix、tenant、endpoint 或上游 body。
- README 明确内存 seed 仅为离线 C 组切片，不是多租户持久化、Key 生命周期或 Redis 配额实现。
