# 实施计划

## 实现决策

- 新增 `internal/tenant` 的 Tenant、APIKeyRecord、Store 接口及锁保护的内存实现；bootstrap raw key 只用于生成记录，record/测试断言只读取 prefix/hash。
- 新增 `internal/auth`：严格 bootstrap 配置、Bearer 解析、SHA-256、`subtle.ConstantTimeCompare`、tenant Context 和稳定错误；未知 prefix 也比较固定长度摘要。
- Gateway 在 chat/provider-health 前调用 auth；chat 认证后才解码请求，再按 tenant/model 取得 Provider 顺序。认证拒绝不读 JSON body；model/quota 拒绝在解码后、任何 Provider attempt 前发生。`/healthz` 维持公开。
- Store 只声明 tenant/model 的 Provider 名称；`cmd/api` 仍用 002 的 `runtime.Build` 构造全局实际 Adapter，tenant route 必须是该全局集合的有序子集，mock 与真实 Provider 混排在启动前拒绝。
- 预留固定 allow 的 `QuotaGate` 接口；使用 mock/`httptest` 验证拒绝路径零 Provider 调用，不使用真实 Provider、Docker、MySQL 或 Redis。
- 对公开 HTTP 行为新增 `contract.md`；本阶段没有迁移或不可逆架构决定，因此不创建 migration/ADR。

## 数据流

`bootstrap raw key → prefix/hash APIKeyRecord`；`CLI raw key → Bearer → auth → tenant Context → model route → quota gate → Router/Provider`。

tenant/model/provider route 仅从 Store 返回；请求 model 是选择键，不是权限声明。认证拒绝发生在 JSON decoder 前；model/quota 拒绝发生在解码后、任何 Provider attempt 前。

## 任务顺序

1. 实现 tenant/API key 内存 Store、bootstrap 与 auth；覆盖 hash+prefix、常量时间比较、禁用/畸形 key 和不回显。
2. 接 Gateway/CLI/model 路由/allow-only QuotaGate；覆盖 body 前拒绝、跨 tenant 路由隔离、health 可见性、零 Provider 调用及 contract。
3. 进程内生成一次性 key，运行本机 mock API 和两个 CLI；复核无 key/错误 model/预留 quota 拒绝，更新 README 和真实记录。
4. 全量验证、私有复盘、提交并 fast-forward `master`；不 push/tag。

## 风险与降级

- 内存 Store 重启后丢失，不能扩展到多进程；迁移 MySQL 时须另开 L3 + migration-plan，不能静默替换。
- API Key 没有重放防护；服务仍只监听 `127.0.0.1`，远程暴露、TLS 和管理面留后续设计。
- QuotaGate 默认 allow，`quota_exhausted` 不会被 emit；Redis Lua/Reservation 的原子扣减留给阶段 4 专项。

## 章程

暂未建立；本 feature 不以章程为阻塞条件。
