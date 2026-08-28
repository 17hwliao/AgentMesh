# 任务

- [x] T001 新增内存 Tenant/APIKey Store 与 auth bootstrap：只存 prefix/hash、常量时间 Bearer 比较、tenant Context、稳定 `auth_failed`。验证：`go test -count=1 ./internal/tenant/... ./internal/auth/...`、`go vet ./...`。
- [x] T002 接 Gateway/CLI 的 body 前认证、tenant-model 路由、仅 allow 的 QuotaGate 与受保护 health；测试跨 tenant 隔离、model 拒绝、零 Provider 调用和 HTTP contract。验证：`go test -count=1 ./internal/gateway/... ./internal/gatewayclient/... ./cmd/...`、`go build ./...`。
- [x] T003 进程内生成一次性 key/hash，运行仅本机 mock API 和两个 CLI；记录 auth/model/quota 拒绝的状态码与 Provider 调用数，更新 README/计划；不操作 Docker/真实 Provider。验证：`go test -count=1 ./...`、本机 `go run` 演示。
- [x] T004 全量 `gofmt -l .`/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`；不 push/tag。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`。
