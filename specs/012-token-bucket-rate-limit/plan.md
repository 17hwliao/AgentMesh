# 012 计划：每租户内存令牌桶限流

## 设计

新增 `internal/ratelimit`：`Config` 从 `AGENTMESH_RATE_LIMIT_PER_MINUTE` 与 `AGENTMESH_RATE_LIMIT_BURST` 成对读取；两者均为空时返回无类型 `nil` gate，任一缺失或非正整数返回稳定配置码。`Gate` 以 mutex 保护按 tenant ID 索引的桶；新桶装满，每次允许请求消耗一枚令牌，按可注入的 `func() time.Time` 连续补充并封顶至 burst。拒绝决策带下一枚令牌所需的 duration，Gateway 统一向上取整为 `Retry-After` 秒。

Gateway 新增仅含 `Admit(context.Context, tenantID)` 的 RateGate 边界及启动期 setter，默认 allow。`AuthenticatedHandler` 将 chat handler 包在 `auth.Authenticate` 之后的 rate middleware：它只处理 POST chat，先从认证 context 取 tenant，再决定允许或写 429 `rate_limited`；拒绝时绝不调用下游 `handleChat`，因此不读取 body、不创建 trace、不做模型路由、QuotaGate、Reservation 或 Provider attempt。health、provider health、trace 和 admin 路由不经过此 middleware。既有 `QuotaGate` 仍在 model 解码与路由后运行，维持 `quota_exhausted` 含义。

`cmd/api` 在构造 runtime/Provider 前打开 rate gate；配置错误只记录稳定码并退出。成功配置后把 gate 注入 Server。无配置时不注入，保证 001–014 的默认启动与 fallback 不变。

## 测试与验证

- T001：对 Gate 验证 full burst、耗尽、向前补充、不同 tenant 隔离、时钟回拨不生成令牌及并发安全；配置测试覆盖关闭、单变量和非法值，不触及网络。
- T002：HTTP 测试验证认证失败携带非法 JSON 时不调用 rate gate；同 tenant 第二次请求在 body 解码、Resolver、Quota、Reservation、Provider 前得到 `rate_limited` 与正确 `Retry-After`；不同 tenant 不互相消耗。
- T003：验证 api 配置错误在 Provider 构造前退出、关闭时现有流式 fallback 不变；README 只说明局部内存语义和重试行为。
- T004：`gofmt -w` 受影响 Go 文件、`go build ./...`、`go vet ./...`、`go test ./...`、`git diff --check` 与 Adaptive Spec；写私有复盘、提交并 fast-forward master，不 push/tag/Docker。

章程：暂未建立；本计划不阻塞执行。
