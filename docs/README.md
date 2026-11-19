# AgentMesh 文档导航

2026-10-19：[用户管理与私有图片 API](account-images.md)，包含迁移、存储配额、鉴权和读取边界。

2026-10-16：[OpenCode / Claude Code 接入](client-integration.md)、[New API 差异与优先级](new-api-gap-analysis.md)。模型发现、标准 Chat/工具、Messages 转换与两个实际 CLI 的千问 Read 往返已实现。[上下文与图片说明](gateway-protocol-design.md)保留设计背景。后端/计费另见 [扩展契约](backend-roadmap.md)；下方切片验证记录保留历史范围。

2026-11-16：[中转控制面路线图](RELAY-ROADMAP.zh-CN.md) 与 [单机控制面设计](standalone-control-plane.zh-CN.md) 已建立设计基线。S1a、S1b、S2、S2.5 已具备本地实现和自动测试；S3/S4 仅完成 loopback + httptest TLS 的本地模拟验收，均不构成真实局域网、公网或生产发布。规格见 [026](../specs/026-standalone-control-plane/spec.md)，决策见 [005](../decisions/005-standalone-control-plane.md)、[006](../decisions/006-free-pool-isolation.md)。阶段记录见 [S0](acceptance/S0.md)、[S1a](acceptance/S1a.md)、[S1b](acceptance/S1b.md)、[S2](acceptance/S2.md)、[S2.5](acceptance/S2.5.md)、[S3](acceptance/S3.md)、[S4 本地模拟](acceptance/S4-local-simulation.md)，复验命令见 [阶段复验入口](acceptance/README.md)。

2026-11-18：[S3.5 本机宿主与本地模型设计](S3.5-local-host-privacy.zh-CN.md) 已建立分阶段边界；S3.5a 开始实现只读 Agent 扫描与 Ollama/LM Studio 本地模型目录。订阅、配置自动写入、隐私、真实局域网和完整备份仍是后续阶段，规格见 [027](../specs/027-local-host-privacy/spec.md)，决策见 [007](../decisions/007-local-host-subscription-privacy.md)。

2026-11-19：[S3.5a 本机发现与本地模型](acceptance/S3.5a.md) 已通过 Windows 自动验收。后续配置接入、隐私、订阅、局域网和加密备份见 [S3.5 并行开发设计](../specs/027-local-host-privacy/parallel-design.md)。

本目录是现有资料的索引：不移动、不删除、不覆盖 README、规格、决策或私有阶段记录。运行时行为以代码和 015 HTTP 契约为准；路线图只说明计划和边界。

## 推荐阅读路径

1. [项目 README](../README.md)：当前能力、运行方式、已验证事实和限制。
2. [实施步骤](archive/roadmap.zh-CN.md)：V0/V1 路线图与 DoD 对照；“建议”或“后置”不代表已经落地。
3. `specs/`：每个 feature 的原始范围、任务和高风险契约/迁移计划。
4. `decisions/`：跨 feature 的持久化设计取舍。
5. `.private/stage-records/`：本机复盘和真实验证摘要，Git 不跟踪。

## 规格与决策索引

| 范围 | 文档 |
| --- | --- |
| 网关、真实 Adapter、鉴权 | [001](../specs/001-local-mock-stream-gateway/spec.md)、[002](../specs/002-real-provider-adapters/spec.md)、[003 契约](../specs/003-local-api-key-gate/contract.md) |
| Trace、配额与账务 | [004 契约](../specs/004-observability-trace-records/contract.md)、[005](../specs/005-quota-reservation-state/spec.md)、[006 迁移](../specs/006-quota-reservation-persistence/migration-plan.md)、[011](../specs/011-usage-outbox-ledger/spec.md) |
| 认证、限流与 API 契约 | [012](../specs/012-token-bucket-rate-limit/spec.md)、[013](../specs/013-tenant-key-persistence/spec.md)、[015](../specs/015-api-contract-consolidation/spec.md) |
| 中转控制面（设计/验收） | [路线图](RELAY-ROADMAP.zh-CN.md)、[单机设计](standalone-control-plane.zh-CN.md)、[S3.5 设计](S3.5-local-host-privacy.zh-CN.md)、[026](../specs/026-standalone-control-plane/spec.md)、[027](../specs/027-local-host-privacy/spec.md)、[决策 005](../decisions/005-standalone-control-plane.md)、[决策 007](../decisions/007-local-host-subscription-privacy.md)、[S0](acceptance/S0.md)、[S1a](acceptance/S1a.md)、[S1b](acceptance/S1b.md)、[S2](acceptance/S2.md)、[S2.5](acceptance/S2.5.md)、[S3](acceptance/S3.md)、[S4 本地模拟](acceptance/S4-local-simulation.md) |
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
