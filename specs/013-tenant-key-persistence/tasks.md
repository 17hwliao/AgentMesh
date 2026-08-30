# 013 任务：持久租户与 API Key 生命周期

- [x] T001 新增 `003_tenant_api_keys.sql` 与持久 tenant/key/route 数据类型；以离线 SQL 夹具验证 schema、外键、唯一约束和无原始凭据。
- [x] T002 实现 MySQL TenantStore 与显式 opt-in 配置选择；测试重启加载、dummy digest 常量时间入口、禁用/撤销拒绝、路由隔离及缺配置零连接。
- [x] T003 实现 lifecycle repository 与回环管理 middleware；测试 admin token 在业务 body 解码前拒绝、创建 Key 单次返回和创建 tenant/Key 的持久化语义。
- [x] T004 接线管理路由和 `cmd/api` 双模式；测试按 key ID 幂等撤销、撤销后零 Provider 调用、既有 Bootstrap/fallback 不回归。
- [x] T005 更新 README，记录 MySQL mode、手动 migration、回环/admin token 边界、一次性原始 Key 语义及真实环境未验证事实。
- [x] T006 执行全量验证、Adaptive 校验、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
