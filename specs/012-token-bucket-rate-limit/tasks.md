# 012 任务：每租户内存令牌桶限流

- [x] T001 新增 `internal/ratelimit` 的成对配置、可注入时钟的并发安全令牌桶及离线单元测试。
- [x] T002 在认证后、body 解码前接入 Gateway RateGate；增加拒绝顺序、`Retry-After`、tenant 隔离及零下游调用测试。
- [x] T003 在 `cmd/api` 启动期接线并更新 README；验证非法配置早于 Provider 构造、关闭时流式 fallback 不变。
- [x] T004 全量验证、Adaptive Spec、私有复盘、提交并 fast-forward master；不 push、tag 或操作 Docker。
