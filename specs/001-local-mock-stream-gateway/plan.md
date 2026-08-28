# 实施计划

## 实现决策

- 使用标准库 `net/http` 和 Go module；暂不引入 Eino/Go-zero，避免首个可运行切片耦合真实依赖。
- `internal/provider` 定义流式 Provider、chunk、usage 和健康检查最小契约；mock 可按固定 chunks 与失败位置注入。
- `internal/router` 只接收有序 Provider 列表：首 chunk 前可尝试下一个，已经转发任何 chunk 后仅返回 `stream_interrupted`。
- `internal/gateway` 负责严格解析最小 chat 请求、设置 SSE 头、在同一请求 Context 上转发，以及把稳定错误写成 SSE 帧。
- 两个 CLI 通过 HTTP 调用网关，不直接导入其 internal 包，从运行路径证明客户端复用边界。

## 数据流

`summary-cli` / `sql-diagnose-cli` → 本机 `/v1/chat/completions` → Router → mock Provider stream → SSE → CLI。

请求取消沿 HTTP Context → Router → Provider stream 传播；无 token、租户或持久化状态，因而本阶段没有账单语义。

## 任务顺序

1. 建 Go module、契约、循环 mock 和仅回环 API/health 最小可运行入口。
2. 加 Router 的首块前 fallback、首块后中断，以及网关 SSE 转发和取消测试。
3. 加两个 HTTP CLI、示例命令与 README，实际跑通本机 mock 链路。
4. 全量验证、复盘、提交并 fast-forward `master`。

## 风险与降级

- mock 只验证网关语义，不声称 Ark/Ollama 已接入；真实 Provider 留独立 L2 阶段。
- Context 取消测试以 Provider 的取消信号和测试前后稳定 goroutine 行为为证据，不以一次人工观察替代测试。
- 若 SSE 客户端断连难以由纯 HTTP 路径稳定复现，保留 `httptest` 下的 Context 取消测试并在 README 如实说明范围。

## 章程

暂未建立；本 feature 不以章程为阻塞条件。
