---
level: L3
feature: 005-quota-reservation-state
created: 2026-08-29
---

# 阶段 4A：Reservation 状态机与 Attempt 边界

## 原始需求

在接入 MySQL、Redis Lua 和实际网关扣费前，先将配额 Reservation 的状态、`cancelled` 许可条件、Provider attempt 边界和 `reservation_id + version` 幂等语义实现为可注入、可故障测试的领域契约。

## 目标

- 定义 Reservation `creating → reserved → settled/cancelled` 状态机、版本号和 request/tenant 归属；`expired_pending` 是非终态，只能留给未来 reconciler 裁决。
- 定义逻辑 `provider_attempt` 记录：在调用 Adapter 前标记 started；任一 attempt 已开始后，首块前失败、流中断或取消都只能 `settled`，不得 `cancelled`。
- 所有变更要求 `reservation_id + expected_version`；同一操作的同版本重试返回原结果，冲突操作或版本返回稳定拒绝码，不能重复结算或释放。
- 提供锁保护的内存 Repository 与可注入接口，仅作为离线语义验证；不将其称作持久表、余额扣减或生产配额。

## 非目标

- 不接 Gateway、Provider、Router、trace、HTTP API、MySQL/SQLite、Redis Lua、令牌桶、余额、金额、usage_outbox、reconciler、迁移、对账、Docker 或真实 Provider。
- 不进入 `expired_pending` 的自动任务，不实现 `demo-stage-4`；它们必须等 Redis/MySQL/reconciler 阶段。

## 默认假设

- 创建 Reservation 不是预扣：005 的 `reserved` 只表示领域状态通过，不表示任何 Redis/数据库已写或额度已冻结。
- `cancelled` 仅允许在 `creating/reserved` 且不存在 started attempt 时发生；无法证明 attempt 未发起时一律拒绝取消并要求 settle。
- Repository 的进程重启丢失、单进程锁和没有真实时钟扫描都是已知降级，不得作为故障恢复或并发一致性证据。
- 输入没有 prompt、raw key、Provider 响应、endpoint 或 Token 文本；公开错误只使用稳定码。

## 验收条件

- 创建、reserve、attempt-start、settle、cancel 的合法/非法转换、版本递增和终态不可逆均有表驱动测试。
- 同版本同操作重放幂等；同版本不同操作、旧/未来版本和跨 tenant 查询/变更均被拒绝，记录数和终态不重复变化。
- attempt 前取消成功；attempt 后的首块前失败、流中断、Context 取消都只能 settle；`expired_pending` 不能直接 cancel。
- README/plan 和受控测试输出明确 005 没有 Redis/MySQL/SQLite/真实扣减/reconciler，不把内存绿测写成持久化一致性。
