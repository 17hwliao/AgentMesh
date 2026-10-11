# 两项目 Linux 上线手册（2026-10-10）

本手册补充 README.md，当前接口契约见两仓库 docs/backend-roadmap.md 与 backend-expansion.md。旧报告和初版路线图是历史记录；以新验收清单为准。交付为源码与配置，真实云域名、模型账户及支付商户仍需单独联调。

扩展配置可关闭，但要同时更新gateway/API/worker及监控配置。已存的兼容平台租户不会阻止主配置启动；包含停用provider的路由整体403，其他路由继续工作，不会自动把账单或请求切到别的平台。恢复扩展后仍使用原持久路由。操作前做备份并完成目标配置验收；不要删除租户数据来规避配置错误。

## 主部署：先准备真实模型与影子数据

可解压联合包，或同级克隆两个仓库并在 AgentMesh 运行 scripts/export-backend-stack.py 导出 deploy 模板；AgentMesh、sql-sentinel、deploy同级。需要Linux、Docker/Compose（支持!override，≥v2.24.4）、Python3、openssl、域名DNS和80/443入站。数据库/broker/gRPC/监控不开放公网；只通过SSH隧道管理。按Compose各 mem_limit 总和再加系统/模型内存规划机器；扩展会额外运行ES、PostgreSQL和实验MySQL，不应直接沿用最初5–6GiB预算。Ollama模型服务由操作者提供。

```sh
cd deploy
python3 setup.py --domain api.YOUR_DOMAIN --ark-key-file /PRIVATE/ark-key \
  --ark-model YOUR_ARK_ENDPOINT --ollama-url http://TRUSTED_PRIVATE_HOST:11434 \
  --embedding-model YOUR_EMBEDDING_MODEL --embedding-dimension YOUR_DIMENSION
# setup保留旧.env与secrets，不覆盖；已有配置须逐项核对。
chmod 700 secrets
chmod +x init-*.sh *-start.sh backup.sh rebuild-history.sh
docker compose build am-migrate ss-migrate proxy
docker compose up -d control baseline candidate redis kafka
docker compose up am-migrate ss-migrate topics
```

导入由数据所有者提供的合法脱敏数据到独立baseline/candidate，使用相同schema、主键、常驻索引和数据；禁止生产可写DSN。配置SQL_SENTINEL_SHADOW_TABLES=允许的表，并避免永久索引使用idx_cand_前缀。生产不要执行seed或acceptance脚本，它们会造数据、赠额、停服务及故障注入。

```sh
docker compose up -d gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker prometheus otel-collector proxy
docker compose exec -T proxy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
python3 commission.py --url https://api.YOUR_DOMAIN
```

公网需实际验证有效HTTPS、未认证拒绝、聊天完整DONE/usage、embedding维度、SQL报告、审批后benchmark和RAG引用。公网上/admin/*、/metrics、/sentinel/auth/*、/sentinel/metrics应不可用。真实域名证书及供应商联调本地未执行。共享影子表没有自动行级租户隔离，不能给不同租户授权含他人数据的表。

## 可选扩展：精确余额、执行器、历史和持久监控

```sh
python3 setup-extensions.py
```

此步骤生成内部CA/叶证书、service token、可选库密码，保留所有现有资料；生成的价格example不会自动启用收费，也不会创建真实provider key。将真实兼容平台key写入secrets/compatible_key，审核并写入secrets/billing_catalog；设置.env COMPATIBLE_BASE_URL与COMPATIBLE_MODELS，公开alias/上游model须与价格目录对应。千问北京非思考档位只是可核对示例，需按账户、上下文和思考模式检查当前官方价；不能把其他平台或榜单模型套用该价格。sql-review路由也必须有对应收费条目，否则SQL模型提案不可用。

本手册的扩展命令统一使用两个-f及profile，避免混用基础配置导致worker凭据/执行器挂载发生变化：

```sh
docker compose -f compose.yml -f extensions.yml --profile extensions up -d control
docker compose -f compose.yml -f extensions.yml --profile extensions up am-migrate ss-migrate runner-grants
docker compose -f compose.yml -f extensions.yml --profile extensions up -d elasticsearch postgres write-shadow jaeger-init jaeger alertmanager
```

ES默认8.19.23；本机拉取该版本两次EOF，协议验收临时使用缓存8.19.17，不能据此声明8.19.23实际镜像已验证。目标服务器先拉取/审查支持版本并固定批准的digest，再进行HTTPS/security/key烟测。ES需要Linux vm.max_map_count满足官方要求，并检查磁盘和容器内存；不得关闭鉴权或证书校验解决问题。

bootstrap-history.py只接受受信本机https://localhost:9200（验收29200），通过SSH临时转发ES，使用内部CA验证localhost SAN。转发仅用于bootstrap，不将ES监听开放公网：

```sh
python3 bootstrap-history.py --url https://localhost:9200
docker compose -f compose.yml -f extensions.yml --profile extensions up -d model-runner verifier
docker compose -f compose.yml -f extensions.yml --profile extensions up -d gateway sentinel-api sentinel-worker usage-relay usage-worker sentinel-relay prometheus otel-collector proxy
```

首次reader授权现在由runner-grants在AM迁移后创建，只有两张identity表SELECT权限；gateway/worker不持provider或影子写凭据。Health只验证服务传输/依赖，不产生收费模型调用。启用金额模式前，通过私网/admin/billing/credits录入可信赠额或真实收款凭证，给SQL/RAG使用的operator钱包充值；余额初始为0，旧quota余额不兑换人民币。GET /v1/billing/balance与requests对账；pending_reconciliation必须从真实供应商取得usage/proof补账。无支付渠道资料时只完成该后台闭环，没有真实自动收款。

Jaeger内部OTLP接Collector，单节点Badger保留48小时；查询API为/api/v3，查询需要时间范围。Alertmanager保存私网告警，默认receiver没有外部通知目的地。用SSH隧道看Jaeger/Prometheus/Alertmanager，不挂公网上；外部通知渠道须操作者明确配置。观察billing pending/超时holds/history backlog等告警。Collector故障不应阻塞业务，但采集队列与重试有界，不能承诺故障期间所有trace不丢。

## 实验、Kubernetes和独立核对

PG与write-shadow仅实验，不作为线上业务库。建立experiments可写目录（运行UID10001），加入要验证的query.sql与candidate.json：

```sh
mkdir -p experiments
sudo chown 10001:10001 experiments
# 暂停共享影子任务，避免干扰读测量；恢复时沿用扩展配置。
docker compose -f compose.yml -f extensions.yml --profile extensions stop sentinel-worker verifier
docker compose -f compose.yml -f extensions.yml --profile extensions run --rm pg-compare
docker compose -f compose.yml -f extensions.yml --profile extensions run --rm write-probe
docker compose -f compose.yml -f extensions.yml --profile extensions start verifier sentinel-worker
```

PG映射有边界且只证明当前快照结果；写入是4并发合成fixture，始终L1。噪声门禁拒绝时不得挑掉样本或强制升级；百万行、真实混合写流量和稳定Case2b指标需要另给数据。L3只有精确hash/有效期/数据所有者声明匹配且L2合格才可能成立。

可选Kubernetes renderer基于当前Compose生成单副本资源；随包JSON带fixture-only-do-not-deploy注释，仅供结构验收。先替换真实.env并把镜像运入已授权registry，再使用render-kubernetes.py生成；无真实配置会拒绝fixture端点。见kubernetes/README.md。已使用官方Kubernetes Go API类型严格结构解码，不是集群admission；本机没有cluster context。数据库迁移、runner-grants、topics及jaeger-init Jobs必须按依赖次序完成，再启动应用；需要实际CNI、StorageClass、LB/DNS/HTTPS验收。不声称HA或egress限制。

## 备份、重建和定期轮换

```sh
BACKUP_EXTENSIONS=true COMPOSE_PROJECT_NAME=YOUR_PROJECT sh backup.sh backups/YOUR_PRIVATE_BACKUP
```

停止基础写者及model-runner/verifier，备份4个权威SQL库、Redis RDB并校验，随后恢复进程。secrets/CAkey单独加密离机保存；SQL/RDB也是私有数据。ES只重建，PG/write-shadow实验数据可丢弃；Jaeger/Prom/Alertmanager如需保留历史另做卷备份。恢复必须在新卷/独立项目先验证人民币wallet/held、credits/requests/attempts/audit、审批/RAG/outbox与Redis额度，再切换；不要把验收备份覆盖生产。

ES丢索引：停止API/verifier/worker，重新bootstrap索引/最小权限key；COMPOSE_PROJECT_NAME=YOUR_PROJECT sh rebuild-history.sh --ack-history-rebuild，再恢复扩展写者。只有未过期MySQL历史记录可重建，20次重试耗尽也由此恢复。

内部CA有效90天、叶证书80天；ES app key30天；价格目录最长93天、example仅7天。部署负责人应记录提前续期窗口与密钥负责人。证书用openssl在私有目录生成新叶证书并核对SAN/用途/CA链，先备份旧文件、原子替换、重建相应容器。CA轮换应安排停写维护窗口，统一替换所有信任链与叶证书后复验。ES key轮换时将旧文件私有归档，重新bootstrap创建key、重建API/verifier，验证新key后通过ES管理接口撤销旧key。不能用删除钱包/重发initial修复余额。

## 本机验收重现

只能在新隔离目录使用setup.py --acceptance及setup-extensions.py --fixture-pricing，并绑定acceptance.yml / extensions-acceptance.yml，项目名codex-backend-acceptance；端口只回环。基础10组acceptance.py，核心5组acceptance-expanded.py，可选7组acceptance-extensions.py，另有真实Redis/Kafka/MySQL/缓存、fresh reader、backup restore和Kubernetes结构检查。基础测试应切回基础部署模式；金额账本与token-unit额度是两种协议，不能混用预期。任何fixture PASS都不代表真实供应商、支付、云、GitHub或集群已经验收。

Go工具链1.26.9和x/net v0.60.0修复本次扫描可达问题。公网proxy从固定Caddy v2.11.7源码用同一修复工具链重建，不能沿用旧官方镜像中的Go1.26.8二进制。Proxy.Dockerfile和proxybuild/go.mod/go.sum随包固定；scanner检查应用与proxy源码/实际二进制，未进行所有容器OS包的全面CVE审计。镜像tag只用于本地构建，转移上线时另固定发布digest。
