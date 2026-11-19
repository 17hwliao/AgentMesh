# 阶段复验入口

本目录的阶段记录只描述对应检查在当时实际覆盖的范围。所有命令均在
`AgentMesh-control-plane` 工作树执行，默认使用 Windows PowerShell 和
`GOMEMLIMIT=3GiB`。

| 阶段 | 证据 | 复验命令 |
| --- | --- | --- |
| S0 | [S0.md](S0.md) | `go test -count=1 ./internal/...` |
| S1a | [S1a.md](S1a.md) | `go test -count=1 ./internal/standalone -run '^TestS1A'` |
| S1b | [S1b.md](S1b.md) | `go test -count=1 ./internal/standalone -run '^TestS1B'` |
| S2 | [S2.md](S2.md) | `go test -count=1 ./internal/standalone -run '^TestS2'` |
| S2.5 | [S2.5.md](S2.5.md) | `go test -count=1 ./internal/standalone -run '^TestS25'` |
| S3 | [S3.md](S3.md) | `go test -count=1 ./internal/standalone -run '^TestS3'` |
| S3.5a | [S3.5a.md](S3.5a.md) | `go test -count=1 ./internal/hostscan ./internal/localmodels ./internal/catalog ./internal/standalone` |
| S3.5b 配置片段 | [S3.5b 配置片段](S3.5b-config-snippet.md) | `python scripts/test-client-config.py` |
| S3.5c 占位符 | [S3.5c 占位符](S3.5c-placeholders.md) | `go test -count=1 ./internal/privacy` |
| S3.5d 授权核对 | [S3.5d 调研](S3.5d-authorization.md) | 官方文档核对 |
| S3.5e 地址校验 | [S3.5e 地址校验](S3.5e-address-validation.md) | `go test -count=1 ./internal/standalone -run '^TestValidateLANAddress$'` |
| S3.6 目录锁 | [S3.6 目录锁](S3.6-directory-lock.md) | `go test -count=1 ./internal/standalone -run '^TestDataDirLockAcrossProcesses$'` |
| S4（本地模拟） | [S4-local-simulation.md](S4-local-simulation.md) | `go test -count=1 ./internal/standalone -run '^TestS4'` |

## 统一收尾

```powershell
$env:GOMEMLIMIT='3GiB'
go test -count=1 ./...
go test -count=1 ./internal/...
go vet ./internal/... ./cmd/api
go build ./cmd/api
node --check internal/standalone/ui/app.js
git diff --check
```

## 边界

- S3 使用 loopback 上的两个本地实例和 `httptest` 自签 TLS 模拟，不代表真实局域网设备验收。
- S4-local-simulation 使用 loopback、`httptest` TLS/反向代理和本地 SQLite，不代表真实域名、公网、MySQL/Redis 或多副本部署。
- S4 的 daily token limit 是本地成本保护阈值，不是货币计费或可信账单。
- 当前 Windows 环境无法完成 `go test -race`（cgo 构建失败）；race 需在具备可用 C 工具链的 Linux/CI 环境执行。
- 本次 `go test -count=1 ./...` 在 `GOMEMLIMIT=3GiB` 下通过；`cmd/local-benchmark` 的 Windows 链接内存风险仍可能存在，不能外推到其他机器。
