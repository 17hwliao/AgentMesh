# 011 计划：终态账务投影与只读对账

## 数据与事务边界

新增 `migrations/002_usage_ledger.sql`，其中 `usage_outbox` 和 `usage_records` 都以 `reservation_id` 为主键。两表只保存 tenant/request/model、终态、预扣/结算/释放单位、`usage_observed`、`settlement_kind`、Redis operation 的终态前 version 和安全时间；不保存 prompt、Provider 响应、密钥或 endpoint。

`SQLRepository.MarkSettled` 与 `MarkCancelled` 在既有终态 UPDATE 成功后、同一 InnoDB transaction 内插入不可变 outbox snapshot，随后才 commit。插入失败或 commit 失败一律 rollback，调用方不能把 MySQL 终态当作成功。现有终态行不做 backfill：它们缺少可信 outbox 证据，对账应报告 `outbox_missing`。

## 投影与对账

新增 repository 的显式 batch drain：事务锁定未投影 outbox（MySQL 8 `FOR UPDATE SKIP LOCKED`），以 `reservation_id` 插入 usage record，再在同一 transaction 写 `projected_at`。两次写入要么共同 commit、要么共同 rollback；事务回滚后的重跑由唯一键与锁保证幂等，写入失败保留未投影 outbox。

新增 Redis operation 只读检查器，读取终态前 version 的 `settle` 或 `cancel` operation key 并解码状态/释放单位。reconciliation 对每一条终态 Reservation 只比较 MySQL snapshot、outbox、usage record 与该 Redis operation；输出 `reconciliation_complete`、`outbox_missing`、`usage_record_missing`、`redis_operation_missing` 或 `ledger_mismatch`。它不读取或推导 Redis 全局余额，不退款、不补写、不改变任何一方。

## 入口与失败行为

提供两个显式 CLI：`usage-outbox-drain`（唯一会投影 usage record）和 `usage-reconcile`（只读）。两者只复用 `AGENTMESH_QUOTA_MYSQL_DSN`、`AGENTMESH_QUOTA_REDIS_URL`，配置缺失时在连接前以 `quota_configuration_missing` 拒绝；不接入 Gateway、定时器或后台 goroutine。启动 worker 前操作者必须先按 migration-plan 应用 002 migration。

## 验证与批次

- T001：新增 migration、持久类型和终态 outbox 同事务写入；替身测试覆盖 settled/cancelled、事务失败 rollback 与既有终态不回填。
- T002：实现 batch drain 与 usage record 幂等；替身测试覆盖重复、并发 claim、record 写入失败和崩溃后重跑。
- T003：实现 Redis operation 检查、逐项 reconciliation、两个 CLI 的缺配置受控拒绝与 README；替身测试覆盖三方完整、任一缺失和单位/状态不一致。
- T004：全量格式/build/vet/test/diff、Adaptive、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。

章程：暂未建立；本计划不阻塞执行。
