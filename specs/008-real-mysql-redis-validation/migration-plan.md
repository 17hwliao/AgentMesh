# 008 真实存储迁移执行计划

## 触发与前提

本批会在操作者提供的 MySQL schema 上执行既有 `migrations/001_quota_reservations.sql`，命中 L3 迁移触发。执行前必须显式设置验证开关、DSN、Redis URL 与 namespace，并由操作者确认目标为可处置的非生产 schema；验证器不读取 flags、配置文件或全局 Git/Docker 设置。

操作者须先以现有运维方式备份或导出目标 schema，并使用仅覆盖本批两表所需 DDL/DML 权限的数据库账号。程序不自动创建 schema、备份 schema、drop 表或修改其他表。

## 执行策略

1. 连接后只检查 `quota_reservations` 与 `provider_attempts` 的存在性和必要形状。
2. 表不存在时，仅执行 migration 中对应的 `CREATE TABLE` 语句；表存在时跳过 CREATE，核对列、主/唯一键、reconciler 索引、状态 CHECK 和 attempt 外键。
3. 任何形状不符、DDL 失败或半完成都停止并输出稳定失败码；不尝试 ALTER、DROP 或自动“修复”。重跑只会补建仍不存在的表，再次核对全部形状。
4. migration 成功后才写入带本次 namespace 的验证数据；成功与失败都只删除该 namespace 已知的 attempt、reservation、余额、operation 和 reserve-marker key。

## 回滚与恢复

已存在表绝不回滚。首次新建的表即使尚无验证数据也不由程序 drop；如需删除只能由操作者在确认零业务数据的专用环境人工处理。验证数据清理失败会被摘要标记，不能被写为成功；保留的 reservation 可由后续 namespace-scoped 重跑或 reconciler 调查。

## 通过门槛

两表形状与 006 migration 一致、所有验证数据在专属 namespace 内、验证器没有触碰无关表/key、结束摘要明确 migration/场景/清理状态；否则本次只记录失败或验证不可用，不声称真实基础设施已通过。
