# 本地后端改进契约（2026-10-11）

本轮收敛流式协议、Provider 配置、计费状态与本地性能证据。真实模型、支付商户及云服务器仍需单独验收。

## 流式协议

`internal/sse.Reader` 供兼容模型适配器、网关客户端和本地基线共同使用。支持 LF/CR/CRLF、首个 UTF-8 BOM、注释心跳、多行 `data:` 及任意网络分片。只有空行结束的完整事件才交付；未完整结束的 `[DONE]`、截断、非法 JSON、上游 error 事件不能计为成功。单事件和单行上限约 128 KiB，元数据也计入上限；总响应另受网关输出限制与请求超时约束。

取消请求会关闭正在读取的上游 HTTP 响应。已经输出内容后的失败不切换模型；实际用量缺失时，账单进入待核对状态，不能把截断请求记为免费成功。这些规则保留原有网关/账单约束。

## Provider 与价格绑定

`internal/providerconfig` 是 Go 网关、本地执行器、远程模型映射和离线配置检查共用的验证入口。模型别名最多 128 字符，兼容映射最多 32 项/16 KiB，重复 JSON 键被拒绝。模型名称允许常见版本/命名空间字符；API Key 必须为不含空白或控制字符的可打印 ASCII。Base URL 只允许 HTTP(S)，禁止内嵌凭据、查询串与 fragment，显式端口必须有效。

`COMPATIBLE_MODELS`、`COMPATIBLE_DISABLE_THINKING`、`COMPATIBLE_USE_MAX_COMPLETION_TOKENS` 在连接远程执行器前校验。布尔开关只接受空值、`true`、`false`。价格表复用同一别名/模型名称规则，并继续要求显式的上游模型、价格版本、有效期和 CNY 整数纳元结算。配置校验不证明供应商支持某项能力，也不替代真实用量与账户单价核对。Python 部署生成器保留独立的千问地域检查。

## 状态与计费验证

独立 `big.Int` 算法核对 1,000 组带缓存输入的计价样本。专用 MySQL 测试逐次检查：充值总额 = 当前余额 + 累计扣款；冻结额 = 未结算请求预留额；冻结额不能超过余额。24 组请求覆盖重复充值/冲突、取消、重复与冲突用量、未知用量核对、崩溃恢复、重复终态及审计唯一性。该测试只接受 `agentmesh_improvement` 数据库，价格是固定夹具，不是实际千问报价。

## 可复现本地基线

`cmd/local-benchmark` 新增 `--stream-chunks`（0..64）、`--chunk-bytes`（0..64，乘积不超过 4096）与 `--read-delay-ms`（0..20）。两项分片参数须同时设置或同时为零。报告保存完整流消费延迟 P50/P95/P99、成功/失败样本、限流断言、累计分配量、分配次数、25 ms 采样的堆/goroutine 峰值及首尾 goroutine。Linux 额外记录进程 user+system CPU；其他平台明确标记不可用。

```sh
go run ./cmd/local-benchmark --rounds 3 --requests 80 --concurrency 8 --stream-chunks 64 --chunk-bytes 64 --out .private/long-stream.json
go run ./cmd/local-benchmark --rounds 3 --requests 80 --concurrency 8 --stream-chunks 16 --chunk-bytes 64 --read-delay-ms 2 --out .private/slow-reader.json
```

资源统计包含同一进程内的客户端、mock 网关、预热和限流测试；堆不是 OS RSS，采样可能漏峰值。慢读是每个事件的消费延迟，不模拟超出 TCP 缓冲区的大响应背压。这是新基线，没有同环境旧版对照，不能给出“提升百分比”、数据库容量或生产吞吐承诺。Git HEAD 只标记基础提交；工作区源码身份以联合验收 SHA256 清单为准。

导出 [联合部署模板](../deployments/backend-stack/EXPORT.md) 后，`deploy/verify-local-improvements.py` 提供 `host`、`linux`、`persistent`、`bench`、`fuzz`、`contracts` 模式。需要 Go/git/rg/gofmt/Python/Docker；Linux 模式依赖本地验证镜像，首次使用可分别在两个源码目录执行 `docker build --target verify -t local/agentmesh-verify:roadmap .` 和 `docker build --target verify -t local/sql-sentinel-verify:roadmap .`；MySQL 模式使用 mysql:8.4.11。正式部署仍按既有 [运行契约](backend-roadmap.md) 与导出的上线手册执行。
