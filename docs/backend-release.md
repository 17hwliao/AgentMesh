> **2026-10-08 扩展版本**：当前运行契约以 [后端扩展说明](backend-expansion.md) 为准。以下早期阶段记录保留历史口径，不能用其未勾选任务或旧能力限制判断本次交付。实际验收与剩余环境条件见联合发布包报告。

# 当前后端契约（2026-10）

生产部署使用联合发布包中的 `deploy/compose.yml`、独立 MySQL/Redis/Kafka 和 Caddy。两个仓库与 deploy 目录保持同级。默认的本机 mock 命令仍用于历史示例；它不是生产启动方式。运行手册为联合包 `deploy/README.md`。

## 可用接口

| 接口 | 权限与行为 |
|---|---|
| `GET /healthz` | 进程存活；合法 JSON |
| `GET /readyz` | MySQL、配额 Redis、限流 Redis、钱包标记正常才返回 200；异常/退出返回 503 |
| `POST /v1/chat/completions` | 租户 Bearer Key；必须 `stream:true`；`max_tokens` 默认 1024、最大 4096；支持 temperature 0–2 和 include_usage |
| `POST /v1/embeddings` | 同样鉴权、模型授权、限流、并发限制、预算结算；Ollama embed 接口 |
| `GET /v1/observability/traces/{id}` | 仅所属租户可读；进程内最近 128 条，重启不保留 |
| `/admin/tenants`、`/admin/api-keys` | 独立管理 Bearer Token；仅服务器回环地址/SSH 通道；Key 原文创建时仅返回一次 |
| `POST /admin/quota/grants` | `{tenant_id,grant_id,units}`；同 ID 同额度幂等，同 ID 不同额度返回 409 |

额度是内部预算单位，不是付款账单。输出以 Unicode rune 限额，同时向模型传递 max_tokens/num_predict；模型报告 token 数与 rune 不保证相等。已知使用量超出预留、未知使用量、部分错误和 embeddings 无使用量时采取全额预留的保守结算，不宣称精确计费。默认每请求预留 4096 单位；余额低于预留值会拒绝请求，即使看似能支付一个短回答。

全局最多 128 个活跃请求，每租户 32；单请求 2 分钟、SSE 写入 15 秒。请求体 64 KiB，上游 embedding 响应 8 MiB。Redis 限流默认每分钟 120、突发 20。认证和限额拒绝发生在模型调用前。首块前可 fallback；流开始后失败不换模型。取消清理使用脱离客户端取消的 5 秒上下文，未完成的记录由恢复循环处理。

每个受保护接口的鉴权处理器限制数据库查询并发 64、3 秒超时；超限返回 429 auth_busy，鉴权结束不限制或取消后续 SSE 上下文。

## 持久状态与恢复

`cmd/migrate` 在独立迁移身份下执行嵌入迁移，使用命名锁、步骤哈希和逐步完成记录。运行身份仅具备 CRUD 权限。初始 operator 路由和 API Key 哈希由迁移命令创建；新租户初始额度和补充额度均写入 MySQL grants，Redis 按稳定业务键仅应用一次。取消/结算共用稳定终态键，数据库版本递增不会再次退款。

钱包丢失或缺少版本 2 标记时拒绝请求和充值，readyz 返回 503。不能靠重新初始化/flush Redis 恢复。停止全部请求与钱包相关后台进程，再用 `quota-rebuild --offline --tenant ID` 从完整持久账本重建。终态历史由 MySQL 汇总，不受历史条数限制；活跃记录上限 10000，保守占用全额预算，等待恢复。Redis 退款成功但数据库尚未持久化、随后 Redis 又丢失时，恢复可能多扣额度；需人工核对后用唯一 grant_id 补偿，不能自动增发。

Kafka consumer 在当前消息投影成功或安全隔离后才取下一条并提交；SQL outbox/projection 唯一键处理重复投递。发布超过 5 分钟但没有投影的事件会重新投递。毒消息 DLQ 只保存摘要，不保存原始敏感 payload。MySQL 账本、Redis AOF 和 Kafka 卷需要持久保存。单机版本不是高可用服务。

历史数据若只有 Redis 初始余额、没有 durable grants，不能推断原始累计充值。升级前停流量、备份、核对历史资金/额度来源、人工导入可信 grant 账本后离线重建；不能直接给所有旧租户再发初始额度。

## 验证与边界

`go test ./...`、`go vet ./...`、Linux `go test -race` 与 govulncheck 由 CI 执行。Docker 的 verify target 同样执行 race/vet。联合包本机验收使用真实 Linux MySQL、Redis、Kafka，模型和 GitHub 使用内部协议 fixture。真实模型/公开 TLS/GitHub 授权配置必须按运行手册另行联调。单机服务的容量受内存、持久卷、Redis 操作日志、SQL/Kafka 保留策略限制，需要持续观测和容量调整。
