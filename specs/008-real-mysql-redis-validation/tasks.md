# 008 任务清单

- [x] T001：实现验证配置预检、namespace 校验和脱敏 JSON 摘要；覆盖缺配置在创建 MySQL/Redis 客户端前返回 `quota_configuration_missing`、零外部尝试及环境值不回显。
- [x] T002：实现既有 migration 的存在性/形状核验与仅缺表 CREATE 执行；覆盖两表存在、缺表、schema 不符、DDL 失败与禁止 ALTER/DROP 的边界。
- [x] T003：实现复用 Repository/Lua/Reconciler 的 namespace-scoped 场景编排、余额/终态断言和精确清理；覆盖 settle/cancel 重放、两类过期候选及清理失败摘要。
- [x] T004：增加 `real-storage-verify` CLI、PowerShell/Make 入口和 README；先跑无配置受控拒绝，再在环境存在时执行一次真实 MySQL/Redis 验证，记录安全 stdout 摘要与实际清理结果。
- [x] T005：全量格式/build/vet/test/diff、Adaptive 校验、私有阶段复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
