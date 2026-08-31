# 017 本机并发评测计划

## 评测入口

1. 新建 `internal/localbench`：纯报告模型、样本汇总与 nearest-rank 分位数；失败样本保留在报告，成功延迟单独计算分位数。
2. 由该包建立短生命周期 loopback mock 网关、随机内存 tenant/API Key，并完整读取每个 SSE 响应至 `[DONE]`。预热后以 worker pool 执行多轮请求。
3. 在独立、同样认证的网关上安装现有 TokenBucket，执行 1,000 并发请求；比较 200、429 和其他状态，并以 mock 调用次数证明 429 没有进入 Provider。
4. `cmd/local-benchmark` 收集当前 Git SHA、时间、Go/CPU/GOMAXPROCS 与参数，原子写入 `.private/benchmark-results/` JSON，同时输出同一安全报告；`make benchmark-local` 调用该入口。

## 数据诚信

- 延迟从 HTTP client 发起到 body 完整读完；报告显式标注 loopback/mock，不能与真实 Provider 首 token、跨机网络或存储性能比较。
- 每轮保留所有成功与失败计数；任一失败、非 200/429 或断言不成立时仍写报告、再以非零退出。
- 分位数仅基于本次成功请求的原始毫秒样本；空成功样本不生成伪造分位数。
- 章程：暂未建立。

## 验证

为汇总、失败保留、SSE 消费及限流并发断言添加离线测试；运行 `make benchmark-local` 留下实际 JSON，再执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验。
