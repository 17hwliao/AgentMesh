# 019 任务：空库安全引导与请求 Context

- [x] T001 将 `tenant.Store`、route visibility 与启动检查改为显式 Context；实现 MySQL 严格三态判定（pristine / declared routes / error），并以 SQL 替身覆盖全空、部分数据、无效 route 与真实查询 error。
- [x] T002 将 Resolver、auth middleware、Gateway chat/provider-health 与所有实现接线到调用方 Context；`cmd/api` 用短时启动 Context；覆盖取消/超时传递、未知 prefix dummy digest 与撤销即时生效，且不引入缓存。
- [x] T003 建立空库持久管理引导的离线 HTTP 回归：创建前业务 401 且零副作用，admin 创建首个 tenant/Key 后可流式调用；同步 README、003 HTTP contract 与本阶段 contract，保持真实环境未验证事实。
- [x] T004 全量格式/build/vet/test/diff、Adaptive 校验、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
