---
level: L1
feature: 023-embedding-gateway
created: 2026-09-10
---

# AgentMesh Embedding Gateway

## 目标

在 AgentMesh 现有 API-key/tenant 边界内增加独立的非流式 `POST /v1/embeddings` 能力，为 SQL Sentinel RAG 等应用提供统一 embedding provider 入口。

## 范围

- 请求必须经过现有 Bearer API key 鉴权；tenant 从认证 context 获取，不能由请求体伪造。
- `model` 必须存在于该 tenant 的 model route；route 中 provider 名称必须是进程显式构造的 embedding provider。
- `input` 支持单字符串或字符串数组；最多 32 个输入，每项最多 8192 bytes，拒绝空值、未知 JSON 字段和 trailing JSON。
- 定义独立 embedding Provider contract，不复用 chat stream Provider。
- 提供 deterministic offline mock；Ollama `/api/embed` adapter 只在显式 endpoint/model 配置下启用，缺失时 controlled refusal。
- 响应包含 embedding vectors、logical model、provider、input count、dimension 和不含 prompt 的最小 trace metadata。
- 不记录或返回原始输入、API key、upstream endpoint 或 provider 原始错误。

## 非目标

- 不声称云 provider 或真实 Ollama 已经成功联调。
- 不把 embedding request 混入 SSE chat、SQL 执行、RAG vector store 或性能资格裁决。
- 不在本特性中实现 billing/token 估算；只保留 provider/trace 的最小安全元数据。

## 验收标准

1. 未鉴权请求为 401；tenant 未授权 model 为 403；非法 body 为 400。
2. provider 不可用为 503，协议/维度错误为 502，错误响应不泄漏输入或敏感配置。
3. mock 输出确定性；Ollama adapter 请求 `/api/embed` 并校验 batch count/dimension。
4. 通过 HTTP contract tests 验证鉴权、tenant route、限制、trace header、错误映射和成功响应。
5. `go test ./...`、`go vet ./...`、`go build ./...`、`gofmt` 和 `git diff --check` 通过。
