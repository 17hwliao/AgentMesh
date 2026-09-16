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
