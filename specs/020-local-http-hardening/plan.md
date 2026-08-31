# 020 本地 HTTP 加固计划

## 实现路径

1. 在创建 API Key 的唯一成功响应上设置 `Cache-Control: no-store`，并用管理端到端测试锁住敏感响应边界。
2. 将 `cmd/api` 的监听改为可测试的 `http.Server` 构造：读头 5 秒、读请求 15 秒、空闲连接 60 秒；`WriteTimeout` 保持零值，避免截断 SSE。
3. 扩展进程内 TokenBucket 配置：默认闲置 TTL 为 15 分钟、容量为 10,000。每次准入时在锁内回收安全过期桶；满且无可回收桶时拒绝新 tenant，不删除活跃桶。
4. 用注入时钟验证回收、活跃保护、容量拒绝和既有 `Retry-After` 语义；README 记录本地 SSE 超时取舍及限流边界。

## 风险与边界

- 回收只处理距最后一次访问达到 TTL 的桶；时钟回拨时不回收，避免误删活跃 tenant。
- 容量拒绝不写入新桶，沿用既有 Gateway 的 `rate_limited` → 429/`Retry-After` 映射。
- `WriteTimeout=0` 是明确的 SSE 兼容取舍，不等同于无限资源保证；客户端取消与 Provider 超时仍是流式路径的终止边界。
- 章程：暂未建立。

## 验证

`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`；并复跑 `make demo-stage-1` 保护既有 mock SSE、fallback 与 trace 演示。
