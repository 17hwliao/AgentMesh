---
level: L3
feature: 011-usage-outbox-ledger
created: 2026-08-30
---

# Reservation Usage Outbox、持久记录与逐项对账

**原始需求：** 为 006 的终态 Reservation 在同一 MySQL 事务写入 usage outbox；显式消费为幂等 usage records，并对 Redis 结算、MySQL 终态与 usage record 作逐 reservation 只读核对。

## 目标

- 对每个 `settled` 或 `cancelled` Reservation，在终态 MySQL 更新成功的同一事务创建一个安全的 outbox 事件。
- 显式、可重跑的 drain 将 outbox 投影为每 reservation 至多一条 `usage_records`，不依赖常驻 worker。
- 显式 reconciliation 逐项比较 MySQL 终态、对应 Redis operation 结果和 usage record，输出稳定的完整/缺失/不一致结果，绝不自动修账。

## 非目标

- 不改变 Redis 预扣、退款、Reservation 状态机、Provider、Gateway 流语义或 010 的真实环境验证器。
- 不实现 HTTP 查询/API、账单、价格计算、tokenizer、全局 Redis 余额重建、自动常驻任务或自动修复。
- 不持久化 prompt、message/delta、原始 API Key、DSN/Redis URL、Provider 原始错误或原始凭据。

## 验收条件

1. migration 新增 `usage_outbox` 与 `usage_records`；以 `reservation_id` 唯一约束保证终态事件与 record 均幂等，且仅存 tenant/model/终态/单位/usage 观测与安全时间摘要。
2. `MarkSettled` 与 `MarkCancelled` 的终态 Reservation 更新和 outbox 插入要么同一事务提交，要么共同回滚；无 outbox 写入时不得产生终态成功。
3. 显式 drain 对重复、并发或崩溃后重跑不重复产生 usage record；outbox 仅在 record 持久化后标记已投影，失败保留待重试且不影响已结束请求。
4. 显式 reconciliation 逐 reservation 核对 MySQL 的 `reserved/settled/released`、Redis 对应 settle/cancel operation 结果与 usage record；缺失或不一致输出稳定码，不退款、不补写、不改 Redis。
5. 内存/SQL/Redis 替身测试覆盖事务回滚、终态重放、drain 重放与并发、缺 outbox/record/Redis operation 及金额不一致；不调用真实 MySQL/Redis。
6. README 明确 usage records 是已结算摘要而非精确账单；真实端点成功仍仅属于 010，未配置时不伪造实证。

## 默认假设

- 本批是 L3，因新增持久表、事务边界与配额账务对账；`migration-plan.md` 是唯一专项文档。
- reconciliation 以每个 reservation 的 Redis operation key 为证据，只检查可获取的 release/状态，不推导或更改 tenant 全局余额。
- drain/reconciliation 采用现有环境变量存储配置及显式 CLI/函数入口；配置缺失时在连接前受控拒绝。

## 实现范围与验证

- 涉及 migration、`internal/reservation` repository/outbox/reconciliation、显式 CLI、README 与离线替身测试；不改网关的对外请求/响应契约。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；不 push、不 tag、不启动 Docker。
