# 决策 007：本机宿主、订阅与隐私控制面边界

**状态：** Accepted
**日期：** 2026-11-18
**适用阶段：** S3.5a-S3.5e、S3.6
**相关文档：** [S3.5 设计](../docs/S3.5-local-host-privacy.zh-CN.md)、[027 规格](../specs/027-local-host-privacy/spec.md)

## 背景

AgentMesh 已有独立的单机控制面、设备 Key、路由组、用量、Responses 子集、Remote AgentMesh 和本地边界模拟。下一阶段希望把本机 Agent、本地模型服务和用户自有订阅来源纳入统一目录，同时不把网关重写成透明代理或操作系统沙箱。

本决策冻结后续实现必须遵守的产品和安全边界。

## 决策

### 1. 分阶段交付

S3.5 拆成 S3.5a-S3.5e，备份硬化单独列为 S3.6。任何阶段不能以“大版本一次交付”为由跳过验收。

### 2. Agent 是客户端，不是 Provider

本机安装的 Codex、Claude Code、Gemini CLI、OpenCode 等只作为宿主客户端被只读发现。只有存在可验证的模型调用协议时，对应的 API Provider、本地模型服务或订阅适配器才进入 Provider/Lane 体系。

### 3. 扫描默认只读

扫描只使用已知目录、PATH 和用户显式添加的根目录：

- 不扫描整个磁盘。
- 不读取浏览器 Cookie、密码库或无关键文件。
- 不执行扫描到的二进制。
- 不跟随 symlink、Windows junction 或 reparse point。
- 不自动修改 Agent 配置；配置接入先生成可复制片段。

### 4. 订阅账号是默认关闭的实验适配器

- `--enable-subscription-adapters` 默认 false。
- 只允许用户本机显式导入。
- 默认 `local_only`，禁止局域网、公网、账号共享、转售和绕过上游额度。
- 默认不复制 refresh token；必须明确外部引用和受管密文的边界。
- 必须有刷新、撤销、失败冷却和上游条款/风控提示。
- 没有完成条款核验和稳定撤销路径前，不把订阅适配器作为默认卖点。

### 5. 订阅账号与 Provider 的 MVP 映射

一个订阅账号对应一个 Provider。这样可复用现有：

- 单 Provider 凭据；
- 模型目录；
- Provider/model 冷却；
- 路由组和首包前 fallback；
- 用量和审计。

公共模型 ID 固定为 `<provider-slug>/<upstream-model>`。Provider 显示名称和账号标签不得改变 slug、模型 ID、凭据 ID 或历史用量归属。

### 6. 配置写入必须可证明安全

自动写入最多先支持一个 Agent Adapter，并且必须同时满足：

- 用户可见的 `plan_id`、diff 和备份。
- `expected_hash` 和显式确认。
- 目标路径 allowlist。
- 拒绝 symlink、junction、reparse point。
- 保留 ACL、所有者和权限。
- 检测目标 Agent 是否运行。
- 配置快照加密。
- 写入失败或 hash 漂移时不覆盖原文件。
- rollback 只在当前文件仍等于 apply 后 hash 时允许。

### 7. 隐私层是 best-effort 本地保护

- 默认 `off`。
- `standard`/`strict` 只处理 JSON 字符串值，不修改字段名、工具 schema 或 JSON 结构。
- 处理 Chat 文本 delta、tool call arguments 和 Responses 事件。
- 占位符拆包使用有界缓冲。
- `standard` 无法还原时保持 masked；`strict` 失败关闭。
- 日志、trace、audit、备份不记录原值。
- 开启脱敏后绕过语义缓存。
- 不宣称为操作系统沙箱或完整 DLP。

### 8. 备份和恢复

- 当前明文目录备份保留为兼容格式，不悄悄改变语义。
- 新的 `.agmbackup` 必须版本化、认证加密、支持离线恢复、预检、完整性校验和原子替换。
- 错误密码、篡改、截断、磁盘不足和恢复中断必须测试。
- 自动配置写入和恢复前必须补齐 `agentmesh.lock` 的进程级单写者锁。

### 9. 局域网与公网

- 真实局域网必须显式启用 `AllowLANListen`。
- 管理面继续只绑定 loopback。
- `local_only` 必须在 Snapshot/路由层强制执行，不能依赖 Key 名称或 UI 隐藏。
- S3.5e 必须用同网两台真实设备验收；loopback/httptest 只能算模拟。
- 公网、真实域名、Caddy/Nginx、多副本和生产 TLS 部署仍属于后续阶段，不能由本地模拟推导为已发布能力。

## 后果

- S3.5a 不引入订阅、配置自动写入或隐私脱敏。
- S3.5a 只新增最小宿主扫描和本地模型目录能力。
- 订阅与自动配置写入各自需要独立规格、威胁模型和验收记录。
- S3.5 开发承接现有控制面工作，集成分支为 `codex/local-host-privacy`。免费池继续使用独立工作区。
