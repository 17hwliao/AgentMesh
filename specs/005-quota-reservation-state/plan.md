# 实施计划

## 实现决策

- 新增 `internal/reservation`：State、Reservation、Attempt、Repository 和稳定领域错误码；内部 ID 由调用方提供，便于故障与幂等测试，当前不新增公开 HTTP/API。
- 内存 Repository 用一把锁和不可变副本保存 Reservation 与逻辑 attempt；每次成功状态变更递增 version。它是接口参考实现，不模拟事务、进程崩溃或跨进程锁。
- 变更以 `(reservation_id, expected_version, operation)` 判定幂等：已成功的同操作同版本重放返回已保存结果；同版本不同操作、旧/未来版本返回稳定冲突，不产生第二个终态或 attempt。
- `StartAttempt` 只允许 reserved，先写 started attempt 再返回；Cancel 先检查是否有 started attempt。任何 started attempt 后的错误形态都由统一 Settle 终结，避免依据上游可见性释放未知成本。
- `expired_pending` 写入状态模型和契约，但本 feature 不公开进入/裁决路径；未来 reconciler 才能在持久化 attempt 证据上决定 settled/cancelled。
- 不建 migration/ADR：本批没有数据库、迁移或不可逆外部架构变更；使用唯一专项 `contract.md` 固化状态和错误语义。

## 数据流

`Create(creating,v1) → Reserve(v2) → [StartAttempt(v3) → Settle(v4)]`，或在没有 started attempt 时
`Create/Reserve → Cancel`。任何 attempt started 后的失败、取消或不确定性都进入 `Settle`；`expired_pending` 只交 future reconciler。

Repository 不接收 Provider 调用本身，005 只证明状态边界；006+ 才把 Adapter attempt、Redis 预扣与 MySQL 持久状态按同一顺序接入。

## 任务顺序

1. 实现 Reservation/Attempt 值对象、状态转换和稳定错误契约；覆盖合法路径、终态、cancelled 边界与 expired_pending 限制。
2. 实现锁保护的内存 Repository、版本幂等 journal 与 tenant 归属；覆盖重放、冲突、跨 tenant、重复 attempt/settle 和并发安全。
3. 运行受控状态场景并记录 README/plan；核验任何 started attempt 的失败/取消都 settle，明确没有真实预扣或持久化。
4. 全量格式/build/vet/test、Adaptive 校验、私有阶段复盘、提交并 fast-forward `master`；不 push/tag。

## 风险与降级

- 内存 Repository 无法抗进程崩溃，也没有 Redis/MySQL 原子性；只能作为状态机单元验证，不能证明额度不透支。
- 版本幂等不能代替网络请求去重或 outbox；后续须持久化 request/operation 证据并让 reconciler 重放安全。
- 当前 Gateway 的 allow-only QuotaGate 保持不变；不把半成品 Reservation 接到真实流路径，避免出现“状态已创建但没有持久预扣”的虚假安全感。
- T003 受控测试只验证内存转换、重放、tenant/版本冲突和并发同操作；它不产生 Redis/MySQL/Provider/Docker 事实。

## 章程

暂未建立；本 feature 不以章程为阻塞条件。
