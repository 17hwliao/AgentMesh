---
level: L2
feature: 012-token-bucket-rate-limit
created: 2026-08-30
---

# 每租户内存令牌桶限流

## 来源

补齐 V0 路线图中尚未实现的网关过载保护；不依赖仍受外部环境阻塞的真实存储验证。

## 目标

- 为已认证租户的 `POST /v1/chat/completions` 提供进程内、每租户独立的令牌桶限流。
- 使用显式环境变量配置全局每分钟补充速率与桶容量；未配置时保持既有行为不变。
- 在认证成功后、读取 JSON body 与路由 Provider/Reservation 前拒绝超限请求。
- 返回稳定的 HTTP 429 `rate_limited` 码及可供重试的 `Retry-After`；保留既有 `quota_exhausted` 语义不变。
- 令核心算法可注入时钟并发测试，无 sleep、无真实网络或外部存储依赖。

## 非目标

- 不引入 Redis、跨进程协调、持久化配额策略或管理 API 的限流配置。
- 不改变 Reservation 预扣/结算、Provider fallback、租户 Key 鉴权或既有 HTTP 契约。
- 不计量 token、模型用量或按模型单独限流；每个已认证聊天请求只消耗一个本地令牌。
- 不操作 Docker、真实 MySQL/Redis，也不触碰 010 的阻塞态规格。

## 默认假设

- 同时缺少 `AGENTMESH_RATE_LIMIT_PER_MINUTE` 与 `AGENTMESH_RATE_LIMIT_BURST` 时，限流明确关闭；只设置其一或非正整数均在启动前以稳定配置错误拒绝。
- 每个新租户桶初始装满，按连续时间补充，拒绝响应的 `Retry-After` 向上取整到下一枚令牌可用的秒数。
- 认证失败的请求绝不创建或消耗桶；health 与管理 API 不经过此门禁。
- 内存状态仅覆盖当前进程；README 必须明确它不是 Redis 或分布式限流承诺。

## 验收标准

1. 同一租户的 burst、耗尽、注入时钟后的精确补充均可离线验证；不同租户互不影响，`-race` 下无数据竞争。
2. 错误 Key 携带非法 JSON 时仍先返回 `auth_failed`，且限流器未被调用；认证成功但超限时返回 429 `rate_limited`，不读取 body、不解析模型、不触发 Provider 或 Reservation attempt。
3. 限流关闭时既有 mock/真实 Provider 选择、fallback、`quota_exhausted` 与流式语义保持不变；启用配置的缺失或非法组合在连接/Provider attempt 前失败。
4. README 记录配置、局部内存边界、错误与重试语义；全量 format/build/vet/test/diff 与 Adaptive Spec 校验通过。
