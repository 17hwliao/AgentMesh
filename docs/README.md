# AgentMesh 文档导航

本目录是现有资料的索引：不移动、不删除、不覆盖 README、规格、决策或私有阶段记录。运行时行为以代码和 015 HTTP 契约为准；路线图只说明计划和边界。

## 推荐阅读路径

1. [项目 README](../README.md)：当前能力、运行方式、已验证事实和限制。
2. [实施步骤](../实施步骤.md)：V0/V1 路线图与 DoD 对照；“建议”或“后置”不代表已经落地。
3. `specs/`：每个 feature 的原始范围、任务和高风险契约/迁移计划。
4. `decisions/`：跨 feature 的持久化设计取舍。
5. `.private/stage-records/`：本机复盘和真实验证摘要，Git 不跟踪。

## 规格与决策索引

| 范围 | 文档 |
| --- | --- |
| 网关、真实 Adapter、鉴权 | [001](../specs/001-local-mock-stream-gateway/spec.md)、[002](../specs/002-real-provider-adapters/spec.md)、[003 契约](../specs/003-local-api-key-gate/contract.md) |
| Trace、配额与账务 | [004 契约](../specs/004-observability-trace-records/contract.md)、[005](../specs/005-quota-reservation-state/spec.md)、[006 迁移](../specs/006-quota-reservation-persistence/migration-plan.md)、[011](../specs/011-usage-outbox-ledger/spec.md) |
| 认证、限流与 API 契约 | [012](../specs/012-token-bucket-rate-limit/spec.md)、[013](../specs/013-tenant-key-persistence/spec.md)、[015](../specs/015-api-contract-consolidation/spec.md) |
| 实测与演示 | [014](../specs/014-real-provider-evidence/spec.md)、[017](../specs/017-local-concurrency-benchmark/spec.md)、[018](../specs/018-admin-api-demo/spec.md)、[021](../specs/021-single-provider-verification/spec.md) |
| 长期取舍 | [Provider 边界](../decisions/001-provider-adapter-boundary.md)、[Reservation 结算](../decisions/002-reservation-settlement-boundary.md)、[gRPC 延后](../decisions/003-grpc-deferred.md)、[估算用量不退款](../decisions/004-estimated-usage-no-refund.md) |

## 已验证事实与边界

- 单机 loopback + mock 压测：1000/1000 SSE 成功，p50 2.28ms、p95 5.50ms、约 7,355 req/s；该数字不外推到生产或真实 Provider。
- 已完成一次本机 Ollama `qwen2.5:7b` 的真实 SSE 往返；Ark 仍是 fixture 验证，不能写成双 Provider 均已真实联调。
- SQL Sentinel 已作为首个外部消费者，通过公开 `/v1/chat/completions` 契约完成一次 HTTP 200、trace 和 `[DONE]` 完整的本机联桥。该事实只代表传输层互操作。
- MySQL/Redis 真实存储实证仍依赖操作者提供一次性非生产环境；替身、受控拒绝和真实成功必须分开陈述。

## 代码入口

| 目的 | 代码 |
| --- | --- |
| HTTP 服务 | [`cmd/api/`](../cmd/api/) 与 [`internal/gateway/`](../internal/gateway/) |
| 身份、租户与管理 API | [`internal/auth/`](../internal/auth/)、[`internal/tenant/`](../internal/tenant/)、[`internal/admin/`](../internal/admin/) |
| Provider 与 SSE | [`internal/provider/`](../internal/provider/)、[`internal/router/`](../internal/router/)、[`internal/gatewayclient/`](../internal/gatewayclient/) |
| 配额、账务、限流 | [`internal/reservation/`](../internal/reservation/)（含 ledger）、[`internal/ratelimit/`](../internal/ratelimit/) |
| Trace 与本机评测 | [`internal/observability/`](../internal/observability/)、[`internal/localbench/`](../internal/localbench/)、[`scripts/`](../scripts/) |

## 面试材料

本地可查看 `.private/resume-bullets.md` 和 `.private/stage-records/`。两者均只记录已验证数据与边界；公开 README 是项目说明的首选入口。
