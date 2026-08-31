# 019 HTTP 契约增量：持久身份空库引导

本增量更新 003 的 HTTP 契约启动可用性，不改变既有端点、认证 header、错误码或 JSON 形状。

| 持久身份库启动状态 | 服务行为 | tenant 业务端点 | 管理端点 |
| --- | --- | --- | --- |
| 三表均无记录 | 服务启动为 bootstrap-only | 无有效 Key，认证阶段固定 401 `auth_failed`；不读 body、不路由、不限流、不调配额或 Provider | 已有 admin token 验证后可创建第一 tenant/Key |
| 已有完整且合法 routes | 正常启动 | 保持既有 tenant 认证、路由和流式契约 | 保持既有管理契约 |
| 任一表部分数据、无效/缺失 route、读取失败 | 启动拒绝 | 无 HTTP 服务 | 无 HTTP 服务 |

bootstrap-only 不会生成 Key、route 或认证缓存。创建首个合法 tenant/Key 后，下一请求直接读取持久状态；撤销后的下一请求仍为 401 `auth_failed`。启动检查使用有界 Context，业务读取使用各自 HTTP request Context；取消和超时不得转化为认证成功或 Provider attempt。
