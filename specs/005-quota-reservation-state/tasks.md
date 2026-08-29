# 任务

- [x] T001 新增 Reservation/Attempt 状态模型、转换和稳定错误码；覆盖 `cancelled` 边界、终态和 `expired_pending` 限制。验证：`go test -count=1 ./internal/reservation/...`。
- [x] T002 新增锁保护内存 Repository、tenant 归属和 `(reservation_id, expected_version, operation)` 幂等；覆盖重放/冲突/跨 tenant/重复终结及并发。验证：`go test -count=1 -race ./internal/reservation/...`、`go vet ./...`。
- [x] T003 运行受控状态场景，更新 README/plan，记录内存语义及 Redis/MySQL/reconciler 未实现事实；不接 Gateway/真实 Provider/Docker。验证：`go test -count=1 -run Reservation -v ./internal/reservation/...`。
- [x] T004 全量 `gofmt -l .`/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`；不 push/tag。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`。
