# 双控制台生产部署方案

2026-10-23：认证入口已从 Clerk 改为自托管 Casdoor + QQ SMTP。目标仍为 Linux 单机 Docker Compose、HTTPS 反向代理、两个独立业务控制台。本地源码与受控测试通过不代表真实邮件、云服务器和模型调用已经验收。

## 拓扑

- `mesh.<域名>` → AgentMesh Node BFF → Go 网关。
- `sql.<域名>` → SQL Sentinel Node BFF → SQL API / worker。
- `auth.<域名>` → Casdoor，一个用户池、两个 OIDC 应用。
- `api.<域名>` → 原 Go 公共 API，继续使用应用 API Key。
- 身份数据库独立 schema/账号；网页会话独立 Redis、按 issuer/client/origin 分区。业务 MySQL、Redis、Kafka、影子库和可选服务沿用既有配置。

Caddy 终止 HTTPS。认证、业务数据库和 Redis 不发布公网端口；两个 BFF 保留非 root、只读根文件系统、私有只读挂载与资源上限。Casdoor 镜像固定版本和官方摘要，不使用 latest。第三方身份镜像仍需按目标环境核实安全更新与资源占用，本机测试不替代这一步。

## 初始化与启动

1. 在全新部署目录按原后端手册准备私有配置、租户、价格、余额、数据库与迁移；既有部署先备份，不重复 seed 或覆盖秘密。
2. 按 [CASDOOR-RUNBOOK](CASDOOR-RUNBOOK.md) 初始化身份服务；没有 SMTP 授权码时可生成准备配置，但必须保持 email_delivery=unconfigured。
3. 取得完成验证的稳定 sub，为两个产品分别配置成员权限。运行新的 console-setup.py：使用 identity-private 元数据和 --api-origin，不再要求 Clerk Key。
4. 在原来确认的 COMPOSE_FILE 组合上增加 identity.yml:consoles.yml；真实千问示例为 compose.yml:qwen.yml:qwen-billing.yml:identity.yml:consoles.yml。保留现有项目名、网络、卷和其他实际所需覆盖文件，不混入本地 acceptance 或 identity-local 覆盖。
5. 执行 Compose 配置检查，启动身份数据库、独立会话 Redis、Casdoor，构建两个控制台；分别运行 node check-config.mjs --probe，准备完成后再启动控制台与代理。
6. 生产预检要求真实 SMTP 模式。--allow-unconfigured-email 仅供准备检查，deployment_ready=false；readiness 不证明真实收件或供应商模型可用。

新增身份服务会增加单机内存和持久磁盘需求。模板资源限制是初始上限，不是容量或高可用承诺。账号、业务数据库与后台队列须按实际服务器资源配置。

## 首次真实环境验收

- 四个 HTTPS 入口、OIDC 固定 callback、来源与证书正确；两个产品能独立加载并互相跳转。
- QQ SMTP 服务端接受与真实收件分别核实；验证码错误、过期、重复使用及限流有明确结果，邮箱验证码不自动赠送业务权限。
- 同一账号访问两个产品，移除一侧成员后只拒绝该产品；账号停用及令牌撤销、服务中断均按预期拒绝。
- AgentMesh 网页图片上传、私有读取、真实模型回答、图片历史、用量与账单对应；沿用原 20 元累计预算，最少必要调用。
- SQL 诊断、报告、不同用户审批、测量和 RAG 按原业务边界验证；可选 GitHub、ES 未配置时保持明确缺口。
- 控制台退出当前产品撤销服务端会话；不声称自动退出所有产品。Cookie/CSRF 与后台应用 Key 为不同认证边界。

## 升级、恢复与运行

保留源码 SHA、镜像摘要、Compose 项目名与覆盖组合。升级 Casdoor 先备份身份数据库和 identity-private，不能重跑初始化覆盖用户；initDataNewOnly=true 保留既有数据。会话 key 轮换会使控制台已有会话失效。

backup.sh 在身份数据库属于当前运行的 Compose 组合时，会暂停认证和业务写入并额外导出 identity-casdoor.sql；私有 identity-private、console-private 仍需另外加密离机保存。Redis 会话故障会要求重新登录，不应丢失业务账单、图片或 SQL 任务。

关注 readyz、验证码失败率、发信限制、认证延迟、Redis/数据库资源、费用待对账和磁盘。服务未就绪时不隐藏错误或回退到免登录。真实云部署、域名操作、购买服务与公开发布均另按用户授权执行；本次只做本地实现。
