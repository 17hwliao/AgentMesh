---
level: L2
feature: 015-api-contract-consolidation
created: 2026-08-30
---

# HTTP 契约事实合并

## 来源

003 的鉴权契约落后于 `master` `6753800` 的已实现 HTTP 行为；本阶段只偿还文档债，不引入或改变运行时能力。

## 目标

- 将 `specs/003-local-api-key-gate/contract.md` 更新为当前 API 的单一业务/管理 HTTP 契约入口。
- 覆盖公开 health、已认证 chat/provider health/trace，以及持久模式才注册的三条本机 admin 路由。
- 记录稳定状态码、响应类型、`X-AgentMesh-Trace-ID` 与超限 `Retry-After`，并明确敏感数据绝不出现在响应中。
- 首次以端点为单位写出实际处理顺序，至少区分 tenant 鉴权、限流、body 解码、model route、QuotaGate、Reservation/Provider attempt 与 admin token 鉴权。
- 以既有 HTTP/单元测试与源码为事实来源，新增契约回归测试仅覆盖当前文档容易漂移的关键状态码/响应头。

## 非目标

- 不新增端点、字段、错误码、鉴权逻辑、限流策略、迁移或 Provider 行为。
- 不修改 003/004/013 的历史规格事实，也不把离线替身或受控拒绝写成真实 MySQL、Redis 或 Provider 成功。
- 不建立 015 的 `contract.md`：用户明确要求维护已有 003 契约文件，这是本批唯一文档例外。
- 不触碰 010 阻塞态规格、Docker、外部环境、push 或 tag。

## 默认假设

- `GET /healthz` 是唯一无认证端点；admin 路由只在持久 auth mode 注册，且仅接受独立 admin Bearer token。
- `rate_limited` 与 `quota_exhausted` 都是 429，但前者在 body 前发生且携带 `Retry-After`，后者保持 model 解码/路由后的既有语义。
- trace header 只由已认证且 strict JSON 成功的 chat 请求生成；它在 model route 判定前设置，不代表流最终成功。

## 验收标准

1. 合并后的 003 契约逐端点列出方法、认证前提、成功/拒绝码、响应边界与是否可触发 Provider。
2. 处理顺序表与 `6753800` 源码一致：错误 Key 不读 chat body；rate limit 不读 body也不进入下游；model/quota 在合法 body 后、attempt 前；admin token 在 admin body 前。
3. 测试锁定 `rate_limited` 429/`Retry-After`、trace header、trace 404 收敛、admin `admin_auth_failed` 的关键事实，且不改变实现行为。
4. README 增加该契约入口；全量 format/build/vet/test/diff 与 Adaptive Spec 校验通过。
