# S1b 设备 Key 与基础限额验收

**日期：** 2026-11-16
**分支：** `feat/standalone-control-plane`
**基线提交：** `6f9ab49`
**验收级别：** Windows 本机自动测试；使用 `net/http/httptest` 的进程内 loopback 服务和 `mockopenai` 上游。不是真实设备、真实 Provider、浏览器 UI 或公网验收。

## 已实现能力

- 管理 API 支持 `POST /admin/client-keys` 创建具名 `app_key`，并支持列表、`PATCH`、`DELETE` 和 `POST /admin/client-keys/{id}/rotate`。
- 每个 Key 有独立 ID、名称、前缀和哈希；明文只在创建或轮换响应中返回一次，`GET /admin/client-keys` 不返回明文。
- 两个 Key 可以分别鉴权数据面请求。测试中 device A 和 device B 各完成一次 Chat，上游共收到 2 次请求，两个 Key 均保持启用。
- `last_used_at` 在 Key 通过数据面鉴权后更新。两个 Key 使用后均由管理列表返回非空时间，且不早于请求开始时间。
- `DELETE` 采用软撤销：Key 变为 `enabled=false`，历史 `last_used_at` 保留，记录不从目录删除。
- 撤销 device B 后，device B 的 Chat 返回 401；device A 的新 Key 仍可完成 Chat，证明撤销隔离。
- 轮换会创建全新 Key ID 和明文，旧 Key 在同一事务中被禁用。旧明文立即返回 401；新明文可用，且新 Key 初始 `last_used_at` 为空，首次使用后再更新。
- 轮换 device A 不影响 device B；device B 在轮换后仍可请求。
- Key 元数据支持 `rpm`、`daily_requests`、`daily_tokens`、`model_allowlist` 和 `expires_at`。测试验证创建、列表和 PATCH 后均能读回这些字段。
- 测试把 `daily_requests` 从 5 更新为 1；同一 Key 第一次 Chat 返回 200，第二次返回 429，错误体包含 `quota_exhausted`。
- 模型白名单字段可读回并更新为 `s1b/*`；本轮 S1b 测试没有单独构造白名单拒绝场景。

## 明确未实现或未单独验证

- `expires_at` 的到期拒绝没有单独验证。实现中存在认证时检查过期时间的代码路径，但本组测试只验证字段创建、列表和更新，不把过期拒绝写成已验收。
- `daily_tokens` 和 `rpm` 的拒绝行为没有在 S1b 测试中直接触发；本轮只直接证明 `daily_requests` 限制。`rpm` 的独立验证属于 S2 验收范围。
- 没有验证撤销后的 Key 重新启用、恢复旧 Key 或回滚轮换；测试只证明旧 Key 保持禁用。
- 没有执行重启后 `last_used_at`、Key 状态和限额字段的持久化回归；测试在同一个 App 生命周期内读取目录。
- 没有浏览器级管理页验收、真实设备接入、真实 Provider、局域网、TLS、公网、MySQL/Redis 或多副本验收。
- 没有并发请求下的日请求量原子性测试；当前证明是顺序请求下第二次被拒绝。

## 真实命令与结果

工作目录：`C:\Users\17hwliao\Documents\ChatGPT\实习\work\AgentMesh-control-plane`

```powershell
$env:GOMEMLIMIT='3GiB'
go test -count=1 -v ./internal/standalone -run '^TestS1B'
go vet ./internal/standalone
git diff --check
```

实际结果：

```text
=== RUN   TestS1BAppKeyIsolationRevocationAndRotation
--- PASS: TestS1BAppKeyIsolationRevocationAndRotation (0.04s)
=== RUN   TestS1BClientKeyMetadataAndDailyRequestLimit
--- PASS: TestS1BClientKeyMetadataAndDailyRequestLimit (0.02s)
PASS
ok  	agentmesh/internal/standalone	0.222s
```

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 -v ./internal/standalone -run '^TestS1B'` | 退出码 0，两个测试均 PASS |
| `go vet ./internal/standalone` | 无输出，退出码 0 |
| `git diff --check` | 退出码 0；仅出现当前工作区既有 tracked 文件的 LF 将转为 CRLF 警告，无 whitespace error |

## 错误路径与回滚证据

- 轮换后的旧 device A 明文请求返回 401；新 device A 明文和 device B 明文继续成功。
- 撤销 device B 后，其明文请求返回 401；列表显示 device B 为禁用状态，`last_used_at` 仍在，device A 的新 Key 仍启用。
- `daily_requests=1` 的第二次请求返回 429 `quota_exhausted`，证明限额拒绝路径实际生效。
- 本组测试没有故意制造 SQLite 写失败，因此不能把事务回滚写成已验证。操作层面的回滚需要恢复 `catalog.db` 与 `master.key` 的一致备份；S1b 本轮没有执行该恢复演练。

## 环境限制

- 所有实际执行的命令均在 Windows PowerShell 7.6.9 中完成。
- Go 版本为 `go1.26.11 windows/amd64`，`GOMEMLIMIT` 设为 `3GiB`。
- 自动测试使用进程内 loopback 和 mock 上游，不覆盖真实网络、真实设备或生产流量。
- `git diff --check` 不检查未跟踪文件；本次新增验收文档另用 PowerShell 检查尾随空白，结果见最终汇总。
