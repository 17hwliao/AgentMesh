# 任务

- [x] T001 新增安全的有界内存 trace/usage Recorder 与 Router observer；覆盖 attempt、首块、结果、取消、usage 缺失和淘汰。验证：`go test -count=1 ./internal/observability/... ./internal/router/...`。
- [x] T002 接 Gateway trace 生命周期及认证、tenant 收窄的查询 HTTP contract；覆盖跨 tenant/未知/未完成同码 404、零 Provider 查询和不泄露。验证：`go test -count=1 ./internal/gateway/...`、`go vet ./...`。
- [x] T003 真实本机 mock trace 运行、`make demo-stage-1`、私有 resume/FAQ、三份 `decisions/`、README 更新；不接外部依赖/Docker。验证：`go test -count=1 ./...`、`make demo-stage-1`。
- [x] T004 全量 `gofmt -l .`/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`；不 push/tag。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`。
