# 决策 001：Provider Adapter 与 Router 的边界

**状态：** 已采用（001–002）

Provider Adapter 负责各上游协议、Context 传递、健康检查和安全错误归一化；Router 只接收统一的流式契约和有序、已经构造的 Provider。tenant route 只能声明已有 Adapter 的逻辑名称，不能创建或混排 mock/真实 Provider。

首个 SSE chunk 前允许尝试下一个 Adapter；首个 chunk 后失败统一为 `stream_interrupted`，不再切换。这样客户端不会收到来自不同模型的拼接输出，也不会把 Adapter 细节泄露给业务 CLI。
