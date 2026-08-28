---
level: L3
feature: 004-observability-trace-records
created: 2026-08-28
---

# D 组：租户收窄的流式 Trace 与内存 Usage Record

## 原始需求

让已认证 tenant 能查询自己一次流式调用的安全摘要：trace ID、Provider attempt 名称/顺序、首块与总耗时、结果/取消原因及 Provider 明示的 usage；同时补齐路线图遗留的 stage-1 demo、私有求职资料与三份公开决策记录。

## 目标

- 在每个已认证 chat 请求开始时生成不可预测 trace ID，并以响应 `X-AgentMesh-Trace-ID` 交给客户端；记录不含 prompt、消息文本、响应 delta、raw key、digest、endpoint 或模型密钥的最小事件。
- 记录 tenant ID、请求 model、attempt 名称与开始/结束、首块延迟、总耗时、稳定结果码、取消状态，以及仅在 Provider chunk 明示时的 usage；usage 缺失必须显式为未观测，不能估算成精确 Token。
- 使用可注入时钟/ID 和固定容量的内存 Recorder；达到容量时按确定规则淘汰最旧的已完成记录，重启即丢失。
- 增加认证后的 tenant-scoped trace 查询端点；其他 tenant、未知 ID 与未完成记录都以同一稳定 404 拒绝，不泄露存在性。
- 完成 `make demo-stage-1`（两个 CLI、首块前 fallback、取消演示），私有 resume/FAQ 和 `decisions/` 三项历史决策记录。

## 非目标

- 不接 OTel、Prometheus、Eino Callback、MySQL、Redis、usage_outbox、账单、Token 本地估算、持久化检索、跨 tenant 管理查询或真实 Provider/Docker。
- 不实现配额/Reservation/reconciler；该一致性专项单独为后续 L3，不能把 trace record 当作 attempt 持久化凭据。

## 默认假设

- trace 查询只返回请求摘要；响应一旦开始 SSE 仍保持既有输出 shape，trace 数据不插入 SSE payload。
- trace ID 只能由服务端生成；客户端 header、query、body 都不能指定或覆盖它。
- Provider health 不等于请求 trace 成功；fallback 保留 001/002 的“首块前可切换、首块后不可切换”语义。
- 私有 resume/FAQ 只写入已忽略的 `求职私有资料/`；没有实测延迟或并发数时不虚构数字。

## 验收条件

- mock 正常、首块前 fallback、首块后中断和取消均产生可测 trace；attempt 顺序、首块/总耗时、稳定结果与 observed usage 语义正确。
- trace HTTP 查询先认证，再按 tenant 收窄；无 key、跨 tenant、未知或未完成 trace 都不泄露记录存在性，且不触发 Provider。
- 任何已落盘/HTTP 输出均不含 prompt、delta、raw key/hash/prefix、endpoint 或 Provider 错误正文；Recorder 满时淘汰策略可测。
- stage-1 demo 实际只使用本机 mock 与一次性 key；README、三份 decisions 和私有 resume/FAQ 只陈述实测能力与未实现边界。
