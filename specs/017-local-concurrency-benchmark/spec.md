---
level: L2
feature: 017-local-concurrency-benchmark
created: 2026-08-31
---

# 本机回环并发与延迟评测

**原始需求：** 为简历补齐准确、可复现的网关量化数据，不把离线替身、单元测试或受控拒绝误写成真实基础设施性能。

## 目标

- 新增一个仅本机的 benchmark 入口，在短生命周期 mock 网关上发送确定数量的已认证 SSE chat 请求，输出机器可读 JSON：Git commit、运行日期、Go 版本、CPU/GOMAXPROCS、配置、每轮原始耗时、成功/失败数、吞吐与 p50/p95/p99 延迟。
- 用固定的预热、轮数、请求数与并发度运行多轮，汇总只从本次原始样本计算；任何 HTTP/SSE 失败都作为失败记录，非零退出，绝不丢弃或以成功样本替代。
- 增加限流并发场景：固定 tenant、确定 burst、1,000 个并发已认证请求；报告允许数、429 `rate_limited` 数、其他状态数和 Provider attempt 数，并断言允许数不超过 burst、拒绝请求零 Provider attempt。
- 将本机实测 JSON 留入忽略的私有资料，README 只描述测量边界和复现命令；简历材料仅引用实际运行后生成的数字。

## 非目标

- 不测或宣称真实 Ark/Ollama、MySQL、Redis、跨进程/跨机器吞吐、成本、Token 精度、生产容量、p99 SLO 或 1,000 并发 Redis 预扣。
- 不增加 OTel/Prometheus、远程监听、持久化、Docker、新的鉴权/配额/Provider 语义，且不触碰仍阻塞的 010。
- 不以 `go test` 运行时间、mock 的零延迟或单次偶然结果替代多轮 HTTP/SSE 样本。

## 默认假设

- 默认使用 loopback `127.0.0.1`、既有 mock Provider、临时随机 tenant/API Key，运行结束关闭 server 并删除临时产物；不读取外部凭据。
- 默认预热后执行 5 轮，每轮 200 个请求、20 并发；这些值可显式覆写并完整写入报告，避免隐式比较不同运行。
- 延迟从客户端发起 HTTP 请求到完整 SSE 终止；它包含本机 HTTP、鉴权、路由与 mock 流，不代表真实模型首 token 或网络延迟。

## 验收条件

1. 固定时钟/依赖的离线测试验证样本排序、nearest-rank 分位数、零样本/失败处理及报告字段；真实运行不依赖外部服务或密钥。
2. `make benchmark-local` 在当前机器实际产生 JSON，含环境溯源、5 轮原始样本、汇总分位数和明确的 mock/loopback 范围；所有成功请求完整消费 SSE。
3. 限流 1,000 并发场景报告并验证允许数不超过 burst、429 数与拒绝数一致、拒绝的 Provider attempt 为零；不把这项单进程证据写成 Redis 配额实证。
4. README、全量格式/build/vet/test/diff 和 Adaptive 校验通过；不 push/tag/Docker。

## 实现范围与验证

- 涉及本地 benchmark CLI/Make target、纯计算与 HTTP 级测试、README 与私有实测记录；不新增 L3 专项文档。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`、Adaptive 校验和 `make benchmark-local`。
