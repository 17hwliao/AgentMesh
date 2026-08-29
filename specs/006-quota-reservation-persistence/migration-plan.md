# 006 持久化迁移计划

## 触发与范围

本批首次创建 MySQL 持久化表，命中 L3 迁移触发条件。新增迁移 `migrations/001_quota_reservations.sql`，只创建 `quota_reservations` 与 `provider_attempts`；不迁移现有内存 tenant/API Key，不创建 `usage_records` 或 `usage_outbox`。

## Schema

- `quota_reservations`：`reservation_id` 主键、`tenant_id`、`request_id`、`model`、`state`、`version`、`reserved_units`、已结算/释放单位、`usage_observed`、`settlement_kind`、`heartbeat_at` 和创建/更新时间；`(tenant_id, request_id)` 唯一，`(state, heartbeat_at)` 索引供 reconciler 扫描。
- `provider_attempts`：主键、`reservation_id` 外键、每 reservation 递增 ordinal、Provider/模型、安全结果码、`started_at`/`finished_at`、Provider usage 与已转发本地单位、`usage_observed`；`(reservation_id, ordinal)` 唯一，并按 `(reservation_id, started_at)` 查询。
- 状态以受限字符串保存，代码只接受 005 已定义的 `creating`、`reserved`、`settled`、`cancelled`、`expired_pending`；Prompt、原始 API Key、Provider body、DSN 和 Token 均不落表。

## 执行、兼容与回滚

迁移只允许在 MySQL 8 受控实例上由显式运维命令执行，并先备份 schema；应用在 migration 未完成时不得启用 `AGENTMESH_QUOTA_MODE=reservation`。首版无历史数据回填，现有网关保持未启用模式可回退。

表一旦有 Reservation 证据，不能用 drop-table 作为常规回滚：关闭该模式、保留记录并由 reconciler 结清。仅在确认零业务数据的开发环境，才允许人工删除两张新表；迁移文件本身不自动执行 destructive down 操作。

## 验证

迁移测试在受控 MySQL 或 SQL 解析夹具中确认两表、唯一键、索引和状态字段；Coordinator 集成测试确认创建先于 Redis reserve、attempt started 先于 Adapter、以及失败恢复可被 reconciler 重跑。生产接入前必须独立核对备份、最小 DB 权限与 Redis key namespace。
