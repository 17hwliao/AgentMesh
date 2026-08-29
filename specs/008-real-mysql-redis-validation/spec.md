---
level: L3
feature: 008-real-mysql-redis-validation
created: 2026-08-29
---

# 真实 MySQL / Redis Reservation 受控验证

**原始需求：** 在操作者提供的受控 MySQL 与 Redis 上，验证 006 已实现的迁移、预扣/结算/取消与 reconciler 恢复；缺配置时必须诚实拒绝。

## 目标

- 提供显式 opt-in 的验证入口，只从环境变量读取 MySQL DSN、Redis URL 与验证命名空间。
- 在真实存储上执行既有 migration，并验证预扣、确定用量结算、零 attempt 取消及其幂等余额结果。
- 构造真实持久化的遗留 reservation，验证 reconciler 对 started 与无 attempt 两种证据作保守裁决。
- 将真实成功或受控拒绝的事实写入安全摘要，绝不把离线替身结果写成真实基础设施实证。

## 非目标

- 不改变 migration schema、Reservation 状态机、HTTP/API 契约、Gateway 路由或 Provider 行为。
- 不调用真实 Provider、不做 Docker 编排、负载/跨实例测试、令牌桶、usage outbox 或账单对账。
- 不接受命令行凭据，不记录 DSN、Redis URL、密码、API Key、prompt 或响应内容。

## 验收条件

1. 未同时提供显式验证开关、MySQL DSN、Redis URL 与隔离命名空间时，入口在任何网络/DDL/余额写入前以稳定码 `quota_configuration_missing` 拒绝，并留下无密钥摘要。
2. 显式配置的受控实例上，既有 `001_quota_reservations.sql` 可重复执行；仅创建既有两张表，不执行 drop 或修改无关 schema。
3. 同一验证租户的 reserve → settle、未 started 的 reserve → cancel 及相同操作重放，在真实 Redis/MySQL 中都得到预期余额、终态与一次性效果。
4. 真实存储中的过期候选由 reconciler 裁决：started/forwarded 证据必须 `settled(estimated)` 且不退款；无 attempt 且有 reserve marker 才 `cancelled` 并退款；重跑不二次变更。
5. 所有验证数据和 Redis key 均带专属命名空间；仅清理本次生成的行/key，不删除 migration 表。真实 Provider attempt 数固定为 0。
6. 全量 build/vet/test/diff 与 Adaptive Spec 校验通过；README 与私有复盘如实区分“真实成功”“受控拒绝”“未验证”。

## 默认假设

- 操作者只会提供可处置的非生产 MySQL schema 与 Redis logical DB/namespace；程序用额外 opt-in 防止意外连接，但不能替操作者判定环境性质。
- 本批执行既有 migration 而不改动仓库 schema；`/plan` 仍须写 `migration-plan.md`，说明真实 DDL、备份前提、清理范围与不可 drop 回滚边界。
- 验证入口不接入普通 `go test`，也不启动网关；配置缺失与外部连接失败均如实记录，不伪造通过。

## 实现范围与验证

- 涉及受控验证 CLI/脚本、`internal/reservation` 的真实存储编排测试、Makefile、README 与必要测试；复用 006 的 Repository、Lua 和 Reconciler，不复制业务逻辑。
- 收尾至少执行：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`；真实验证命令由 `/plan` 根据现有入口确定。
