# AgentMesh：多租户 LLM 网关与 Agent Runtime

## 1. 项目目标

AgentMesh 是面向多个 AI 应用的 Go 模型运行时。它统一处理模型 Provider 接入、流式转发、租户鉴权、Token 配额、限流和调用观测，让业务 Agent 专注于自身逻辑。

首版接入两个独立客户端：

1. SQL Sentinel；
2. 一个极简文档摘要 CLI。

这证明它是可复用平台，而不是 SQL Sentinel 的内部工具。

## 1.1 当前可运行切片（001）

当前仓库已提供一个**仅本机、仅 mock** 的最小 SSE 网关，以及两个通过 HTTP 使用它的 CLI。它用于验证
Provider 路由、首块前 fallback、SSE 转发和取消传播；不表示 Ark/豆包、Ollama、API Key、Redis、MySQL、
配额或账单已接入。

在第一个终端启动（只接受 `127.0.0.1:PORT`，拒绝局域网和通配监听）：

```powershell
go run ./cmd/api --addr 127.0.0.1:18080
```

在另一个终端分别运行两个客户端：

```powershell
go run ./cmd/summary-cli --text 'AgentMesh streams local mock output.'
go run ./cmd/sql-diagnose-cli --sql 'SELECT id FROM users WHERE status = ''active'''
```

两条命令都会得到 `mock response`。默认 primary mock 在首个 SSE 数据块前失败，Router 因而使用 fallback mock；
若已经转发任一数据块，后续失败只产生 `stream_interrupted`，绝不切换模型。SQL CLI 只把 SQL 当作提示文本，
不会执行 SQL。没有密钥、真实模型调用或 Docker 操作。

## 1.2 真实 Provider Adapter（002）

Ark 与 Ollama Adapter 已具备离线 fixture 测试，但默认仍为 `--providers mock`。只有操作者显式选择
`--providers ark,ollama` 时，服务才读取以下进程环境变量：`ARK_BASE_URL`、`ARK_MODEL`、`ARK_API_KEY`、
`OLLAMA_BASE_URL`、`OLLAMA_MODEL`。不要把这些值写入 flags、配置文件、日志、终端录制或 Git。

```powershell
go run ./cmd/api --providers ark,ollama
```

任一必填值缺失或格式无效时，服务在创建 HTTP client 前以 `provider_configuration_missing` 或
`provider_configuration_invalid` 退出，且不会尝试 Provider 网络连接。`GET /health/providers` 只返回当前
Provider 的名称和布尔健康状态；它不返回 endpoint、模型或密钥，且 health 成功不等于一次 chat 流成功。

本仓库在 2026-08-28 的受控 T003 运行中，五个必需环境变量均不存在；上述命令实际返回
`provider_configuration_missing`、exit 1，Provider 尝试数为 0。真实调用即使成功也只证明本次 Adapter
可连接，不表示已实现租户鉴权、配额、账单或精确 Token 计费。

## 2. 问题边界

### 要解决

- 多个 Agent 应用重复实现模型接入、SSE、用量统计、超时和取消的问题；
- 不同租户的 API Key、并发和 Token 预算隔离问题；
- 上游模型首字节前故障时的可控降级；
- 模型调用缺少可观测性，无法定位慢请求、失败和成本的问题。

### 首版不解决

- 不复刻 One API 或 Higress 的完整能力；
- 不实现三个以上 Provider；
- 不实现流式输出开始后的无缝模型切换；
- 不做语义缓存、复杂账单、Kubernetes、多 Agent 市场。

## 3. 总体架构

```mermaid
flowchart LR
    C1["SQL Sentinel"] --> API["AgentMesh HTTP API"]
    C2["Document Summary CLI"] --> API
    API --> AUTH["API Key / Tenant"]
    AUTH --> QUOTA["Redis Lua 配额与限流"]
    QUOTA --> ROUTER["Provider Router"]
    ROUTER --> ARK["Ark / 豆包"]
    ROUTER --> OLLAMA["Ollama"]
    ROUTER --> SSE["SSE Stream Relay"]
    SSE --> C1
    SSE --> C2
    API --> DB[("MySQL")]
    API --> OBS["Eino Callback + OTel"]
    OBS --> METRICS["Prometheus / Grafana"]
```

## 4. 关键设计

### 4.1 多租户与权限

- API Key 只保存哈希值和前缀；明文只在创建时返回一次；
- 每个 Key 绑定 `tenant_id`、可用模型、速率上限和 Token 周期预算；
- JWT 用于管理后台登录；Casbin 控制平台管理员、租户管理员和开发者的管理权限；
- 业务调用只凭 API Key，不把 JWT 混入服务间接口。

### 4.2 Provider 路由与降级

- V0 支持 Ark/豆包与 Ollama；路由依据租户允许列表、显式模型选择和健康状态；
- 在第一个 SSE 数据块发送给客户端前，上游连接失败可以尝试备用 Provider；
- 首 Token 已发送后禁止切换 Provider。此时返回规范化流错误，由客户端发起“重新生成”；
- 每次 Provider attempt 都单独记录并累计用量；首 Token 前重试不覆盖第一次 attempt 已产生的 input 成本；
- Provider 实现通过 Adapter 隔离，路由通过 Strategy 实现，不泄漏到业务层。

### 4.3 SSE、超时与取消

- 每个 HTTP 请求创建根 `context.Context`；客户端断连、超时或主动取消时向下游传递取消；
- 上游 HTTP 请求、Eino Stream、内部 goroutine 都监听同一派生 Context；
- 流结束时统一关闭 channel、停止计时器、回收临时资源；
- 测试通过主动断开 N 次 SSE 连接后，验证 goroutine 数回到稳定基线。

### 4.4 配额与限流

- Redis Lua 在单次脚本中检查与扣减 Token 预算，避免并发下超额；
- 令牌桶限制单位时间请求数；
- V0 只验证预扣与正常路径结算；V1 补齐 Reservation 状态机、崩溃恢复和异步账单，不能把 V0 当成生产级计费链路；
- 对流式请求，预扣保守预算；Provider usage 可用时按其结算。不可用时，按模型对应 tokenizer 计算输入与已转发文本，标记为“本地可观测下界”，而非 Provider 精确账单；
- 用量账单通过 outbox 异步写 MySQL，热路径不做重型聚合。

### 4.5 Reservation 状态机（V1）

Reservation 的终态只有 `settled` 与 `cancelled`；`expired_pending` 是等待 reconciler 裁决的中间状态，不是终态。

```mermaid
stateDiagram-v2
    [*] --> creating: MySQL 持久化 reservation
    creating --> reserved: Redis 预扣成功
    creating --> cancelled: Redis 预扣失败 / 本地拒绝
    reserved --> cancelled: 本地拒绝或确认未发起 Provider
    reserved --> streaming: 已发起任一 Provider attempt
    streaming --> settled: 成功、失败、断流或客户端取消
    reserved --> expired_pending: 心跳超时
    streaming --> expired_pending: 心跳超时 / 进程崩溃
    expired_pending --> cancelled: 无 attempt 痕迹
    expired_pending --> settled: 有 attempt / 已转发计量
```

- `cancelled` 只适用于配额/限流拒绝，或能确认 Provider attempt 从未发起的本地失败；
- 任一 attempt 发起后，即使首字节前失败、流中断或客户端取消，也必须 `settled`；释放的只能是确定未使用的预约额度；
- `provider_attempts` 的“已发起”标记必须在调用 Provider Adapter 前持久化；一旦 Adapter 调用已经开始，哪怕网络错误无法判断请求是否到达上游，也保守地按 `settled` 处理；
- fallback attempt 的 input 与已转发 output 用量累加，路由器在下一次 attempt 前重新检查 Reservation 剩余额度；
- `quota_reservations` 在发起 Provider 前持久化 `reservation_id`、`request_id`、状态、attempt 痕迹、已转发文本/本地 Token 计量、心跳和版本号；reconciler 据此幂等裁决；
- Provider 未提供 usage 时，本地计量只反映网关确认已发送/转发的内容；Provider 可能已生成但网关未观察到的部分不向租户收费，并作为平台侧不确定成本记录。

### 4.6 可观测性与版本化

- 使用 Eino Callback 记录模型、Provider、模型版本、Prompt 版本、Token、首 Token 延迟、总耗时、错误与取消原因；
- 通过 OpenTelemetry 贯穿 trace_id，Prometheus 暴露指标；
- 敏感 Prompt、参数和响应只记录脱敏摘要或经过加密、访问审计后的内容；
- 评测和成本统计绑定 `model_version + prompt_version + dataset_version`。

## 5. 技术栈与选型理由

| 技术 | 用途 | 为什么需要 |
| --- | --- | --- |
| Go / net-http / Context | API、SSE、取消传播 | 流式模型调用本身要求长连接与资源回收 |
| Eino / Eino Ext | 模型适配、流和 Callback | 避免业务直接耦合具体 Provider |
| Go-zero | 管理面 REST API、中间件 | 管理端是标准的配置与权限接口 |
| MySQL + GORM | 租户、Key、Reservation、用量账单 | 强一致业务元数据与可恢复的配额状态 |
| Redis + Lua | 限流、Token 预扣与结算 | 原子扣减和高频热路径 |
| JWT + Casbin | 管理面鉴权与 RBAC | 多租户后台必须区分操作权限 |
| Viper + Zap | 多环境配置、结构化日志 | Provider 配置和故障定位需要规范化 |
| OTel + Prometheus | Trace、指标、告警 | 验证延迟、失败、取消和成本 |
| Docker Compose | 本地运行 Ark Mock、Ollama、Redis、MySQL | 一键复现开发和测试环境 |

`gRPC` 不是首版必选。只有 AgentRunner 被独立为进程且需要同步强类型调用、进度流和跨进程取消时才引入。

## 6. 数据模型（建议）

- `tenants`：租户信息、状态、默认配额；
- `api_keys`：Key 前缀、哈希、所属租户、权限与状态；
- `model_routes`：租户可用模型、优先级、故障策略；
- `quota_reservations`：请求 ID、预扣额度、状态、attempt 痕迹、本地计量、心跳、版本；
- `provider_attempts`：每次路由/降级尝试的 Provider、模型、开始时间、usage 与错误；
- `usage_records`：请求 ID、模型、Token、计费单位、延迟、结果；
- `usage_outbox`：与 Reservation 状态一同提交的异步账单事件；
- `prompt_versions`：版本号、模板摘要、状态、发布时间。

## 7. API（建议）

- `POST /v1/chat/completions`：OpenAI 风格模型调用，支持 `stream=true`；
- `POST /v1/agent/runs`：规划接口，V0 不实现；只有确需托管 Agent 生命周期时再引入；
- `POST /admin/api-keys`：创建 Key；
- `GET /admin/usage`：按租户和模型查询用量；
- `GET /health/providers`：Provider 健康状态。

## 8. 验收与指标

使用本地 mock Provider 单独测网关能力，真实模型只记录端到端体验：

- 网关自身增加的 P99 延迟；
- 固定并发下成功率、错误分类和限流正确率；
- 首 Token 前降级成功率；
- 主动断连后的取消传播时间和 goroutine 泄漏测试；
- 租户 A/B 配额隔离测试；
- 同一租户 1000 并发预扣测试：Redis Lua 结果不能透支预算；
- Token 预扣和最终结算一致性。
- Reservation 崩溃恢复、重复结算/取消幂等和 Provider usage 差异标记（V1）。

## 9. 推荐目录

```text
agentmesh/
  cmd/{api,summary-cli}/
  internal/{auth,tenant,quota,router,provider,stream,usage,observability}/
  pkg/{einoadapter,contracts}/
  migrations/
  deployments/docker-compose.yml
  tests/
```
