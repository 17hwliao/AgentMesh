# HTTP 鉴权契约变更

## 适用端点

`POST /v1/chat/completions` 与 `GET /health/providers` 从 003 起必须有 `Authorization: Bearer <raw API key>`。`GET /healthz` 保持公开，响应固定为 `{"status":"ok"}`。

## 拒绝响应

| 条件 | HTTP | JSON error.code | 处理顺序 |
| --- | --- | --- | --- |
| 缺失、畸形、未知或禁用 Key | 401 | `auth_failed` | 不读 body，0 Provider 调用 |
| tenant 不允许请求 model | 403 | `model_not_allowed` | 认证后解码请求，0 Provider 调用 |
| 后续 QuotaGate 拒绝（本阶段仅预留） | 429 | `quota_exhausted` | 认证后解码请求，不发起 Provider attempt |
| bootstrap 配置非法 | 进程不监听 | `auth_configuration_missing` / `auth_configuration_invalid`（stderr） | 不创建 Gateway/Provider 请求 |

响应不得包含 header 原文、Key、摘要、tenant、endpoint、model、Provider body 或比较细节。错误 JSON 只含稳定 code。

## 成功语义

认证后 Store 根据配置 tenant 和请求 model 返回 Provider 顺序，再进入既有 Router；SSE shape、首 chunk fallback 与首 chunk 后 `stream_interrupted` 不变。tenant identity 只存在于服务端 Context，不能由客户端覆盖。

## 客户端兼容性

两个 CLI 只从 `AGENTMESH_API_KEY` 环境读取 raw key 并发送 header；不接受 key flag。旧无 header 调用变为 401 `auth_failed`，这是有意的安全破坏性变更。
