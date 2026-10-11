# Linux 单机后端部署与验收

> 初次使用千问的独立配置见 [千问部署手册](QWEN-RUNBOOK.md)：聊天及 SQL 分析走百炼，embedding 继续使用 Ollama；可选余额收费无需启动全部扩展服务。

> 当前完整版本见 [2026-10-10上线手册](ROADMAP-RUNBOOK.md)，新增人民币账本/第三Provider/缓存、独立执行器、ES/PG/写实验、Jaeger/Alertmanager及Kubernetes结构配置。proxy须按新Proxy.Dockerfile重建修复Go依赖。下文保留初版与核心检查点步骤，旧“未实现”边界由新手册覆盖。

> **2026-10-08 扩展版**：通用 MySQL 影子分析、审批后的 L2 测量/L3 数据绑定、JWT/Casbin、持久 RAG 和 Prometheus/OTel 已接入。新增部署/升级步骤见末尾第 7 节；SQL 接口契约见 `../sql-sentinel/docs/backend-expansion.md`。正文中 orders-v1/L1 的描述保留初版验收口径，由本节和第 7 节覆盖。

本目录与 `AgentMesh/`、`sql-sentinel/` 同级。使用联合包或从 AgentMesh 的 deployments/backend-stack 模板导出本目录；导出方法见 AgentMesh/scripts/export-backend-stack.py。当前交付不包含 UI。Docker Compose 在一台服务器上运行三个 MySQL 实例、Redis、Kafka、两个项目的服务与 Caddy；这是持久化单机方案，不提供跨机器高可用承诺。

## 1. 准备真实运行依赖

需要 Linux、Docker Engine、Compose v2.24.4+（本机验收 override 使用 `!override`）、Python 3、域名 DNS 指向服务器、放行 TCP 80/443 和必要的 SSH。数据库、Redis、Kafka 不开放公网端口。内存预算应覆盖各容器限额之和并留给系统和真实模型服务；当前容器限额约 5–6 GiB，不包含 Ollama 模型。按实测负载规划，不能把本机 fixture 的吞吐当作真实模型性能。

准备 Ark API Key 私有文件、可调用的 Ark model/endpoint ID，以及实际可达的 Ollama 服务和已拉取的 embedding 模型。Ollama 必须经可信内网或认证 HTTPS 访问，不能把无认证 11434 暴露公网。当前 Compose 不暗中创建 Ollama 或下载大模型，setup 要求显式提供可达 URL。若只测试聊天而 embeddings 不可达，完整上线验收仍不通过。

在**全新目录**运行，下面 example 值必须替换：

```sh
cd deploy
chmod +x init-*.sh redis-start.sh backup.sh
export COMPOSE_PROJECT_NAME=internship-backend
python3 setup.py --domain api.example.com \
  --ark-key-file /secure/ark-key --ark-model YOUR_ARK_ENDPOINT_ID \
  --ollama-url https://your-private-model-endpoint \
  --ollama-model qwen2.5:7b --embedding-model nomic-embed-text
docker compose config --quiet
docker compose build am-migrate ss-migrate
docker compose up -d control baseline candidate redis kafka
docker compose up am-migrate ss-migrate topics
```

setup 生成独立数据库密码、应用密钥、管理 token、webhook secret、连接文件和 `.env`。Linux secrets 父目录权限 0700，显式挂载文件 0444 供非 root UID 10001 读取；不能把目录改成公开访问，也不能上传到 Git。现有文件不会被覆盖：这是避免隐式轮换数据库密码；测试 fixture 配置不能通过重新运行 setup 转为生产配置，使用干净目录和新的 secrets/volumes。

初次 orders-v1 演示数据库初始化：

```sh
docker compose --profile setup run --rm seed
docker compose up -d gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker proxy
docker compose ps
python3 commission.py --url https://api.example.com
```

seed 只对本项目创建的隔离 baseline/candidate 执行 10000 行 orders-v1 数据集，**不要指向真实生产数据库，也不要对已有待保留数据重复 seed**。已有影子库需先备份、确认与 baseline 相同的 schema/数据快照。运行身份没有 root 或建表权限，建库/迁移/seed 为独立身份。`commission.py` 校验证书、聊天与 embedding 的真实模型调用、Eino 自动候选/完整影子报告，不写 GitHub 评论。它会消耗实际模型额度。

## 2. 运维管理与接口

Caddy 自动签发公开 HTTPS；`/v1/*` 对应网关，`/sentinel/*` 对应 SQL 服务，`/admin/*` 公网返回 404。管理使用 SSH 隧道：

```sh
ssh -N -L 18080:127.0.0.1:18080 your-user@your-server
```

通过私有客户端读取 `secrets/admin_token` 后作为 Bearer 调用 `http://127.0.0.1:18080/admin/...`，不要把 token 放 URL、命令行参数或终端录制。`secrets/bootstrap_key` 是 operator 应用 key；`sentinel_token` 授权 SQL jobs/report。创建应用 key 原文只返回一次，之后数据库仅保留哈希；撤销接口为 DELETE `/admin/api-keys/{key_id}`。

新租户 POST `/admin/tenants`：`{"tenant_id":"demo","model_routes":{"sql-review":["ark"],"embedding":["ollama"]}}`。初始额度默认 10000000 单位，持久记录 `initial` grant。已开通 operator 的 initial 金额必须与启动配置保持一致；不要通过改启动金额给已有租户充值，否则会被幂等冲突检查拒绝启动。补充额度 POST `/admin/quota/grants`：`{"tenant_id":"demo","grant_id":"credit-2026-001","units":10000}`。同 grant_id 重试不会再加额度，修改额度返回 409。详细请求契约分别见两个项目 `docs/backend-release.md`。

网关额度是预算单位，非精确 token 账单。默认每请求预留 4096；无法确认用量时保守扣预留全额。已转发输出由 rune 上限约束，同时给上游设置生成限额。仅支持聊天 SSE；默认 max_tokens=1024，上限 4096；stream=false 会拒绝。查询 trace 只返回所属租户的最近 128 条内存记录。SQL 报告与状态持久保存且需要独立认证。

## 3. 原生 GitHub 联调（可选）

替换 secrets/github_token 中 `disabled`，设置 `.env` 的 `GITHUB_REPOSITORIES=owner/repo`。Token 权限仅授予目标仓库读取 PR/代码；确需评论时授予 issues 评论写权限。Webhook 地址为 `https://api.example.com/sentinel/webhooks/github`，content type application/json，选择 pull_request，secret 为私有 webhook_secret。服务验签后从官方 API 查询当前 head 与完整 `.sql` 内容，不信任事件下载 URL。

先保持 `GITHUB_PUBLISH_COMMENTS=false`，在专用测试 PR 修改 orders-v1 只读查询 WHERE 行，确认返回的 delivery_id 任务 COMPLETED、报告为 L1。操作者确认需要发布后才设为 true 并重建 sentinel-api；评论按稳定任务 marker 更新，过期 head 标记 superseded。此次修复没有向真实 GitHub 推送代码或发表评论；本机仅测试内部协议 fixture。Go Raw SQL、migration DDL、通用 schema、自动 L2/L3 性能结论都不在当前自动运行范围。

## 4. 故障与配额恢复

关注 `/readyz` 与 `/sentinel/readyz`、容器健康状态、Kafka lag、未发布 outbox、超期 reservations/jobs、worker 重试、死信计数、Redis used_memory、持久盘余量。进程存活不等于依赖可用。readyz 不主动请求收费模型；真实 provider 可用性通过单独小额 smoke test 检查。日志不应含 SQL、原始消息、DSN 或密钥。日志轮换默认 3×10 MiB/容器，持久业务历史不会自动删除。

Redis 采用 AOF appendfsync always/noeviction；Kafka 保留 7 天并有持久 MySQL outbox。发布超过 5 分钟却未投影/处理的记录会重投，consumer 幂等处理。租户数健康检查上限 10000，离线恢复活跃 reservations 上限 10000；超过时拒绝，先停写核查/扩容。操作幂等日志与历史会增长，需要容量监控、经对账后的归档设计，不能直接删除仍需重放的账本。

Redis 钱包丢失时拒绝业务、拒绝普通充值，不再发初始额度。先停所有能产生网关请求/修改钱包的进程，再离线恢复：

```sh
docker compose stop gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker
docker compose run --rm --no-deps gateway quota-rebuild --offline --tenant operator
# 对所有受影响租户执行，核对 grants 与终态账本
docker compose start gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker
```

恢复以 MySQL 全部 grants、终态扣费为准，未终态记录保守占用全额，恢复后台再终结。Redis 退款成功/数据库未确认后再丢 Redis 可能造成保守多扣，人工核账后用唯一 grant 补偿。不能 flush Redis、删除 grants、重发 initial 来“恢复”。旧版本只有 Redis 额度而无 durable grants 的租户必须先停流量备份、核对真实累计发放历史并导入可信 grants，再重建；必须包含真实原始初始金额的 `initial` 记录，并让 operator 启动配置与该金额一致，额外历史发放使用不同 grant_id，避免启动时新发一笔 initial。来源无法验证时不能自动推断额度。

## 5. 备份、恢复、升级

Linux `sh backup.sh` 会停写者、导出控制库与两个影子库、Redis RDB、SHA256 清单，然后恢复进程。备份目录 0700/文件受 umask077 保护。secrets 需单独复制到加密离机存储，SQL 文本/报告和备份都视为私有数据。脚本需要一段停写窗口；对备份做实际还原演练，不能仅看 dump 成功。

在**新项目名、新数据卷、独立网络**恢复演练：生成匹配/安全替换的 secrets，启动三个 MySQL 与 Redis；通过容器内 mysql 从各 SQL 文件导入相应库，再运行幂等迁移。恢复旧 Redis 数据需遵守 Redis RDB/AOF 加载规则；推荐将恢复后的 MySQL grants/账本作为权威，在全部 API 停止时逐租户 quota-rebuild，避免误把新空 AOF 当成已恢复钱包。创建 topics 并启动 relay/worker，依赖 MySQL 重投修复缺少 Kafka 历史，不依赖恢复旧 consumer offset。

恢复后核对 jobs/reports/grants/终态统计、余额与 snapshot，完成 commission，再切换域名/流量。不要将验收数据库覆盖到运行库，不要 `down -v`，不要删 secrets 或生产卷。定期校验 SHA256 并保留至少一次可还原的离机备份。

升级前停写/备份，构建新镜像，运行 migrate 后启动服务。迁移为增量、逐步持久，AgentMesh 校验迁移哈希并使用锁；SQL Sentinel 为 additive IF NOT EXISTS。不自动回滚 schema 或删除业务历史。回滚代码前确认旧代码兼容已升级的 schema/钱包版本；不兼容时恢复备份到独立环境后切流量。保存上一镜像 digest 与 release manifest，定期升级 Go/依赖/基础镜像并重做漏洞扫描与故障验收。

## 6. 本机可复现验收

仅在隔离目录运行（含故意停机、SIGKILL、钱包删除和新临时数据库还原）。不能对真实环境执行 acceptance.py：

```sh
python3 setup.py --acceptance
docker compose -p codex-backend-acceptance -f compose.yml -f acceptance.yml build am-migrate ss-migrate fixtures
docker compose -p codex-backend-acceptance -f compose.yml -f acceptance.yml up -d control baseline candidate redis kafka fixtures
docker compose -p codex-backend-acceptance -f compose.yml -f acceptance.yml up am-migrate ss-migrate topics
docker compose -p codex-backend-acceptance -f compose.yml -f acceptance.yml --profile setup run --rm seed
docker compose -p codex-backend-acceptance -f compose.yml -f acceptance.yml up -d gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker proxy
python3 acceptance.py
python3 acceptance-expanded.py
python3 live-regressions.py
```

## 7. 扩展版部署和升级

生产 setup 增加 `--embedding-dimension 768`（换成真实模型维度）。全新目录自动生成独立 `jwt_key`；已有部署不能只更新源码，先备份、停写并运行新 ss-migrate。运行 `docker compose up -d prometheus otel-collector` 启动私网采集。Prometheus 保留 14 天/500MB、Collector debug 日志按 Docker 轮转，业务记录默认不自动清理。最初单机验收含模拟供应商，此次仍需用真实域名/provider 做 commission。

baseline 运行用户新需 `CREATE TEMPORARY TABLES`，candidate 运行用户新需 `CREATE TEMPORARY TABLES`，保留 SELECT/INSERT/INDEX（ANALYZE TABLE 要求 INSERT）。初次初始化脚本自动赋权，**已有数据库不会重跑 init 脚本**，应由私有管理员在相应隔离 shadow 实例执行：

```sql
-- baseline 实例
GRANT CREATE TEMPORARY TABLES ON sentinel.* TO 'baseline'@'%';
-- candidate 实例
GRANT CREATE TEMPORARY TABLES ON sentinel.* TO 'candidate'@'%';
```

这两实例仅存导入的影子数据，不能指向生产库。通用表通过管理员将相同 schema/脱敏数据导入 baseline/candidate，在 `.env` 设置 `SQL_SENTINEL_SHADOW_TABLES=orders,customers,invoices` 后重建 API/worker；最多16表，均需主键。默认 orders seed 只适用新演示库，不能替换真实已导入数据。导入前停 worker，完成快照核对后再启动。

SQL 管理使用独立隧道：`ssh -N -L 18081:127.0.0.1:18081 user@server`。以私有 sentinel_token 发起 `/auth/tokens`，为作者、审阅者和读者发短期 JWT，分配 tenant_id/project_id/tables；公网 `/sentinel/auth/*` 返回404。新普通用户请求只使用 JWT，不散发 bootstrap。1小时到期、支持服务端撤销；JWT key 轮换会使旧 JWT 失效，应停流量更新 secret 并重建 API。

工作流：作者 POST `/jobs` → 查询完整报告及 SHA header → reviewer POST `/jobs/{id}/approval` → POST `/jobs/{id}/benchmark` 调度新 delivery。审批不可覆盖；变更候选/SQL须重新创建与审批。分析报告保持 L1，基准报告单独给实际等级，噪声/无显著收益不升级。L3 需要数据所有者声明、脱敏代表性快照和精确 SQL 哈希，缺资料最高 L2；具体绑定配置见接口契约，不能用 fixture 声称 L3。

RAG 默认通过 gateway embedding、Eino chat 和 MySQL 持久向量，模型维度不匹配即拒绝；上传知识、检索、带引用回答和撤回接口受同一 JWT 域隔离。32KiB/doc、10000chunks/scope 是有意设定的容量边界。真实模型的答复质量、延迟和成本需单独评测。

Prometheus 没有公网 listener。如需查看，操作者可通过临时 Compose override 仅增加 `127.0.0.1:19090:9090`，再使用 SSH 隧道。`/targets` 看 backends/collector，`/alerts` 看 BackendUnavailable/BackendErrors/SQLJobsStuck，未配置 Alertmanager 不会发送外部通知。OTel 默认 Collector 只记录采集摘要，没有持久 trace 查询平台；要长期查看完整 trace，将 exporter 配到部署方的追踪存储，不能将 debug basic 日志当作完整 tracing backend。

更新挂载的 Caddyfile 后必须执行 `docker compose exec -T proxy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile`，成功再 `caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile` 或重建 proxy，单纯 up 不会保证重载挂载文件的内容。升级后验证公网 auth/metrics仍404，完成 jobs/approval/benchmark/RAG smoke，再切换流量。

完整业务备份现在还包含 JWT sessions、审批/审计、RAG 向量/版本。恢复后需验证引用与域隔离，按恢复点决定撤销/重新签发 JWT；旧 backup 之后的撤销不会自动出现。不要运行 acceptance 脚本到生产，这些脚本会停服务、造数据、发大额测试预算、删除测试钱包。真实云端只执行 commission 和业务 smoke。

验收监听只在 127.0.0.1：网关29080、SQL29081、HTTP代理29880、本地受信 TLS29443；DB/broker 的验收端口也仅回环。fixture 只模拟 Ark/Ollama/GitHub 协议，不访问外部，不计入真实模型和真实 PR 验收。验收镜像由 Dockerfile.fixture 构建，生产 compose 不包含 fixture。两个 Dockerfile 的 verify target 执行 Linux race/vet，全量源码保留 CI 和回归测试；CI 配置此次只本地验证，未上传触发 GitHub Actions。

完整备份/恢复演练还可在该隔离项目执行：`COMPOSE_PROJECT_NAME=codex-backend-acceptance sh backup.sh backups/acceptance-full`，然后在联合包根目录运行 `python3 verify-acceptance-backup.py`。该验证器只对新临时数据库和无网络 Redis 容器执行还原，结束移除临时目标；不能将它当作覆盖生产数据的恢复命令。
