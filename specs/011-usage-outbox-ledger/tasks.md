# 011 任务清单

- [x] T001：新增 002 ledger migration、持久类型与 `MarkSettled`/`MarkCancelled` 的终态+outbox 同事务写入；替身测试覆盖两终态、outbox 写入/commit 失败共同回滚及历史终态不回填。
- [x] T002：实现 MySQL 8 `SKIP LOCKED` batch drain 与 `usage_records` 投影；替身测试覆盖重复/并发 claim、事务回滚后重跑及 record 写入失败保留 outbox。
- [x] T003：实现 Redis operation 只读检查、逐 reservation reconciliation、显式 drain/reconcile CLI 的配置门禁与 README；替身测试覆盖完整、三类缺失和状态/单位不一致。
- [x] T004：全量格式/build/vet/test/diff、Adaptive、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
