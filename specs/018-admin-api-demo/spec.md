---
level: L1
feature: 018-admin-api-demo
created: 2026-08-31
---

# 本地管理 API 生命周期演示

## 目标

- 新增 `make demo-admin`，在单一短生命周期的本机进程中演示 admin API 的 tenant/Key 生命周期。
- 用进程内临时 Store/Lifecycle 与回环 HTTP 组合完成：创建 tenant/Key → chat 200 → 撤销 → 同 Key chat 401 `auth_failed` → 重建 Key → chat 200。
- 演示输出只含步骤、HTTP 状态、稳定码和安全 Provider 摘要；原始 Key 只在进程内传递，不打印、落盘或写日志。
- 脚本及 Go 测试验证 admin token 只用于 admin API，tenant Key 只用于 chat，清理后不留下监听进程或临时凭据。

## 非目标

- 不改变 `cmd/api` 的持久 auth mode、默认 bootstrap 行为或 admin 路由注册条件。
- 不连接或声称使用 MySQL、Redis、Ark、Ollama、Docker、持久 migration 或真实 Key 生命周期。
- 不新增 Web UI、管理 API 字段、生产管理权限或持久化 Store。

## 默认假设

- 演示专用的 lifecycle 仅服务该进程，可复用现有 HTTP Handler 契约；进程退出即丢失所有 tenant/Key。
- 服务仅监听回环临时端口；失败时脚本非零退出并在 finally 清理子进程。

## 验收标准

1. `make demo-admin` 实际输出五步状态：create 201、首 chat 200、revoke 204、旧 Key chat 401 `auth_failed`、新 Key chat 200。
2. 离线测试覆盖演示 lifecycle 的创建/撤销与 Key 不泄露；运行脚本前后无遗留 API 进程或临时 Key 文件。
3. README 明确这是内存/回环演示，不是 MySQL 持久化实证；全量 format/build/vet/test/diff 与 Adaptive 校验通过。

## 任务

- [x] T001 实现 demo 专用内存 lifecycle/回环 HTTP 命令与离线测试，不改变生产 API 接线。
- [x] T002 新增 PowerShell 编排、Make target 与 README 边界，实际运行五步演示。
- [x] T003 全量验证、私有复盘、提交并 fast-forward master；不 push、tag 或操作 Docker。
