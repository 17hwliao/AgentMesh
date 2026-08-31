# 019 计划：空库安全引导与 Context 读取边界

## 启动状态与 fail-closed 规则

为 Store 增加显式、带 Context 的启动检查能力，结果只能是：有效的已声明 routes、三张身份表均为零记录的 pristine identity store，或错误。MySQL 以独立计数/读取查询区分三表全空、部分数据、route 无效和查询失败；不得把空结果或错误折叠为同一状态。

`tenant.NewResolver` 接收该启动检查的有界 Context。正常已声明 routes 继续逐条验证为当前 runtime provider selection 的合法有序子集；只有 pristine 状态允许构造 bootstrap-only Resolver。部分数据、无 route、无效 route、未知 provider 或数据库错误仍返回既有 `tenant_route_configuration_invalid`，主程序不启动。

bootstrap-only Resolver 不生成 route、不保存认证结果，也不绕过 Store。空库中没有可认证 Key，业务端点会在现有 auth middleware 处返回 `401 auth_failed`；管理面仍由已有独立 admin token 路由提供。管理 API 创建首个 tenant/Key 后，后续请求仍直接读 MySQL，因此无需重启即可使用新 route。

## Context 数据流

`tenant.Store` 的 Authenticate、Route 与 tenant-local route visibility 改为接收 `context.Context`；启动检查接口也接收调用者给出的有界 Context。Memory、MySQL、admin demo 与测试替身同步实现该接口，不新增缓存或后台 goroutine。

`auth.Authenticate` 将 `r.Context()` 交给 Authenticate；Gateway 将 request Context 交给 Resolver 的 chat route 与 provider-health visibility；Resolver 原样传给 Store。`cmd/api` 仅为启动检查创建短时 Context，MySQL Store 删除全部内部 `context.Background()`。Context 被取消或超时时读取失败并按既有业务拒绝路径处理，绝不改写为认证成功或 fallback。

## 接线、测试与文档

主程序保持当前配置、回环监听和 admin 注册条件，只把启动 Resolver 校验改为显式有界调用。无需新增 migration：003 的三表结构不变，也不自动执行 DDL。

离线 SQL 替身覆盖 pristine、任一表有残留、非法 route 与查询失败，证明只有严格全空可引导。HTTP 集成测试覆盖空库下 admin 创建第一 tenant/Key 前后：创建前业务请求 401 且不读 body/调用下游，创建后有效 Key 走既有 mock SSE；另以捕获 Context 的替身验证 auth、chat route 和 provider-health 都收到请求取消。保留未知 prefix dummy digest 和撤销即时生效回归测试。

更新本阶段 `contract.md`，并在既有 003 HTTP contract 与 README 写入这一启动可用性事实：它不改变端点、响应 shape 或真实 MySQL 实证状态。章程：暂未建立。

## 批次与验证

- T001：重塑 Context Store/启动状态接口，实现 MySQL 与替身严格空库判定；跑 tenant/auth 相关测试。
- T002：接线 Resolver、Gateway、`cmd/api` 的有界启动 Context；跑 gateway/tenant HTTP 取消和 fail-closed 测试。
- T003：补空库 admin 引导端到端回归，以及 README/contract 更新；跑 `go test -count=1 ./...`。
- T004：执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`、Adaptive 校验、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
