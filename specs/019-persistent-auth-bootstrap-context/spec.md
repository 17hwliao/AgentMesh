---
level: L3
feature: 019-persistent-auth-bootstrap-context
created: 2026-08-31
---

# 持久认证空库引导与请求 Context 贯穿

**原始需求：** 修复持久 MySQL 身份库首次部署时的启动死锁，并让 tenant 认证与路由读取服从 HTTP 请求的取消和超时。

## 目标

- 在已成功打开持久 MySQL 身份库、且 `tenants`、`tenant_model_routes`、`api_keys` 三表均无记录时，服务可启动并只开放既有本地管理面，用于创建第一条合法 tenant route 和 API Key。
- 空库引导态中，所有 tenant 业务端点仍因不存在有效 Key 而在 body、路由、限流、配额和 Provider attempt 前以既有 `401 auth_failed` fail-closed；不伪造 route、Key 或认证成功。
- 保持已有数据的启动校验：任一身份表有部分/损坏数据、无效 route、查询失败或不完整配置时，不能降级为空库引导态，必须保持原有拒绝语义。
- 将 tenant Store 的认证、单模型 route、可见 route 与启动校验读取改为接收显式 Context；HTTP 请求路径传递 `r.Context()`，启动校验使用有界 Context，MySQL 不再自行创建 `context.Background()`。
- 不缓存认证或 route 结果；Key 撤销后下一请求仍直接读取持久状态并立即 fail-closed。

## 非目标

- 不新增或修改 migration、表、索引、持久数据、管理端点、HTTP 错误码或响应 JSON 形状。
- 不自动 seed tenant/Key、不接受 bootstrap 环境变量覆盖持久身份数据、不执行真实 MySQL/Redis/Provider 验证，不触碰 010 阻塞态。
- 不处理审查中 #3–#5 的 `Cache-Control`、HTTP server timeout 或限流 bucket 回收；它们属于后续 020。

## 默认假设

- “空库”只指三张身份表均为零记录的受控新部署；任一表存在记录但 routes 不完整、禁用或不合法，均视为配置/数据异常而非引导态。
- 持久模式仍须完整提供 `AGENTMESH_AUTH_STORE=mysql`、`AGENTMESH_AUTH_MYSQL_DSN` 与 `AGENTMESH_ADMIN_TOKEN`；缺失配置在连接前受控拒绝。
- 管理 API 继续只允许 `127.0.0.1`，继续先常量时间校验独立 admin token、后读 body；原始 Key 仍只在创建成功响应中出现一次。

## 验收条件

1. 使用离线 SQL 替身模拟三表全空时，持久模式可构造 Resolver 与管理路由；创建首个合法 tenant/Key 后，该 Key 可走既有 tenant route；创建前所有业务请求均为 `401 auth_failed`，且零 body 读取、路由、限流、配额和 Provider attempt。
2. 模拟任一身份表已有记录但 routes 缺失/无效，或任一启动读取失败时，服务不进入引导态、不注册可用业务服务；以既有稳定启动拒绝停止。
3. Store 接口及所有实现均接收调用方 Context；认证、chat route、provider-health 可见 route 的取消/超时会到达底层读取，启动校验使用独立有界 Context；不得新增认证/route 缓存。
4. 撤销后的下一请求仍直接读取 MySQL 状态并返回 `401 auth_failed`；未知 prefix 的固定长度 dummy digest 常量时间比较不回归。
5. README 与既有 HTTP contract 如实写明空库仅开放管理引导、业务仍 fail-closed，且不把离线替身结果写成真实 MySQL 实证。

## 实现范围与验证

- 涉及 `tenant` Store/Resolver/MySQL 读取、auth/gateway 接线、`cmd/api` 启动校验、离线 SQL/HTTP/cancellation 回归测试、README 与既有 contract；L3 专项文档为 `contract.md`，因为已发布启动/可用性行为需要记录。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；不 push、不 tag、不启动 Docker。
