---
level: L2
feature: 020-local-http-hardening
created: 2026-08-31
---

# 本地 HTTP 敏感响应与资源边界加固

**原始需求：** 修复安全审查确认的三项 Low 问题：一次性 API Key 响应可被缓存、HTTP 服务没有明确连接超时、进程内限流 bucket 永不回收。

## 目标

- `POST /admin/api-keys` 的成功响应在包含一次性原始 Key 时发送 `Cache-Control: no-store`；原始 Key 仍只出现在该单次响应中，其他管理/业务响应不扩展敏感字段。
- `cmd/api` 使用显式 `http.Server`，设置有限的 ReadHeader、Read 与 Idle 超时，限制慢头、慢请求与空闲 keep-alive 对本地资源的长期占用。
- SSE 不设置全局 WriteTimeout，避免合法长流被固定墙钟截断；该取舍及其仍需依赖客户端取消/Provider 超时的边界必须如实写入 README。
- 进程内 tenant TokenBucket 以可注入时钟淘汰闲置 bucket，并有固定容量上限；达到上限且没有可安全淘汰的闲置 bucket 时，以既有 `rate_limited` fail-closed，而不无限增长或删除活跃 bucket。

## 非目标

- 不增加远程监听、分布式/Redis 限流、跨进程共享、持久化、后台清扫 goroutine、配额/Reservation 语义或新的错误码。
- 不改变 010 的阻塞态、真实 MySQL/Redis/Provider 验证、管理 token/API Key 鉴权顺序、SSE 协议或 Provider fallback。
- 不把回收/上限的离线测试写成生产压测、真实基础设施或精确多实例限流证据。

## 默认假设

- 服务继续仅监听 `127.0.0.1`；读头 5 秒、完整请求读取 15 秒、空闲连接 60 秒，SSE WriteTimeout 保持 0（未设置）。
- bucket 闲置 15 分钟后才可淘汰；最多保留 10,000 个 tenant bucket。满载且无过期 bucket 时，新 tenant 请求得到可重试的 429，而现有活跃 tenant 的额度不被重置。
- 限流仍默认关闭，只有既有成对环境变量完整时启用；所有新增边界均离线、确定性可测。

## 验收条件

1. 成功创建 API Key 的 201 响应含 `Cache-Control: no-store`；原始 Key 不进入日志、错误响应、trace、数据库或后续查询，既有 admin 鉴权先于 body 解码不回归。
2. API server 的读头、读请求与空闲超时有可测试的固定值；SSE WriteTimeout 明确为 0，既有 mock SSE、首块前 fallback 与取消路径不被超时配置破坏。
3. 限流器用可注入时钟证明：闲置 bucket 被回收、活跃 bucket 不被回收、map 不超过上限、上限满载时不创建新 bucket且返回拒绝；tenant 隔离与 `Retry-After` 语义保持。
4. README 如实记录 no-store、超时取舍和进程内限流回收边界；全量格式/build/vet/test/diff 与 Adaptive 校验通过。

## 实现范围与验证

- 涉及 admin handler、`cmd/api` 服务构造、ratelimit、离线 HTTP/时钟测试与 README；不新增 L3 专项文档。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；不 push、不 tag、不启动 Docker。
