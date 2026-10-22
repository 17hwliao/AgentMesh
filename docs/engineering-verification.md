# 本地工程验收与面试演示

本轮增加真实依赖 CI、独立进程负载测试、进程崩溃测试、双实例测试、Python SDK 和 OpenAPI。测试使用隔离的合成模型与数据，不需要真实模型 API Key，也不代表云端或真实千问已经验收。

## 一条命令演示

将两个仓库放在同一目录，目录名为 `AgentMesh` 和 `sql-sentinel`。准备 Docker/Compose（支持 `!override`）、Python 3、Go 1.26.10 与 OpenSSL。在 AgentMesh 内执行：

```sh
python scripts/demo-local.py --out .private/interview-demo
```

脚本构建当前源码镜像；首次安全导出同级 `deploy` 模板，生成本地凭据，迁移数据库、建立主题并准备影子数据。首次项目名为 `codex-backend-lab-xxxxxxxx`，端口在原验收端口上增加 1000；复用既有隔离验收项目时沿用其配置。服务均通过 loopback 暴露。脚本只接受明确的 fixture 配置。

演示依次完成：创建租户和测试赠额、经过代理/gRPC 执行流式请求、按实际 usage 扣减人民币余额、生成 SQL 影子报告、SIGKILL worker 并验证恢复、用 Python SDK 重试同一 SQL delivery。输出目录保存 JSON 报告，不打印凭据，临时租户 Key 文件在结束时删除。

`--reuse-images` 显式复用已构建的本地 engineering 镜像。测试赠额不是支付入账，报告中的局部 SQL 结果相同不是未来数据上的等价证明。演示默认使用已验证缓存的 ES 8.19.17 fixture 镜像；生产 ES 版本与上线流程见联合部署手册。

## 可复现测试

```sh
go vet ./...
go test ./...
python -m unittest discover -s sdks/python -v
python -m unittest discover -s scripts -p 'test_*.py' -v
python -m pip install -r scripts/requirements-contracts.txt
python scripts/validate-contracts.py
docker build --target verify -t backend-integration-runner .
python scripts/verify-integrations.py --runner-image backend-integration-runner --out .private/integration-results
```

真实依赖 runner 创建独立内部网络、MySQL/Redis/Kafka 和临时凭据，不复用数据卷、不绑定宿主端口；结束后只删除本次创建的对象。MySQL 使用 1.5 GiB 上限和临时存储，Kafka 1 GiB，Go runner 2 GiB。整库 race 测试的 mandatory 名称必须实际 PASS；缺失环境造成的 SKIP、测试缺失或其他测试失败均不能使 integration job 成功。子进程入口在父测试之外 SKIP 是有意行为。默认跳过的资源基线可用 `LOCAL_BENCHMARK_OUTPUT_DIR` 单独执行。

两个仓库的新 CI job 只测试和构建，没有发布、开 PR、创建标签或部署步骤。当前新增 CI 配置的远端运行需后续明确上传后确认。

准备好 fixture 栈后，在 AgentMesh 内运行以下有顺序要求的场景；它们会注入故障，不能指向真实业务环境：

```sh
python scripts/backend-lab.py prepare --deploy ../deploy --out .private/prepare
python scripts/backend-lab.py acceptance --deploy ../deploy --out .private/acceptance
python scripts/backend-lab.py replicas --deploy ../deploy --out .private/replicas
python scripts/backend-lab.py backpressure --deploy ../deploy --out .private/tcp
python scripts/backend-lab.py load --deploy ../deploy --out .private/load
python scripts/backend-lab.py soak --seconds 1800 --deploy ../deploy --out .private/soak
```

`acceptance` 先临时关闭金额目录、验证旧 token-unit 协议 10 组，随后恢复金额模式并验证 5 组核心扩展及 7 组条件扩展。两种钱包不混用。`extensions` 可单独复验后 12 组。

## 如何读证据

| 场景 | 实际核对 |
| --- | --- |
| 开放/封闭负载 | 独立 CLI 经 HTTP 代理、网关、mTLS executor 和实际存储；首个内容、完成耗时、调度迟延、429、失败和 dropped 分开记录 |
| 封闭负载 | 对 429 按当前 `Retry-After` 等待；达到 200000 样本上限会失败，不能冒充完成全部时长 |
| 持续负载 | 每 30 秒记录容器 CPU/内存、Go heap、goroutine 和文件描述符；预热后比较窗口，保留逐请求和资源原始记录 |
| 资源门禁 | 内存增长不超过 `max(64 MiB, 25%)`；Go heap 不超过 `max(16 MiB, 25%)`；goroutine/FD 不超过 `max(32, 25%)`；这是有限时长回归门禁，不是永久无泄漏证明 |
| TCP | 实际缩小内核收发窗口，证明阻塞能反向作用到上游并在断开后退出；完整 RPC 链路另验证不读客户端及 1 MiB 输出上限 |
| 金额崩溃 | 真正杀死子进程，覆盖冻结、已开始、usage 已存、结束状态已存和已结算五个边界，重复恢复不能多扣；usage 已存但 outcome 未存仍待对账 |
| 双实例 | 两网关共享余额与撤销状态；worker SIGKILL 后接管；三条实际 Kafka 重复事件消费到 lag=0，终态报告及完成记录仍唯一 |
| SQL | 32 组真实 MySQL 语义对照；另有十万行/95% 热点数据、20 噪声轮与 5 正式轮、真实 EXPLAIN ANALYZE，低效非覆盖索引作为负样本 |

网关默认认证最多 8 个并发数据库查询，每个 MySQL pool 最多 4 条连接；流式请求默认全局 64、每租户 8。认证依赖异常返回 503，失效 Key 返回 401，超出 admission/rate 配额返回 429。双实例的应用内并发限制按实例计算；共享余额与 Redis 限流由实际存储保证。

## SDK 与 API 契约

`docs/openapi.json` 为 OpenAPI 3.0.3。管理接口仅私网，使用独立 admin credential；流式响应必须收到完整 `[DONE]`，错误可能发生在 HTTP 200 后的 SSE 中。金额为整数 nano-CNY，1 元 = 1000000000 nano。

`sdks/python/backend_client.py` 无运行时第三方依赖，读取私有 Key 文件，验证 TLS、拒绝重定向，限制事件/响应大小。`Client.chat()` 必须通过 context manager 使用；提前退出会关闭响应。SDK 不自动重试收费请求；SQL 请求仅在保持相同 delivery 与相同内容时安全重试。

```sh
python sdks/python/example.py --gateway-key-file /private/tenant-key --sentinel-key-file /private/sentinel-token
python scripts/validate-contracts.py --gateway-key-file /private/tenant-key --sentinel-key-file /private/sentinel-token
```

地址和 CA 可通过参数明确配置。示例默认只操作 loopback fixture；换真实环境前应检查查询、额度和凭据权限。

## 已知范围

30 秒负载和有限时长持续测试是本地合成容量，不用于承诺真实模型吞吐或云端 SLO。代码与实际 Go 二进制扫描不替代所有容器 OS 包的完整 CVE 审计。当前固定规则标签和样本量也不构成模型质量认证。真实云、千问 Key、支付商户、外部告警通知、GitHub Webhook 和 Kubernetes 集群联调仍按原运维手册单独 commissioning。
