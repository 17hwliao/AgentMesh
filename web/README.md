# AgentMesh 独立控制台

本目录独立构建、运行和授权，与 SQL Sentinel 共用自托管 Casdoor 用户池。两个 OIDC 应用各自使用客户端凭据，支持 QQ 邮箱验证码；业务权限仍由各产品管理。旧 Clerk Key 不再使用，旧配置会被明确拒绝。

要求 Node >=22.12。依次执行 `npm ci`、`npm test`、`npm run build`、`npm start`，默认地址为 http://127.0.0.1:4180/ 。`/?preview=1` 仅为明确标注的界面示例，不调用模型、不保存业务数据。

真实身份配置使用 `CONSOLE_CONFIG_FILE` 指定私有 JSON，字段参考 console.config.example.json。登录入口 `/auth/login` 跳转身份中心，`/auth/callback` 处理授权码，`/api/auth/session` 检查身份，`POST /api/auth/logout` 退出当前产品。浏览器使用当前 host 的 Cookie，不保存模型 Key、客户端 Secret 或 OIDC token。

业务写入要求当前 origin 及 `X-CSRF-Token`；页面会从受保护的身份接口取得该值。单独调用 Go API 的应用 Key、OpenCode/Claude Code 配置不受网页 Cookie 迁移影响。

共享身份通过验证后仍需本产品成员授权。成员映射使用 Casdoor 的 issuer 和稳定 sub，不按邮箱自动继承旧账号权限。没有授权的用户可以退出或重新检查权限，不会自动获得余额或管理员。

`email_delivery=unconfigured` 时登录按钮及 readiness 明确阻断；本地 `test_sink` 模式只使用隔离收件箱，生产拒绝此模式。`check:config` 检查配置和基础设施；没有邮件配置时只能用 `--allow-unconfigured-email` 做准备检查，返回 deployment_ready=false。

[完整 Casdoor 初始化、QQ SMTP 授权码获取和 Compose 步骤](../deployments/backend-stack/CASDOOR-RUNBOOK.md)。两个控制台使用同一根域的不同子域；登录状态通过身份中心 SSO 复用，业务 Cookie 和权限分别管理。当前仅提供退出当前控制台，不冒充全局退出。

开发使用 Vite 时，`origin` 和 OIDC callback 应填写真实 Vite 地址；`/api`、`/auth` 都代理至 BFF。生产直接部署构建产物与 BFF。配置文件、成员映射、身份密钥、Redis 密码均不加入 Git。
