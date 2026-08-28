# 实施计划

## 实现决策

- 新增 `internal/provider/ark` 与 `internal/provider/ollama`，都只实现现有 `Provider` 契约；请求、流和 health 的协议差异不得进入 Router/Gateway。
- 新增严格运行时配置加载器：允许的 Provider 名称、必填环境变量、URL 的 scheme/host 和重复顺序在启动前检查；错误只暴露稳定码。
- `cmd/api --providers mock` 保持 001 默认行为；只有显式 real order 才读取环境并构造真实 Adapter。真实 Provider 的 HTTP client 必须有超时并使用请求 Context。
- 上游 fixture 使用 `httptest`，覆盖 Ark 与 Ollama 的正常 stream、首块前失败、首块后失败、错误帧、health 及取消；不需要密钥、Docker 或外网。
- `/health/providers` 对当前已构造的 Adapter 执行 health 并仅返回名称/状态；health 失败不是 chat 流成功或 fallback 的声明。

## 数据流

`cmd/api --providers ark,ollama` → 环境配置校验 → Ark/Ollama Adapter → 既有 Router → Gateway SSE。

Adapter 从 HTTP 上游读取 chunk，再将统一 `provider.Chunk` 交给 Router；请求取消沿已有 HTTP Context 传递。配置与密钥不写入响应、事件或日志。

## 任务顺序

1. 实现 Provider 选择/严格环境配置与 Ark Adapter 的 fixture 流、错误、取消、health 测试；保留 mock 默认路径。
2. 实现 Ollama Adapter、`/health/providers` 与两 Adapter 的 fallback 集成测试；验证启动时的稳定配置拒绝。
3. 仅在显式可用环境下进行真实 Provider 尝试，记录 Provider 名称、模型、尝试次数和结果；否则记录受控拒绝及 0 网络尝试。更新 README。
4. 全量验证、私有复盘、提交并 fast-forward `master`；不 push/tag。

## 风险与降级

- Ark/Ollama 上游协议或本地可用性可能不同；fixture 是实现正确性的主证据，真实 T003 失败只作为受控事实，不改写为“两个真实 Provider 已接入”。
- API Key 只进入 Authorization 请求头；错误格式不包含原始上游 body，避免将密钥/Prompt 回显到客户端。
- 没有真实配置时不调用 DNS/HTTP；不以安装/启动 Docker 来制造 T003 成功。

## T003 真实实验记录

2026-08-28：`ARK_BASE_URL`、`ARK_MODEL`、`ARK_API_KEY`、`OLLAMA_BASE_URL`、`OLLAMA_MODEL` 均不存在。执行 `go run ./cmd/api --providers ark,ollama` 实际输出 `provider_configuration_missing`、exit 1；配置加载在构造 Adapter/HTTP client 前停止，Provider 尝试数为 0。未操作 Docker、未调用外部 Provider，也不把 fixture 通过写成真实连接成功。

## 章程

暂未建立；本 feature 不以章程为阻塞条件。
