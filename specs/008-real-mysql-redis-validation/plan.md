# 008 实施计划

## 决策与边界

- 新增 `cmd/real-storage-verify` 和 `make verify-real-storage`。入口只从环境读取 `AGENTMESH_REAL_STORAGE_VALIDATION=1`、MySQL DSN、Redis URL 和验证 namespace；缺任一项先输出机器可读的 `verification_unavailable/quota_configuration_missing`，此时数据库、Redis、DDL、余额写入和 Provider attempt 都为 0。
- namespace 仅接受受限字符与长度，并派生独有 tenant/request/reservation 标识。Redis 只操作由本次已知标识构成的 key；MySQL 清理精确匹配本次 tenant 与 reservation ID。程序不能判断 endpoint 是否生产，操作者必须提供可处置环境。
- 复用 006 的 `SQLRepository`、`RedisQuotaStore`、`Coordinator` 和 `Reconciler`，验证器只负责编排场景与读取安全摘要；不接入 HTTP Gateway、不请求 Provider、不复制 Lua 或状态机逻辑。
- 既有 migration 不是裸重复执行：验证器逐表检查 `quota_reservations`/`provider_attempts` 是否存在；缺失才执行该表的既有 CREATE，已存在则核对必需列、唯一键、reconcile 索引、CHECK/外键约束。形状不符报稳定迁移码并停止，不 ALTER/DROP/修复无关 schema。
- 实测场景使用独立验证 tenant，逐个设定余额：① reserve 64 → 已观察 usage settle 24（余额 76）及 settle 重放；② reserve 64 → 零 attempt cancel（余额 100）及 cancel 重放；③ started/forwarded 的过期候选被 reconciler `settled(estimated)`、零退款且重跑无变化；④ 有 reserve marker 而无 attempt 的候选被取消、退款且重跑无变化。
- 验证成功后删除本次已知 reservation 行、其 attempt 行和 Redis key；migration 表保留。验证失败也只尝试同一精确范围的 best-effort 清理，并把清理结果与稳定失败码写进摘要，不隐藏残留。

## 数据流

`环境预检 → MySQL/Redis 连接 → migration 存在性/形状检查 → namespace 初始化 → reserve/settle/cancel 场景 → 注入过期 heartbeat → Reconciler 两类裁决与重跑 → 精确清理 → stdout 安全摘要`。

摘要只包含配置状态、场景名、稳定结果码、终态、余额断言、清理结果与 Provider attempts=0；不含 DSN、URL、密码、原始 SQL、key、prompt 或响应。

## 文件与测试切分

1. `internal/reservation` 增加可注入的真实存储验证编排与 migration 形状检查；单元测试覆盖配置缺失的零连接、namespace 校验、表存在/缺失/形状不符分支、摘要脱敏。
2. `cmd/real-storage-verify`、PowerShell 包装和 Make target 只负责环境入口与 JSON 输出；测试确认无配置时不创建客户端、不会把环境值回显。
3. 验证场景测试复用 006 Repository/Lua/Reconciler 的接口替身，覆盖场景顺序、幂等余额、保守/取消恢复和精确清理列表；真实 endpoint 只由显式 T003 命令触发。
4. README 记录开关、前提、真实/受控拒绝/未验证三态、重跑与不支持的能力；私有复盘记录本机真实命令事实，不写凭据。

## 风险、恢复与验证

- MySQL/Redis 不是事务：场景中途失败时不把未知预扣写成通过；运行结束将清理结果如实输出，残留只能按 namespace 手工处理或由后续重跑恢复。
- 没有真实配置时，T003 的正确结果是受控拒绝，不能用离线测试替代真实基础设施成功。
- 收尾运行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`、Adaptive 校验；T003 执行 `make verify-real-storage`，并保留真实 stdout 的安全摘要。

## 专项与章程

本 feature 对既有 schema 执行 DDL，故新增且仅新增 `migration-plan.md`；没有对外 API 形状变化或不可逆架构决策，不写 `contract.md`/ADR。章程暂未建立，不阻塞实施。
