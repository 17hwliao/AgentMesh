# 006 实施计划

## 决策与边界

- 本批使用 MySQL 8 的 `database/sql` 持久化 Reservation/attempt 证据，使用 `github.com/redis/go-redis/v9` 执行固定 Lua 脚本；不引入 ORM、迁移框架或 Docker 编排。连接串和 Redis 地址只从环境读取，启用配额模式但任一存储配置/连接不可用时 fail-closed，零 Provider attempt。
- 003 已发布的 `429 {"error":{"code":"quota_exhausted"}}` 保持原样；不新增 endpoint、请求字段或响应字段，故不触发 006 的 `contract.md`。未启用配额配置时仍保持既有 allow-only 开发切片，不能写成已启用生产配额。
- 新增 Reservation Coordinator，Gateway 在认证、model route 与请求解码后调用它；Coordinator 先在 MySQL 创建 `creating`，再经 Redis Lua 预扣，再以版本条件更新 MySQL 为 `reserved`，随后才允许 Router 发起 attempt。
- 每个请求用配置的上限单位预扣。Provider 明示 usage 优先结算；缺失时仅使用网关确认发送/转发的 rune 计量并标为 `estimated`。无法证明未使用的额度不释放，计量单位不声称是精确 Token 账单。
- Router 增加每次 Provider 调用前的同步、可失败 attempt hook；Coordinator 在 hook 中持久化 `provider_attempts.started_at`，检查 reservation 剩余量，再调用 Adapter。普通 Observer 继续仅作诊断，不能承担持久化 started 证据。
- 流式转发由该 request 的 attempt recorder 负责：每 16 个 chunk 或 1 秒（先到者）将单调递增的已转发 rune 下界与 `heartbeat_at` 写入当前 attempt 行，最终结算前再强制 flush。持久化进度失败时立即停止继续转发、保留内存下界并重试终结；若仍不可写，Redis 不退款且遗留 started evidence 让 reconciler 保守 `settled(estimated)`，宁可租户少获退款也不能把未知上游成本释放。README 必须说明 rune 是本地下界，不是 tokenizer 或精确 Token 计量。
- Redis reserve/settle/cancel Lua 以 `tenant_id + reservation_id + version + operation` 记录成功结果并原子修改可用额度；MySQL 版本条件更新与唯一 `tenant_id + request_id` 防止重复终态。拒绝不写幂等成功记录。
- Reconciler 仅由命令或测试显式调用：过期记录有 started attempt 或已转发计量则 `settled(estimated)`，否则才取消；重跑必须无第二次余额变更。没有常驻任务、usage outbox 或三方对账。

## 数据流

`HTTP auth → decode/model route → MySQL creating → Redis reserve Lua → MySQL reserved → [attempt hook: MySQL started + remaining check → Adapter]×N → Redis settle/cancel Lua + MySQL terminal`。

在 Redis 成功而 MySQL `reserved` 更新前中断时记录保持 `creating`，由 reconciler 依据 Redis operation 与 attempt 证据补偿；在 attempt hook 成功后，任何首块前失败、流中断或 Context 取消均走 `settled`。只有 Coordinator 能证明 hook 从未成功时才 `cancelled`。

## 迁移与配置

`migration-plan.md` 定义首次 MySQL 迁移、索引、回滚限制和上线前检查。环境变量使用明确的 opt-in `AGENTMESH_QUOTA_MODE=reservation`、MySQL DSN、Redis URL、每租户初始可用单位及单请求 reservation 上限；不得记录 DSN、密码或原始 Key。受控演示可注入确定性 MySQL/Redis 替身；真实环境缺配置只产生受控拒绝，不伪造持久化成功。

## 实现顺序

1. 增加持久化数据模型、迁移 SQL、Repository/Redis Lua 边界及可注入替身；测试 Lua 幂等、余额不足、settle/cancel 与 MySQL 版本条件。
2. 实现 Coordinator、同步 attempt hook 和 Gateway 接线；测试认证/路由后、Provider 前的 fail-closed，started 后保守 settle，fallback attempt 累加及零 Provider 拒绝。
3. 实现显式 reconciler 与 `demo-stage-4` 故障场景；验证预扣后中断、重复裁决和缺 usage 的 estimated 标记。
4. 全量验证、README/decisions/私有阶段复盘、提交并 fast-forward `master`；不 push/tag。

## 验证与风险

- 每批：相关包测试；收尾：`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、适用包 `-race`、`make demo-stage-4`、`git diff --check` 与 Adaptive Spec 校验。
- 集成测试使用受控替身证明顺序与故障边界；真实 MySQL/Redis 只在显式环境配置存在时运行，失败如实记录为验证不可用。
- 两存储不是分布式事务；`creating`、operation 证据、版本号与可重跑 reconciler 是恢复机制，不能称为 TCC 或严格跨存储原子提交。

## 章程

暂未建立；不作为本 feature 的阻塞条件。
