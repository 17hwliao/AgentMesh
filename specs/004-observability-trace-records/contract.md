# Trace 查询 HTTP 契约

## 适用端点

`GET /v1/observability/traces/{trace_id}` 需要已有的 `Authorization: Bearer <raw API key>`。trace ID 只由服务端在认证后的 chat 请求开始时生成，并通过响应 `X-AgentMesh-Trace-ID` 返回；chat、CLI 和查询请求都不能提供或覆盖它。`/healthz`、chat SSE 和 `/health/providers` 的既有契约不变。

## 响应

成功仅返回当前 tenant 的已完成安全摘要：`trace_id`、`model`、按顺序的 attempt 名称/结果、`first_chunk_latency_ms`、`total_latency_ms`、稳定 `result_code`、`cancelled`，以及可选 `provider_usage` 与明确的 `usage_observed`。绝不返回 tenant ID、prompt、messages、delta、raw key、prefix/hash、endpoint、Provider 原始错误或未观测 Token 估算。

## 拒绝响应

| 条件 | HTTP | JSON error.code | 副作用 |
| --- | --- | --- | --- |
| 缺失、畸形、未知或禁用 key | 401 | `auth_failed` | 不查询 Recorder，不触发 Provider |
| trace 不存在、属于其他 tenant 或尚未完成 | 404 | `trace_not_found` | 不泄露存在性，不触发 Provider |
| 方法不允许 | 405 | `method_not_allowed` | 不触发 Provider |

响应错误只包含稳定 code。Recorder 满且无法记录 pending trace 不是 HTTP 成功/失败证据；业务流保持原有 Router 语义，查询该请求时按 `trace_not_found` 处理。
