# AgentMesh 独立控制台

两套独立服务：本目录属于 AgentMesh；SQL Sentinel 前端位于其仓库的 `web/`。各有构建、监听地址、服务端适配和配置。两者填写**同一个 Clerk 应用**的 Publishable Key、Issuer、Secret Key，成员授权文件分别维护。没有聚合产品入口。

## 本机查看

要求 Node >=22.12，执行：

```sh
cd AgentMesh/web
npm ci
npm run build
npm start
```

访问 `http://127.0.0.1:4180/`。缺少身份配置时业务 API 返回 503，页面显示配置缺口；`/?preview=1` 为公开的界面示例，数据为明确标注的样例，不调用模型、不保存业务数据，也不能绕过服务端认证。SQL 控制台默认 4181，通过侧栏互相跳转。开发时分别运行 `npm start` 和 `npm run dev`，Vite 只代理 `/api` 至当前产品的服务端适配。

## 取得共享 Clerk 配置

1. 用户自行登录 https://dashboard.clerk.com/ 并创建**一个**应用，启用邮箱验证及所需登录方式。开发阶段使用 development instance。
2. Dashboard → **API Keys**：复制 Publishable Key (`pk_test_...`)；Secret Key (`sk_test_...`) 保存到本地私有文件，禁止发到聊天、Git、浏览器配置或命令参数。无需向开发者提供个人密码、身份证或邮箱密码。
3. 同页核对实例 Frontend API 地址，作为 `clerk_issuer`（例如 `https://your-instance.clerk.accounts.dev`）。Publishable Key 必须与该实例一致，服务端启动校验这一关系。
4. 同一用户在 Dashboard → **Users** 中的稳定 `user_...` ID 用于两产品成员映射；以 `issuer + subject` 识别身份，邮箱不作为合并依据。
5. 生产使用同一根域下两个子域，例如 `mesh.example.com`、`sentinel.example.com`，按 Clerk Domains 页完成 DNS 与生产实例配置。同根域共享会话；不同根域需要另行配置 satellite，不可只复制 Key 就宣称单点登录。

官方参考：[JS 登录组件](https://clerk.com/docs/js-frontend/reference/components/authentication/sign-in)、[JWT 验证](https://clerk.com/docs/guides/sessions/manual-jwt-verification)、[生产域名](https://clerk.com/docs/guides/development/deployment/production)。真实邮箱收信、跨控制台会话及目标网络可达性尚待提供实例后联调。

## 本地配置与权限

将 `console.config.example.json` 复制到私有目录，填写以下配置。用环境变量 `CONSOLE_CONFIG_FILE` 指向该文件，然后启动；不将整个 `.env` 或管理 token 暴露给前端。

| 配置 | 说明 |
|---|---|
| `origin` | 本控制台的完整外部 origin；本机 4180，生产 HTTPS |
| `peer_url` | SQL 控制台 origin；不带令牌或用户资料 |
| `public_api_url` | 客户端接入用的真实网关地址，含 `/v1` |
| `clerk_publishable_key` / `clerk_issuer` | 两控制台共用身份实例 |
| `clerk_secret_file` | 仅服务端读取的 Clerk Secret Key 文件 |
| `members_file` | 本产品 `user_...` → 业务身份的私有 JSON |
| `backend_url` | 可信内部网关 origin，如 `http://gateway:18080` |
| `backend_admin_file` | 可选 Key 管理所需的私网管理凭据 |

`members.example.json` 给出结构。租户必须预先开通、充值并签发专用网关 Key，其文件路径写入 `gateway_key_file`；该 Key 只供控制台服务端使用。服务端会向 `/v1/account` 核对其实际租户及启用状态，防止成员文件错误串租户。按租户共享余额及 Key 元数据；`can_manage_keys` 默认 false，只有该标志与服务端管理配置同时可用时开放本租户 Key 管理。服务端拒绝浏览器提供租户覆盖值；撤销前按租户核对归属。普通成员不是平台管理员。

成员文件每次请求重读，移除或 `enabled:false` 后新请求立即拒绝。原子替换文件以避免读到半份 JSON。新注册账号默认没有任何产品授权，也不自动赠送模型费用。控制台对每个请求验证 RS256、固定 Issuer、exp/iat/sub/sid/azp、允许来源，向 Clerk 核实当前 session、用户状态及主邮箱已验证，再检查本产品授权。Clerk 不可用时拒绝业务，不退回未验证身份；这会增加身份服务请求量，当前针对小规模演示使用。正在执行的流不因退出自动退款。

## 当前界面

- 模型目录、流式文字与有界 PNG/JPEG 图片、停止接收、请求/账单关联；上下文仅保存在当前页面内存。
- 当前租户 Key 游标列表、一次性签发、按租户核实后撤销。无自助充值及平台租户管理。
- 人民币余额/冻结/最近账单、待对账区分；模块读取失败不显示零余额。
- OpenCode / Claude Code 配置模板；未配置公网地址时明确标记占位。别名需与模型目录核对，工具覆盖范围沿用后端文档。
- SQL 控制台跳转不携带凭据，SQL → 网关继续使用既有服务器专用身份。

## 测试与部署

`npm test` 使用临时文件、RSA 签名及受控 Clerk/业务服务响应，不读取个人凭据、不访问付费模型。`npm run build` 构建真实前端。测试不证明 Clerk 真实邮件、跨子域 SSO 或云端部署已通过。

Dockerfile 独立构建；可在联合部署目录使用新增 `consoles.yml` 与 `Caddyfile.consoles`。参考 `../deployments/backend-stack/CONSOLES.md`。保持管理 API、数据库和监控私网。没有执行任何 GitHub 推送、远程发布或真实充值。


## Review 后补齐（2026-10-22）

登录组件与业务 hash 路由已隔离；身份变化取消旧请求，并用身份代次保护成功和失败响应。已登录但没有产品授权时，可重新检查权限或退出换号。构建/示例不证明真实 Clerk 登录、邮件或跨子域 SSO 已验收。

页面直接适配真实 Go 报告与 RAG JSON 契约。SQL 任务从后端按项目及表授权分页读取，支持跨浏览器查询；知识文档支持当前版本列表、更新、删除，审计与历史搜索有独立入口。网关增加价格、图片资产、trace 和平台管理；`platform_admin` 必须在服务端成员文件显式设置，普通 Key 管理不等于平台管理。

客户端配置从真实目录选择模型，并检查工具能力、有效 CNY 价格、余额冻结上限和网关就绪。检查使用控制台专用凭据，不冒充新 Key 或全部 harness 功能已实测；新应用 Key 继续使用原 `scripts/client-config.py` 单独检查。所有写操作不自动重试，结果不明时先核实。

生产配置增加 `production:true`，拒绝开发 Clerk Key、非 HTTPS origin、不同父域和占位公网地址。使用 `CONSOLE_CONFIG_FILE=... npm run check:config` 校验私有配置与内部 readyz；输出不打印凭据。完整流程见 AgentMesh `deployments/backend-stack/WEB-PRODUCTION.md`；Linux 生成器 `console-setup.py` 保留原后端环境，不覆盖已有成员目录。

本轮修复与验证范围：[web-console-verification](../docs/web-console-verification.md)。
