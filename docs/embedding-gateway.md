# Embedding Gateway

AgentMesh 的 embedding 能力与 SSE chat Provider 分离，入口为受 API key 和 tenant model route 保护的 `POST /v1/embeddings`。

## 离线 demo

```powershell
go run ./cmd/embedding-demo
```

它只使用 deterministic mock 和进程内 auth，不访问外部服务。

## API 启动

默认 `cmd/api` 会构造离线 mock embedding provider。tenant route 中例如：

```json
{"embed-model":["mock"]}
```

即可让该 tenant 调用 `model=embed-model`。请求的 API key 仍使用现有 AgentMesh 鉴权配置。

显式启用 Ollama：

```powershell
$env:AGENTMESH_EMBEDDING_PROVIDER='ollama'
$env:AGENTMESH_EMBEDDING_BASE_URL='http://127.0.0.1:11434'
$env:AGENTMESH_EMBEDDING_MODEL='nomic-embed-text'
go run ./cmd/api --providers ollama
```

tenant route 需要把对应 logical model 指向 `ollama`；缺少 embedding 配置时启动以稳定 configuration error 拒绝。本仓库没有声称真实 Ollama/cloud 调用已经成功。

响应只返回向量和最小 trace metadata，不返回原始 input、API key、endpoint 或 upstream 原始错误。

## 本机 Ollama 与受限 RAG answer

可选的本机 Compose 只启动 Ollama；拉取 embedding/chat 模型仍需操作者显式执行，不能把容器启动当成真实模型 smoke 成功：

```powershell
docker compose -f deployments/docker-compose.ollama.yml up -d
ollama pull nomic-embed-text
ollama pull qwen2.5:7b
```

`POST /v1/embeddings` 保持 SQL Sentinel HTTP adapter 所需的 OpenAI 形状：`data[index].embedding`，并支持同一 tenant route 的 logical model。真实 embedding 仅在 `AGENTMESH_EMBEDDING_PROVIDER=ollama`、`AGENTMESH_EMBEDDING_BASE_URL`、`AGENTMESH_EMBEDDING_MODEL` 都提供时启用；现有 chat resolver 也要求 `--providers ollama` 的 `OLLAMA_BASE_URL`、`OLLAMA_MODEL`。

`POST /v1/rag/answers` 是供后续 RAG query 接口传入已经检索的 chunks 的受限回答层。每个 chunk 带 `source_uri/document_id/version/chunk_id/hash/content`，hash 必须是 content 的 SHA-256；每条 `facts[]` 项目必须回指一个完整输入 citation。Ollama 仅输出一基证据序号，网关将其绑定为原始、哈希校验过的完整 citation，因此模型无法伪造 source/version/chunk/hash。没有证据只允许 `insufficient_evidence` 拒答；SQL、DDL、执行计划、索引/性能裁决在模型调用前拒绝。fake 默认离线，Ollama answer 要显式设置 `AGENTMESH_RAG_ANSWER_PROVIDER=ollama`、`AGENTMESH_RAG_ANSWER_BASE_URL`、`AGENTMESH_RAG_ANSWER_MODEL` 和可选 1–300 秒 `AGENTMESH_RAG_ANSWER_TIMEOUT_SECONDS`。

真实 answer smoke 需要操作者明确确认非生产 endpoint：

```powershell
$env:AGENTMESH_REAL_RAG_ANSWER_VALIDATION='1'
$env:AGENTMESH_RAG_ANSWER_BASE_URL='http://127.0.0.1:11434'
$env:AGENTMESH_RAG_ANSWER_MODEL='qwen2.5:7b'
make verify-real-rag-answer
```

缺少门禁或配置时脚本输出受控 `verification_unavailable`、不发网络请求并非零退出。2026-09-11 已使用本机 Docker `qwen2.5:7b` 完成一次真实 answer/citation smoke；该事实不外推为云端可用性、模型质量基准或生产 SLA。
