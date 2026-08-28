---
level: L2
feature: 001-local-mock-stream-gateway
created: 2026-08-28
---

# 本机 Mock 流式网关与双客户端切片

## 原始需求

建立 AgentMesh 的第一个可运行 Go 垂直切片，证明它可被 SQL 诊断 CLI 和文档摘要 CLI 复用，而不是 SQL Sentinel 的内部库。

## 目标

- 建立 Go module、最小 HTTP API 与可注入的 Provider/Router 契约。
- 仅用可配置 mock Provider 实现 OpenAI 风格 `POST /v1/chat/completions` 的 SSE 流式转发。
- 在首个 SSE 数据块前失败时按 Router 顺序尝试备用 mock；首块后失败只发送规范化流错误，绝不切换。
- 让请求 Context 传至 Provider；客户端取消须停止 mock 流并可由测试观察。
- 提供两个本地 CLI 演示客户端：SQL 诊断提示与文档摘要提示。

## 非目标

- 不实现 API Key、JWT、Casbin、Redis、MySQL、配额、账单、Eino、真实 Ark/Ollama 或管理面。
- 不监听非回环地址，不做生产网络暴露、自动重试、缓存或流开始后的 Provider 切换。

## 默认假设

- 服务默认且仅允许 `127.0.0.1`；测试使用 `httptest` 与 mock，不需要 Docker 或网络凭据。
- 请求内容都是不可信数据，仅作为 Provider 输入；日志和响应不得包含密钥。
- 首版 SSE 使用 `data: {json}\n\n` 帧，结束使用 `data: [DONE]\n\n`；错误码是稳定机器码。

## 验收条件

- `go run ./cmd/api --addr 127.0.0.1:18080` 可启动健康检查与 chat API；非回环地址被拒绝。
- 两个 CLI 都能将自己的固定输入流式调用到同一 API，并显示 mock 输出。
- mock 首块前失败时请求由备用 Provider 成功完成；首块后失败不触发备用 Provider。
- 主动取消 SSE 请求后 mock 收到 Context 取消，相关测试不遗留流 goroutine。
- 所有行为可离线测试；README 明示 mock-only 与未实现边界。
