> 当前生产部署入口：[WEB-PRODUCTION.md](WEB-PRODUCTION.md)。本文件保留最初接入说明，后续验证以当前提交的验证记录为准。

# 两套独立控制台：共享 Clerk 身份

前端源码分别位于 `AgentMesh/web`、`sql-sentinel/web`，各自构建及运行。接口代理是每个控制台自己的 Node 服务，现有 Go 后端协议保持兼容。此文仅描述配置和本机证据，真实 Clerk、云域名和容器运行必须另行核实。

## 目录与配置

两仓库及导出的 `deploy` 同级。在部署目录准备以下私有文件，Linux 目录 0700，容器运行 UID 1000 只读访问自己的目录；按实际 owner/group 授予最小读取权限，避免 0777。不要把这些文件放在 web/dist 中。

```text
deploy/console-private/
  mesh/
    config.json
    members.json
    clerk_secret
    admin_token         # 只有启用租户 Key 管理时才需要
    gateway_key         # 对应已有租户的专用网关 Key
  sentinel/
    config.json
    members.json
    clerk_secret        # 与 mesh 的同一个 Clerk 应用
    sentinel_token      # SQL bootstrap token，只在服务端
```

分别复制两个 `web/console.config.example.json`，在容器内配置所有私有文件路径到 `/run/console-private/...`。mesh 的 backend_admin_file 指向 admin_token；sentinel 的指向 sentinel_token。members_file 指向 members.json。`backend_url` 分别为 `http://gateway:18080`、`http://sentinel-api:8080`。mesh 成员中的 gateway_key_file 指向 gateway_key。

两份 config 使用相同 Clerk Publishable Key、Issuer 及 Secret Key，但 origin、peer_url 和 members_file 不同。建议同根域子域 `mesh.example.com`、`sentinel.example.com`。不要把开发实例会话测试写成生产 SSO 已通过；不同根域需按 Clerk satellite 文档另外配置。

在 Clerk Users 页获取已验证用户的 `user_...`，分别写入两份 members.json，示例见各仓库 web/members.example.json。默认注册不发模型额度、不自动授予另一产品权限。网关租户、其余额与专用 Key 由现有管理流程创建；SQL 分配具体 tenant/project/role/tables。普通 writer 不能审批自己任务，需另一 reviewer 身份或明确管理员角色。用 `enabled:false` 撤销该产品访问，独立于另一产品；Clerk 会话退出/撤销则两产品新请求均拒绝。

## 部署

沿用现有项目名、源码和已配置业务服务。新增环境变量 MESH_CONSOLE_DOMAIN、SENTINEL_CONSOLE_DOMAIN，并保持 APP_DOMAIN 为原 API 域名；三个域名不能相同。使用两个控制台同根域方案。

```sh
docker compose -f compose.yml -f consoles.yml config --quiet
docker compose -f compose.yml -f consoles.yml build mesh-console sentinel-console
docker compose -f compose.yml -f consoles.yml up -d mesh-console sentinel-console
docker compose -f compose.yml -f consoles.yml up -d proxy
docker compose exec -T proxy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker compose exec -T proxy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

若既有部署使用 qwen.yml、qwen-billing.yml 或 extensions.yml，**保留原有文件组合及 profile**，最后再加 `-f consoles.yml`，不要用上面的简写替换整套已运行配置。首次迁移仍遵循后端手册。Caddyfile.consoles 保留 API 的私网管理拦截，新增两个主机路由；未直接开放 Node、数据库、监控和管理 API 的公网端口。

当前环境 Docker daemon 未运行，本次未把 Docker 配置或构建成功当作容器运行验收。已验证 Node 本机两服务、生产前端构建及受控身份/业务代理测试。没有真实 Clerk 配置，所以邮箱注册、跨子域会话及与当前 Go 服务的完整浏览器联调尚未执行。已有后端历史验收不替代这次前端真实联调。

## 验证范围

- 各仓库 web 内运行 `npm ci`、`npm test`、`npm run build`。
- 自动测试使用隔离 RSA/JWKS、受控 Clerk 会话/用户响应、真实 localhost HTTP 业务夹具；不会读取真实 Key 或调用模型。
- 浏览器预览有显著示例标记，真实登录未配置时 API 默认拒绝。前端示例不放宽服务端认证。
- 生产首次联调应确认同账号两产品独立授权、未验证邮箱拒绝、禁用/退出、账单与报告关联。此处是未来配置流程，不声称已完成。
- 前端 Clerk SDK 为延迟加载，约 1.53 MB 未压缩；首次实际登录有该加载成本。构建保留体积告警，未通过放宽阈值隐藏。

服务端业务路由采取明确白名单，不提供任意 URL 代理。浏览器请求不会转发其 Authorization 给业务服务；只有验证后的身份映射能选择服务端保存的凭据。后台不会自动重试有副作用的业务请求。
