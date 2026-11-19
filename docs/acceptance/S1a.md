# S1a 单机目录与闭环验收

**日期：** 2026-11-16
**分支：** `feat/standalone-control-plane`
**基线提交：** `6f9ab49`
**验收级别：** Windows 本机自动测试；使用 `net/http/httptest` 的进程内 loopback 服务和 `mockopenai` 上游。不是真实 Provider、浏览器 UI 或公网验收。

## 已实现能力

- standalone 本地模式使用数据目录中的 SQLite catalog 和 `master.key`，提供 SQLite 目录、凭据加密、不可变 Snapshot 和本机 `local` Key。
- 管理端通过一次性 admin token 建立 session，并使用 CSRF 保护修改请求；管理静态页面已嵌入二进制。
- 管理 API 支持创建 Provider、测试 `/models`、发现模型、列出模型和启用模型。发现的新模型默认禁用。
- 数据面提供 `/healthz`、`/readyz`、`/v1/models`、`/v1/chat/completions`，使用本机 Key 鉴权；Chat 可返回完整 SSE 和 `[DONE]`。
- 空 catalog 时 `/readyz` 不返回 ready；发现并启用模型后可转为 ready。
- Provider 凭据不会以明文出现在 `catalog.db`，也不会通过管理端 Provider 列表返回。
- 重启后同一数据目录中的 Provider、启用模型和本机 Key 仍可继续使用。
- 坏配置可进入修复模式：管理端可登录并显示 `snapshot_invalid`，数据面 `/readyz` 返回 503；修正已持久化的 Provider URL 后可清除修复状态。

## 未实现或未验收

- S1a 不包含路由组、跨 Provider fallback、每 Key 限额、模型白名单、CIDR、上下文窗口过滤、`/v1/responses` 或 Codex 兼容。
- S1b 的设备 Key 与状态能力不在本文验收范围；其验收记录待补。
- 没有执行真实 Provider 联调。自动测试只使用 `mockopenai`。
- 没有执行浏览器级管理页全流程验收；测试覆盖的是管理 HTTP API、session/CSRF 和嵌入资源，不能替代 UI 全量验收。
- 没有执行 `cmd/api --standalone` 真实进程启动和外部 `curl` 验收；测试直接构造 App，并由 `httptest` 创建 loopback listener。
- 未覆盖局域网监听、TLS、公网、可信代理、MySQL/Redis 双 Store、多副本和完整 Codex 接入。

## 运行命令与结果

工作目录：`C:\Users\17hwliao\Documents\ChatGPT\实习\work\AgentMesh-control-plane`

```powershell
$env:GOMEMLIMIT='3GiB'
go test -count=1 ./internal/standalone -run 'TestS1A'
go test -count=1 ./internal/standalone -run 'TestS2'
go test -count=1 ./internal/standalone -run 'TestS25'
go vet ./internal/standalone
git diff --check
```

实际结果：

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 ./internal/standalone -run 'TestS1A'` | `ok agentmesh/internal/standalone 0.292s`，退出码 0 |
| `go test -count=1 ./internal/standalone -run 'TestS2'` | `ok agentmesh/internal/standalone 0.395s`，退出码 0 |
| `go test -count=1 ./internal/standalone -run 'TestS25'` | `ok agentmesh/internal/standalone 0.257s`，退出码 0 |
| `go vet ./internal/standalone` | 无输出，退出码 0 |
| `git diff --check` | 退出码 0；仅出现当前工作区既有文件的 LF 将转为 CRLF 警告，无 whitespace error |

`TestS1A` 实际执行了 `TestS1AClosedLoopAndRestart` 和 `TestS1ARepairModeAfterPersistedBadConfig`。

## 重启与错误路径证据

- `TestS1AClosedLoopAndRestart` 用同一数据目录和同一 `local` Key 重启后再次调用 Chat，返回 `hello from mock`，证明 catalog、启用状态和 Key 哈希可跨重启使用。
- 同一测试尝试把 Provider URL 改为云元数据地址，PATCH 返回 422；前后 `/admin/state` 字节一致，旧 Snapshot 仍能完成 SSE 请求。
- `TestS1ARepairModeAfterPersistedBadConfig` 直接在 SQLite 中写入 `ftp://bad` 后重启，管理端出现 `"repair":"snapshot_invalid"`，数据面 `/readyz` 为 503。
- 修复已持久化的 URL 后，`repair` 标记被清除。该 fixture 没有启用模型，因此测试没有把 `/readyz` 200 当作修复成功的唯一断言。
- 测试扫描 `catalog.db` 和管理端 Provider 列表，未发现明文 `upstream-secret`。

## 环境限制

- WSL bash 不可用；所有实际执行的命令均在 Windows PowerShell 中完成。
- `GOMEMLIMIT` 设为 `3GiB`。
- 自动测试使用进程内 loopback 和 mock 上游，不能外推到真实 Provider、公网、局域网、TLS 或并发生产流量。
- `git diff --check` 不检查未跟踪文件；三份新增验收文档另用 PowerShell 检查尾随空白，结果均为 0。
- 本阶段只运行了指定测试、`go vet ./internal/standalone` 和 `git diff --check`；未运行全仓测试、race、浏览器验收或真实进程重启演示。
