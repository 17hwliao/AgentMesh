---
level: L3
feature: 006-quota-reservation-persistence
created: 2026-08-29
---

# 配额预扣与持久化接入

**原始需求**：阶段4B 配额预扣与持久化接入——原子预扣/结算、持久化 reservation/attempt、reconciler 补偿。

## 目标

让已认证租户的每次流式调用先经原子预扣与持久化 reservation 才能发起 Provider attempt，流结束按实际用量幂等结算，中断的调用由 reconciler 依据持久 attempt 证据裁决，保证额度既不透支也不泄漏。

## 非目标

- 不实现令牌桶限流、账单/支付、语义缓存、OTel/Prometheus 指标。
- 不做管理面、跨实例部署验证、Key 生命周期或第三个 Provider。
- 不重写 005 已固化的状态机语义；持久化实现必须复用同一状态与错误契约。

## 验收条件

1. **给定** 有效租户且额度充足，**当** chat 请求到达，**则** 先持久化创建/预扣再发起 Provider attempt；流结束后按用量结算，同一请求最终只产生一个终态记录且只扣一次。
2. **给定** 额度不足或预扣存储不可用，**当** 请求到达，**则** 以稳定码（`quota_exhausted` 等）拒绝，Provider attempt 数为 0（fail-closed）。
3. **给定** 任一 attempt 已 started 后失败（首块前失败/流中断/客户端取消），**当** 结算，**则** 只能 `settled`，且仅释放确定未使用部分。
4. **给定** 能证明未发起 attempt 的本地拒绝，**当** 取消，**则** `cancelled`；无法证明时拒绝取消并要求结算。
5. **给定** 预扣后进程中断留下的未终态记录，**当** reconciler 扫描，**则** 有 attempt 或已转发计量证据的判 `settled(estimated)`，无 attempt 痕迹的才 `cancelled`；`expired_pending` 不被普通操作直接裁决。
6. **给定** 同一 reservation 的重复或并发结算/取消，**当** 执行，**则** 幂等返回原结果，终态不重复、不二次释放。
7. **给定** 演示命令，**当** 运行 `make demo-stage-4`，**则** 展示一次预扣、一次首块前失败 fallback 且两次 attempt 计量累加、再模拟中断后由 reconciler 裁决。

## 默认假设

- `持久化存储选择` → 关系型库保存 reservation/attempt 状态与证据，原子键值存储保存预扣计数；两者用本机受控实例或可注入替身验证，不要求容器编排。
- `结算用量来源` → Provider 明示 usage 优先；缺失时保存本地已转发计量并标记 estimated，但它只是不精确下界，不能据此退款或声称精确 Token 账单。
- `拒绝顺序` → 配额检查位于认证与 model 路由之后、任何 Provider attempt 之前。
- `存储结构` → 表/键结构与迁移语句由 `/plan` 决定，spec 不固化。
- `reconciler 触发` → 手动命令或测试内调用，不做常驻定时任务。

## 实现范围

- 涉及模块/目录：`internal/reservation`（持久化实现）、`internal/gateway`（QuotaGate 接线）、reconciler 入口与 `Makefile`（demo-stage-4）。
- 依赖前提：005 状态机契约、003 认证与路由、可注入存储接口；外部依赖见默认假设。
- 验证命令：`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`make demo-stage-4`。
