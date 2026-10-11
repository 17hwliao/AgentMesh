# 2026-10 后端扩展

后续条件扩展已补充，余额计费、缓存、第三Provider和gRPC当前行为以 [backend-roadmap.md](backend-roadmap.md) 为准；下文未实现的描述属于2026-10-08核心检查点。

当前后端使用真实 MySQL、Redis、Kafka 持久化，具备 Ark/Ollama SSE、鉴权、租户路由、预算 reservation、恢复、embeddings、独立管理接口。基线契约见 backend-release.md；新一轮修改均本地保留。

额度结算/取消检查钱包与原 reservation marker 存在，钱包丢失时不能通过退款新造余额。充值在 MySQL 锁住租户并验证全部持久 grants 总额不超过 2^52，覆盖并发和 Redis 步骤尚未完成的 grants；操作仍幂等。钱包丢失依照既有离线 rebuild 流程恢复，不能重新发 initial。

Prometheus 与 OTel 实际接入 cmd/api。`/metrics` 包括有限 endpoint/method/status 标签的计数、SSE 总耗时、并发请求、活跃 reservations、usage outbox；数据库查询失败 gauge=-1。有限异步 OTLP exporter 不阻塞业务线程，traceparent 从 SQL Sentinel/Eino 经 HTTP 客户端传到网关/provider。业务内存 trace 与预算账本仍独立，未知 usage 保守扣预留，不是精确 token 财务账单。生产 HTTPS 代理屏蔽公网 metrics/admin；通过 SSH 隧道私有访问。

CLI SSE 截断且没有 DONE 时返回错误，避免把部分文本当作成功。Embedding 边界拒绝 NaN/Infinity，provider 输入/输出受既有大小和维度限制。默认仍只支持 stream=true，语义缓存、第三 provider、完整支付、gRPC、Kubernetes 是原文明确列出的条件后置项，未凭空创建外部账户或引入不同部署目标。

联合部署加入 SQL Sentinel 的 scoped JWT/Casbin、报告哈希审批、通用影子表分析/实测、持久 RAG，以及 Prometheus/OTel 采集，见另一项目 docs/backend-expansion.md 和 deploy/README.md。实际云域名/真实模型/GitHub 必须在目标环境做最后联调；本机协议 fixture 不能代替真实供应商验收。
