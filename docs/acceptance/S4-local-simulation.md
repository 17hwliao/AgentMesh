# S4 本地边界模拟验收

**日期：** 2026-11-16
**分支：** `feat/standalone-control-plane`
**基线提交：** `6f9ab49`
**验收级别：** Windows 本机自动测试；使用 loopback listener、`httptest.NewTLSServer` 和 `mockopenai` 上游。不是公网部署、真实域名、真实反向代理、MySQL/Redis、多副本或生产级审计验收。

## 已实现能力

- 数据面只注册健康检查和 `/v1/*` 路由；测试访问数据面的 `/admin/state` 返回 404，管理 API 由独立 `AdminHandler` 提供。
- `healthz` 返回 200；在已配置并启用模型后，`readyz` 返回 200。
- 测试用 `httptest.NewTLSServer` 包装数据面，客户端通过 TLS 完成 Chat，验证的是测试内 TLS 边界，不是生产 TLS 监听器。
- IP 限流使用独立请求窗口。配置 `IPRequestsPerMinute=1` 时，同一 loopback IP 第一次 Chat 返回 200，第二次返回 429；实现使用 `RemoteAddr`，并设置 `Retry-After: 60`。
- 并发上限配置 `MaxConcurrent=1`，上游延迟 200ms；两个并发请求中得到一个 200 和一个 429。实现使用进程内活动计数，并设置 `Retry-After: 1`。
- 请求体上限配置 `MaxBodyBytes=32`；测试请求正文超过 32 字节时返回 413。实现先检查 `ContentLength`，再用 `MaxBytesReader` 限制实际读取。
- 全局日 token 上限配置 `DailyTokenLimit=1`，同一测试中的第一次 Chat 返回 200，第二次返回 429。该上限按当天所有 Key 的已记录 token 总和判断，不是货币金额或账单计费。
- 数据面成功请求写入 `audit_log`；管理端 `GET /admin/audit` 返回事件。测试找到 `action=data_request`、目标模型和 `outcome=success` 的事件，并确认详情含 `input_tokens=`、不含用户正文 `hello`。
- 审计查询默认 100 条、最多 500 条，按 ID 倒序返回；当前代码只在数据请求完成路径写入 `data_request` 审计事件。
- 备份使用 SQLite `VACUUM INTO` 生成 `catalog.db`，并复制 `master.key`。测试完成备份、关闭源 App、把备份恢复到新目录、启动新 App，旧的 local Key 仍可调用原模型。
- `cmd/api` 的 standalone 入口提供 `--backup-dir`、`--restore-dir`、`--max-concurrent`、`--ip-rpm`、`--max-body-bytes` 和 `--daily-token-limit` 参数；参数为 0 表示不启用对应上限。

## 明确未实现或未验证

- 没有真实域名、公网 DNS、Caddy/Nginx 反代、HTTPS 证书部署或 SSE 经公网代理不被缓冲的验证。
- `App.Start` 本身仍使用普通 HTTP listener；本组 TLS 仅由 `httptest.NewTLSServer` 提供，管理面仍是 loopback HTTP。不能写成已具备生产 TLS 入口。
- 没有可信代理支持或 `X-Forwarded-For` 解析；IP 限流只看 `RemoteAddr`，经过反代时所有请求可能被视为同一来源。
- 没有 MySQL/Redis、多副本、共享限流、分布式 token 预算或高可用验证；SQLite 仍是单机存储。
- 日 token 上限是全局、进程内准入检查，不是 `Reserve → Settle` 的原子预算，也没有货币计费。并发请求可能同时通过准入，之后才记录 token；测试没有验证并发下的硬上限。
- 审计只覆盖数据请求，不覆盖管理操作；没有日志不可篡改、集中收集、保留周期、告警或外部审计存储。审计写入错误在代码中被忽略，本轮没有测试审计落盘失败。
- 备份恢复只验证“干净备份”的成功路径；没有校验文件完整性、加密、对象存储、定时备份、异构版本或恢复中断后的原子回滚。`RestoreDataDir` 会覆盖目标目录中的同名 `catalog.db` 和 `master.key`。
- 没有真实 `cmd/api --standalone` 进程启动、浏览器管理、Docker/NAS、真实 Provider、公网流量或生产级压测。
- 数据面 `/admin/state` 的 404 不能替代对整个管理面公网暴露面的独立安全评估；本组没有执行渗透或边界扫描。

## 真实命令与结果

工作目录：`C:\Users\17hwliao\Documents\ChatGPT\实习\work\AgentMesh-control-plane`

```powershell
$env:GOMEMLIMIT='3GiB'
go test -count=1 -v ./internal/standalone -run '^TestS4'
go vet ./internal/standalone
git diff --check
```

实际结果：

```text
=== RUN   TestS4LocalTLSAdminIsolationIPLimitAndAudit
--- PASS: TestS4LocalTLSAdminIsolationIPLimitAndAudit (0.06s)
=== RUN   TestS4ConcurrencyLimit
--- PASS: TestS4ConcurrencyLimit (0.24s)
=== RUN   TestS4BodyAndDailyTokenLimits
--- PASS: TestS4BodyAndDailyTokenLimits (0.05s)
=== RUN   TestS4BackupAndRestore
--- PASS: TestS4BackupAndRestore (0.05s)
PASS
ok  	agentmesh/internal/standalone	0.529s
```

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 -v ./internal/standalone -run '^TestS4'` | 退出码 0，四个测试均 PASS |
| `go vet ./internal/standalone` | 无输出，退出码 0 |
| `git diff --check` | 退出码 0；仅出现当前工作区既有 tracked 文件的 LF 将转为 CRLF 警告，无 whitespace error |

## 错误路径与回滚证据

- 数据面 `/admin/state` 返回 404，证明测试中的管理路由没有挂在数据 mux 上。
- 超过 IP 限额返回 429；超过并发上限返回 429；请求体超限返回 413；全局日 token 超限返回 429。
- 审计详情只包含 Provider 与 token 计数，不包含测试用户正文 `hello`；这是脱敏边界的自动测试证据。
- 备份恢复测试在关闭源 App 后恢复到新目录，并以旧 local Key 完成 Chat，证明确认的目录级备份可被新 App 使用。
- 回滚/恢复不是事务性多文件切换：重复启动、损坏文件、缺失 `master.key` 或恢复中途失败未演练；操作上应停止服务、准备一致的清理目标目录，再用 `--restore-dir` 启动并重新验证。

## 环境限制

- 所有实际执行的命令均在 Windows PowerShell 7.6.9 中完成。
- Go 版本为 `go1.26.11 windows/amd64`，`GOMEMLIMIT` 设为 `3GiB`。
- 自动测试使用 loopback、本地 TLS 包装和 mock 上游，不能外推到真实域名、公网流量、真实反代、生产存储或真实计费。
- 本文记录的是 S4 的本地边界模拟验收，不表示已完成公网发布。
