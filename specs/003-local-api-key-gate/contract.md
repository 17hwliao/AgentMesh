# AgentMesh HTTP 契约（事实基线：6753800）

本文件合并 003、004、012、013 的**已实现** HTTP 事实。它不承诺真实 MySQL、Redis 或 Provider 已成功联调；持久 auth mode 只有在运行时显式配置后才注册管理端点。

## 全局响应与安全边界

- JSON 错误固定为 `{"error":{"code":"<stable_code>"}}`；不返回 Key、hash、prefix、tenant ID、DSN、endpoint、prompt、message、delta、Provider 原始错误或鉴权比较细节。
- 仅 `GET /healthz` 公开。所有业务/trace 端点使用 tenant API Key；管理端点使用独立 admin token，二者不可互换。
- 除 SSE 流中已开始后的错误帧外，拒绝使用 JSON。SSE 成功/已开始响应使用 `text/event-stream`；流在首块后失败时以 SSE `stream_interrupted` 错误帧结束，绝不 fallback。
- `X-AgentMesh-Trace-ID` 只由服务端生成。它在已认证、strict JSON 成功的 chat 请求中于 model route 判定前设置；表示可查询诊断会话，并不表示路由或流最终成功。

## 端点矩阵

| 端点 | 认证与可用性 | 成功 | 主要拒绝/边界 | Provider attempt |
| --- | --- | --- | --- | --- |
| `GET /healthz` | 始终公开 | 200 `{"status":"ok"}` | 非 GET：405 `method_not_allowed` | 从不 |
| `POST /v1/chat/completions` | tenant API Key | 200 SSE；可有 trace header | 见 chat 顺序表 | 仅到最后一步后 |
| `GET /health/providers` | tenant API Key | 200，仅当前 tenant 可见的 `{name, healthy}` 列表 | 401 `auth_failed`；非 GET：405 `method_not_allowed` | 不发起 chat attempt；只执行 adapter health |
| `GET /v1/observability/traces/{trace_id}` | tenant API Key | 200，当前 tenant 的已完成安全 trace 摘要 | 401 `auth_failed`；非 GET：405 `method_not_allowed`；unknown/cross-tenant/pending：404 `trace_not_found` | 从不 |
| `POST /admin/tenants` | 仅持久 auth mode 注册；admin token | 201 `tenant_id` | 见 admin 顺序表 | 从不 |
| `POST /admin/api-keys` | 同上 | 201 `key_id` 与仅此一次的原始 `api_key` | 见 admin 顺序表 | 从不 |
| `DELETE /admin/api-keys/{key_id}` | 同上 | 204；重复撤销保持幂等 | 见 admin 顺序表 | 从不 |

## Chat 处理顺序

生产 API 的 chat 路由由 tenant 鉴权 middleware 包住；因此 tenant 鉴权先于 handler 方法检查。RateGate 只在已认证 `POST` 上运行；未配置时为无类型 nil，直接跳过。

| 顺序 | 条件/动作 | 响应 | 是否读 body / 触发下游 |
| --- | --- | --- | --- |
| 1 | 缺失、畸形、未知或禁用 tenant Key | 401 `auth_failed` | 不读 body；不建限流桶、不路由、不调用 Provider |
| 2 | 已认证 POST 被每进程 tenant RateGate 拒绝 | 429 `rate_limited` + 正整数 `Retry-After` | 不读 body；不进 model/Quota/Reservation/Provider |
| 3 | 已认证但不是 POST | 405 `method_not_allowed` | 不读 body；不进 model/Quota/Reservation/Provider |
| 4 | strict JSON 失败、未知字段、超 64 KiB 或 trailing 数据 | 400 `invalid_chat_request` | 已读 body；不路由、不调用 Provider |
| 5 | strict JSON 成功，创建 trace session 并设置 header | 继续 | 此时还未判定 model；后续拒绝仍可能带 header |
| 6 | tenant 不允许 `model` | 403 `model_not_allowed` | 已解码；不进 Quota/Reservation/Provider |
| 7 | 可注入 QuotaGate 拒绝 | 429 `quota_exhausted` | 已解码/路由；不进 Reservation/Provider |
| 8 | 可选 Reservation Begin 拒绝或存储不可用 | 429 `quota_exhausted` 或 503 `quota_unavailable` | 已解码/路由；不调 Provider Adapter |
| 9 | Provider 不可用，或首块前所有候选失败 | 503 `provider_unavailable` | 仅此时可有 attempt；首块前才允许既有 fallback |
| 10 | 首 SSE 块后失败 | SSE `stream_interrupted` 错误帧与 done | 不切换 Provider；Reservation 仍保守结算 |

`rate_limited` 表示请求频率，不等价于 Token 预算；`quota_exhausted` 表示既有 Quota/Reservation 边界。两个码均为 429，但只有前者必须带 `Retry-After`。客户端取消/超时沿 request Context 传播，不伪造 JSON 成功或 fallback。

## Trace 查询契约

trace 摘要只包含 `trace_id`、model、按序 attempt 名称/结果、首块/总耗时、稳定 `result_code`、`cancelled`、`usage_observed` 和 Provider 明示 usage。它不返回 tenant ID、输入输出文本、原始 Key、endpoint 或未观测 Token 估算。

缺失/畸形/未知/禁用 Key 在 Recorder 查询前返回 401；已认证的 unknown、其他 tenant 所属或尚未完成 trace 都返回同一 404 `trace_not_found`，不泄露存在性，也不触发 Provider。Recorder 容量不足导致未记录 pending trace 时同样按此 404 表示，业务流本身不因此失败。

## 管理面处理顺序与响应

管理路由仅在 `AGENTMESH_AUTH_STORE=mysql` 的完整持久配置成功时注册，并继续受 `127.0.0.1` 监听限制。其 Bearer 值与 tenant API Key 独立，以固定长度 digest 常量时间比较。

### 持久身份库启动状态（019）

只有 `tenants`、`tenant_model_routes`、`api_keys` 三表均无记录的受控新部署会启动为本地管理引导态：管理 API 可由既有 admin token 调用，但没有有效 tenant Key，因此业务/trace/provider-health 请求仍在 body、路由、限流、配额或 Provider 前以 401 `auth_failed` 拒绝。创建首个合法 tenant/Key 后，下一请求直接读取 Store 并进入既有业务契约，无需重启。

任一表存在部分数据、route 缺失/无效或启动读取失败都不是引导态，服务以既有 `tenant_route_configuration_invalid` 启动拒绝停止，不提供 HTTP 服务。启动读取有独立有界 Context；认证、chat route 与 provider-health 可见 route 使用各自 HTTP request Context，取消或超时不得被转化为认证成功或 Provider attempt。

| 顺序 | 条件/动作 | 响应 | body / 副作用 |
| --- | --- | --- | --- |
| 1 | 缺失、畸形或错误 admin token | 401 `admin_auth_failed` | 不读 body；不调用 lifecycle |
| 2 | 路径或方法不匹配 | 404 `admin_request_invalid` | 不读业务 body；不调用 lifecycle |
| 3 | POST strict JSON、大小上限或 route 验证失败 | 400 `admin_request_invalid` | 只在 token 成功后读 body；不调用 lifecycle |
| 4 | 创建 tenant 已存在 | 409 `tenant_exists` | lifecycle 已执行，零 Provider |
| 5 | 创建 Key 的 tenant 不存在/禁用 | 404 `tenant_not_found` | 零 Provider |
| 6 | 删除 Key 不存在 | 404 `api_key_not_found` | 零 Provider |
| 7 | 未分类 lifecycle 存储失败 | 503 `admin_operation_failed` | 错误不回显底层细节 |

成功创建 Key 的 201 响应是原始 Key 唯一允许出现的位置；数据库、日志、trace、后续查询和错误响应均不得保存或回显它。撤销以 `key_id` 精确执行，成功重放仍为 204；下一 tenant 业务请求即因认证读取最新状态而得到 `auth_failed`。

## 事实测试对应

- chat 鉴权/Quota 边界：`internal/gateway/auth_test.go`；限流 body 前边界与 tenant 隔离：`internal/gateway/rate_limit_test.go`。
- trace header、安全摘要与 404 收敛：`internal/gateway/trace_test.go`。
- admin token 先于 body、一次性 Key 与幂等撤销：`internal/admin/handler_test.go`。
- 本契约合并批新增的公开 health/provider-health 事实：`internal/gateway/contract_test.go`。
