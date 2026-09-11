# 实现计划

1. 建立独立 embedding provider/errors/config contract。
2. 实现 deterministic mock 和 Ollama `/api/embed` adapter。
3. 实现严格请求解码、输入/维度限制、tenant model route、错误映射和安全 trace metadata。
4. 在 gateway 增加可选、受认证保护的 `/v1/embeddings` handler，并在 `cmd/api` 显式构造 provider。
5. 添加 provider、HTTP contract、config refusal 和 demo 验证。
6. 执行全量 test/build/vet/gofmt/diff check，记录真实外部服务未验证边界。
