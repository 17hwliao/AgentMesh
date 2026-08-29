# 011 Usage Ledger 迁移计划

## 部署前提

本批新增 `usage_outbox` 与 `usage_records`，命中 L3 数据结构变更。操作者须先备份目标 schema，并以仅限新表 DDL/DML 的账号，在启用带 outbox 的 Gateway 或运行 drain 前显式应用 `migrations/002_usage_ledger.sql`。不自动执行 DDL，不创建 schema，不使用真实 Provider/Docker。

现有 `quota_reservations` 与 `provider_attempts` 绝不 ALTER/DROP；历史 terminal reservation 不回填 usage outbox/record，因为不能补造当时的 Redis operation 或终态快照。部署前已存在的行在 reconciliation 中只能得到缺失证据，而非被修复为完整。

## 变更与可重跑性

两张表的主键均为 `reservation_id`：一条 terminal Reservation 最多一个 outbox 与一个 usage record。outbox 另有未投影扫描索引；usage record 以 outbox snapshot 为唯一来源。migration 只 CREATE TABLE IF NOT EXISTS；若已有同名表形状不符，操作者必须停止并人工处理，程序不 ALTER 或删除数据。

## 回滚与恢复

应用 migration 后不自动 drop 新表。若代码部署失败，停止 Gateway 的 reservation 模式和 drain；未投影 outbox 保留以供修复后重跑。已投影 record 不删除、不重算；对账只报告差异。只有确认没有任何新业务数据的专用环境，才由操作者人工决定是否回退 schema。
