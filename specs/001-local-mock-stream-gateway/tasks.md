# 任务

- [x] T001 建立 Go module、`cmd/api`、Provider/Router 最小契约与可注入 mock；实现仅回环地址校验、`GET /healthz` 和可运行的最小 chat 入口。验证：`go test -count=1 ./...`、`go run ./cmd/api --help`、`go build ./...`。
- [x] T002 实现首块前 fallback、首块后规范化中断、SSE 转发与 Context 取消；为 fallback 边界和取消传播写离线测试。验证：`go test -count=1 ./internal/router/... ./internal/gateway/...`、`go vet ./...`。
- [x] T003 新增 `cmd/summary-cli` 与 `cmd/sql-diagnose-cli`，以 HTTP/SSE 调用网关；更新 README 并用 mock 服务完成两条真实本机 CLI 演示。验证：`go test -count=1 ./...`、两条 `go run` 命令。
- [x] T004 全量 gofmt/build/vet/test、Adaptive Spec 校验、私有阶段复盘、README 边界核验、本地提交并 fast-forward `master`；不 push/tag。验证：`$goFiles = rg --files -g '*.go'; gofmt -l $goFiles`、`go test -count=1 ./...`、`git diff --check`。
