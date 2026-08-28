# 任务

- [x] T001 实现 `--providers` 严格选择、环境配置加载与 Ark Adapter（stream/error/health/Context）；默认 mock 不读取真实配置。验证：`go test -count=1 ./internal/provider/... ./cmd/api/...`、`go run ./cmd/api --help`、`go build ./...`。
- [x] T002 实现 Ollama Adapter、仅名称/状态的 `/health/providers`、首块前 Ark→Ollama fallback 与首块后不切换集成测试。验证：`go test -count=1 ./internal/provider/... ./internal/gateway/... ./internal/router/...`、`go vet ./...`。
- [x] T003 用显式环境配置尝试真实 Provider；记录名称、模型、尝试次数及成功或受控配置拒绝，更新 README。验证：`go test -count=1 ./...`、受控 `go run ./cmd/api --providers ark,ollama`；不操作 Docker。
- [x] T004 全量格式/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`；不 push/tag。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`。
