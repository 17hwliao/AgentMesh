# 006 任务清单

- [x] T001：新增 MySQL migration、持久化 Reservation/attempt Repository 与可注入 SQL 替身；覆盖版本条件、request 幂等、started evidence、进度/heartbeat 单调写入及迁移 schema。
- [x] T002：新增 Redis Lua reserve/settle/cancel 边界与确定性替身；覆盖额度不足、成功操作重放、仅释放确定未使用单位，以及 Redis/MySQL 间故障遗留 `creating`。
- [x] T003：实现 Coordinator、同步 attempt hook、流中 16 chunk/1 秒 flush、Gateway fail-closed 接线与显式 reconciler；覆盖零 Provider 拒绝、attempt 后 settle、fallback 累加、进度写入失败和 reconciler 重跑。
- [x] T004：增加 `demo-stage-4`、README/decisions/私有复盘；全量 build/vet/test/race、迁移/故障验证、Adaptive 校验、提交并 fast-forward `master`；不 push/tag。
