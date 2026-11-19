---
level: L1
feature: 026-standalone-control-plane
created: 2026-11-16
status: draft
---

# S1a 单机控制面规格

**原始需求：** 在 AgentMesh 原型仓库中开辟新方向：保留现有网关数据面，增加 magpie 式单机控制面。第一阶段让用户在没有 MySQL/Redis 的机器上通过网页添加 OpenAI 兼容 Provider、发现并启用模型、获得本机 Key，并调用现有 `/v1/chat/completions` 得到 SSE。

**设计文档：** [单机控制面设计](../../docs/standalone-control-plane.zh-CN.md)
**路线图：** [中转控制面路线图](../../docs/RELAY-ROADMAP.zh-CN.md)
**决策：** [决策 005](../../decisions/005-standalone-control-plane.md)

## 1. 目标

S1a 必须形成一个可演示、可重启、可修复的本地闭环：

```text
standalone 启动
  → 管理页登录
  → 添加 Provider
  → GET /models 探测
  → 发现模型并启用
  → 复制 local Key
  → 调 /v1/chat/completions
  → 完整 SSE + [DONE]
  → 重启后仍可用
```

成功不是“有管理页面”或“能编译”，而是用户能在一台干净机器上完成上述流程。

## 2. 用户故事

### 2.1 首次初始化

作为本机用户，我希望启动一个命令后看到管理地址和一次性管理 token，这样我不需要先安装 MySQL/Redis 或配置一组环境变量。

### 2.2 添加服务商

作为本机用户，我希望在网页里填写 Provider 名称、Base URL 和 API Key，并用免费的 `/models` 探测确认连通，这样我不需要改代码或重启。

### 2.3 发现模型

作为本机用户，我希望从上游模型列表中发现模型，默认不启用，确认后手工启用，这样不会因为新模型突然产生费用或误路由。

### 2.4 调用网关

作为本机用户，我希望用管理页一次性显示的本机 Key 调用 `/v1/chat/completions`，并看到流式输出，这样我可以把 OpenCode/SDK 指向本机地址。

### 2.5 修复配置

作为本机用户，如果我保存了无效配置，我希望旧配置继续服务，管理页仍可打开并修复；如果进程重启后配置无效，我希望管理页可访问而数据面明确 503。

## 3. 范围

### 3.1 包含

- `cmd/api --standalone --data-dir ...` 启动模式。
- 独立 loopback 数据 listener 和管理 listener。
- SQLite catalog、迁移和数据目录管理。
- Provider CRUD、单凭据加密保存、模型探测。
- 模型发现、启停、stale 标记、手工字段覆盖规则。
- 一个自动生成的 `local` Key。
- `identity.Principal` 最小实现和 auth 兼容。
- 不可变 Snapshot 的构建、原子替换和 generation。
- 管理页的初始化、服务商、模型、状态四个区域。
- 修复模式和 readiness 行为。
- 单元/集成测试、文档和演示脚本。

### 3.2 不包含

- 路由组、虚拟模型、权重、跨 Provider fallback。
- 每 Key 硬限额、模型白名单、CIDR、上下文窗口筛除。
- 多设备 Key、Key 撤销页面、设备用量。
- 局域网监听、TLS、mDNS、二维码、Remote AgentMesh。
- `/v1/responses`、Codex 兼容声明。
- MySQL/Redis 双 Store、企业租户/Casdoor 改造。
- 订阅 OAuth、插件、隐私脱敏、搜索/图片/视频。
- 自动改写 Claude Code/Codex/OpenCode 配置文件。
- 公网部署、可信代理、成本上限和审计外送。

## 4. 功能需求

### 4.1 启动和监听

- `--standalone` 是必填开关；没有它时现有企业模式行为不变。
- `--data-dir` 必须可写；数据目录包含 `catalog.db`、`master.key`、`agentmesh.lock`。
- `--addr` 默认 `127.0.0.1:18080`，`--admin-addr` 默认 `127.0.0.1:18081`。
- S1a 两个地址都必须是 loopback；非 loopback 启动失败，错误码 `standalone_address_must_be_loopback`。
- 数据面不挂 `/admin`；管理面不暴露 `/v1/chat/completions`。
- 管理 listener 启动失败时进程退出。
- 支持 SIGINT/SIGTERM 优雅关闭；SSE 请求在关闭时按现有网关语义处理。

### 4.2 SQLite

- 使用纯 Go 驱动，WAL、foreign keys、busy timeout、NORMAL synchronous。
- 迁移嵌入二进制，启动时执行；失败则退出。
- 表至少包括：`providers`、`provider_credentials`、`models`、`client_keys`、`catalog_metadata`、`audit_log`、`schema_version`。
- 写入使用事务；Provider 删除为软删除；模型 stale 不自动删除。
- 数据面热路径不查 SQLite。

### 4.3 凭据

- Provider 凭据 AES-256-GCM 加密，AAD 绑定 provider/credential/version。
- 主密钥 keyring 支持 current/previous；写入使用 current。
- API 只返回 `has_credential`、掩码前缀或尾号，不返回明文/密文。
- 主密钥丢失时 Provider 标记 `credential_unreadable`，不进入运行快照；不得回退明文。
- 日志、错误和 trace 不得包含明文/密文/主密钥。

### 4.4 Provider

- 只支持 `openai_compatible`。
- 支持 bearer 和 none 两种认证。
- `auth_type=none` 不得要求或发送假 Key；对现有 Adapter 的扩展必须默认关闭，保持企业模式行为不变。
- Base URL 必须是 http/https，不含 userinfo/fragment/query；拒绝云元数据地址。
- 不跟随重定向；模型探测超时 5 秒，chat 测试超时 20 秒；响应体上限 1 MiB。
- 保存 Provider 不自动发起付费 chat。
- 测试分 `models` 和显式 `chat` 两级；chat 必须 `confirm=true` 且 `max_tokens=16`。

### 4.5 模型

- 发现使用 `GET {base_url}/models`。
- 公共 ID 为 `providerSlug/upstreamModelID`；只按第一个 `/` 分割。
- Provider `slug` 创建后不可修改，保证公共模型 ID 稳定。
- 新发现模型默认 disabled。
- 新发现模型的 `tools`/`vision` 默认 false；只有管理员手工标记后才允许对应输入，不能根据模型名猜测能力。
- 手工字段不被发现覆盖。
- 上游消失只标 stale，不删除；stale 不参与路由。
- 允许手工创建模型，至少在 S1a 支持 PATCH 启停和能力/窗口字段。

### 4.6 Snapshot

- Snapshot 不可变，至少包含 generation、Provider 视图、模型路由和本地 Key 元数据。
- 每个 Provider 使用唯一 `Name()`；每个启用模型使用单条 alias 的子 Adapter，公共模型 ID 通过包装层映射到该 alias。
- 发现但禁用的模型不构建 Adapter；单 Provider 启用模型数设有上限，避免 Snapshot 过大。
- 管理写入在事务内编译候选 Snapshot；失败回滚，不替换旧快照。
- 成功后 `atomic.Pointer` 替换；旧请求继续使用旧 Snapshot。
- Snapshot 不持有可变 quota/cooldown 状态；运行状态按稳定 `laneKey` 存在进程级 runtime。
- 管理状态和 trace 暴露 generation。

### 4.7 身份和 Key

- `internal/identity` 定义 `admin`、`local`、`app_key` 三种 Kind。
- S1a 只实际生成一个 `local` Key，拥有 `chat.write` 和 `models.read`。
- 数据面拒绝 `admin`；管理面拒绝 `local`/`app_key`。
- 管理 token 只存哈希，只打印一次。
- local Key 只存哈希，只在初始化页展示一次；`--rotate-local-key` 可轮换。
- S1a 不解析 `X-Forwarded-For`/`X-Real-IP`。

### 4.8 管理页面

- 初始化/状态、服务商、模型、运行状态四个区域。
- 登录使用 admin token 换 HttpOnly + SameSite=Strict cookie；修改请求带 CSRF。
- 不使用 localStorage 保存 token。
- 静态资源本地提供，CSP 限制 `default-src 'self'`。
- 不显示明文凭据、密文、admin token 或 local Key（除初始化一次性展示）。

### 4.9 数据面

- S1a 合同：`/healthz`、`/readyz`、`/v1/models`、`/v1/chat/completions`。
- 公共模型 ID 含 `/`，不把旧 `/v1/models/{model}` 单段路由纳入合同；需要详情时使用多段 wildcard 或 query。
- `/v1/chat/completions` 使用现有 SSE/错误映射；成功流以 `[DONE]` 结束。
- 模型必须显式 `providerSlug/upstreamModelID`；disabled/stale 返回明确错误。
- S1a 没有跨 Provider fallback；上游失败直接映射。
- standalone 不启动 embeddings、images、billing、企业 admin 和 Casdoor 路径。

## 5. 管理 API 合同

| 方法 | 路径 | 关键约束 |
| --- | --- | --- |
| `POST` | `/admin/session` | token 换 cookie + CSRF；登录限速 |
| `DELETE` | `/admin/session` | 注销 |
| `GET` | `/admin/state` | 返回 generation/ready/repair/计数/脱敏错误 |
| `GET` | `/admin/providers` | 不返回秘密 |
| `POST` | `/admin/providers` | 校验 slug/base_url/auth/credential |
| `GET` | `/admin/providers/{id}` | 只返回掩码 |
| `PATCH` | `/admin/providers/{id}` | 支持 revision 冲突和密钥轮换 |
| `DELETE` | `/admin/providers/{id}` | 软删除 |
| `POST` | `/admin/providers/{id}/test` | models 或显式 chat |
| `POST` | `/admin/providers/{id}/discover` | 发现并写目录 |
| `GET` | `/admin/models` | 支持 provider/enabled/stale 过滤 |
| `POST` | `/admin/models` | 手工创建模型，适用于无 `/models` 的上游 |
| `PATCH` | `/admin/models/{id}` | 启停、能力、窗口 |

错误形状固定：

```json
{
  "error": {
    "code": "stable_code",
    "message": "stable_code"
  }
}
```

所有管理响应 `Cache-Control: no-store`。

## 6. 状态和启动行为

| 状态 | 管理面 | 数据面 `/healthz` | 数据面 `/readyz` | `/v1/*` |
| --- | --- | --- | --- | --- |
| 正常 | 可访问 | 200 | 200 | 正常 |
| 空 catalog | 可访问/引导 | 200 | 503 | 503 |
| Snapshot 编译失败 | 可访问/修复 | 200 | 503 | 503 |
| SQLite/迁移失败 | 不启动 | 不适用 | 不适用 | 不适用 |
| 部分凭据不可读 | 可访问/提示 | 200 | 至少一个可用 Provider/模型时 200，否则 503 | 受影响模型不可路由 |

数据面 `/readyz` 只有在 Snapshot 存在且有可用 Provider/模型时才返回 200。管理面不通过公网暴露。

## 7. 安全需求

- 两个 listener 默认且 S1a 强制 loopback。
- 管理 API 先认证后读 body；登录限速。
- Cookie HttpOnly、SameSite=Strict、Path=/admin；S1a loopback HTTP 不设 Secure，S4 TLS 后必须设。
- 修改请求校验 Origin 和 CSRF。
- Provider 请求不跟随重定向，限制响应大小和超时。
- Base URL 不允许 userinfo/fragment/query；拒绝云元数据地址。
- 凭据加密，API/日志/错误不泄露秘密。
- 管理 token/local key 不出现在 URL query。
- 日志不记录 Authorization、Cookie、请求正文、上游正文、Provider Key、密文、主密钥。

## 8. 验收

### 8.1 必须通过

1. 无 MySQL/Redis 的临时数据目录启动 standalone。
2. 终端显示管理地址和一次性 admin token。
3. 管理页添加一个 OpenAI 兼容 Provider。
4. `/models` 探测成功，发现模型默认禁用。
5. 启用模型后，管理页显示一次 local Key。
6. `curl`/SDK 调 `/v1/chat/completions`，收到完整 SSE 和 `[DONE]`。
7. 重启后配置/Key 仍可用。
8. 无效配置保存返回 422；旧 generation 和旧模型继续服务。
9. 修复模式：管理面可访问；数据面 healthz 200、readyz 503；修复后恢复。
10. 日志和数据库导出没有明文/密文 Key。

### 8.2 自动测试

- `internal/catalog`：迁移、事务、stale、软删除、Snapshot 构建/替换。
- `internal/secrets`：AES-GCM、AAD、keyring current/previous、不可读错误。
- `internal/identity`：Kind/Scope、context、admin/local 隔离。
- `internal/auth`：local key 哈希、常量时间、PrincipalStore 兼容。
- `internal/control`：cookie、CSRF、Origin、登录限速、错误脱敏。
- `internal/gateway`：mock upstream SSE、`[DONE]`、模型错误、无 fallback。
- `cmd/api` 集成：临时数据目录、两个 listener、重启持久化、修复模式。

### 8.3 验证命令

```powershell
gofmt -l .
go vet ./...
go test -count=1 ./...
go build ./...
git diff --check
```

Windows 全量测试若因链接内存不足失败，必须记录失败包和其余结果；不能写成“全量通过”。Linux/CI race 在 S0 或实现阶段单独执行。

## 9. 实施边界

- 不改现有企业模式的默认行为。
- 不把免费池未提交改动合入本分支。
- 不在 S1a 把 `/v1/messages`、embeddings、images、billing 纳入对外能力声明。
- 不在 S1a 实现路由组、限额、Responses、局域网或公网。
- 不提交、推送、创建 PR/标签/Release，除非用户另行明确授权。

## 10. 依赖和后续

- 依赖 `master` 现有的 gateway/router/provider/auth 契约。
- 需要新增 SQLite 驱动；实现时再更新 `go.mod`/`go.sum`。
- S1b 在 S1a 的 Principal 和 Snapshot 上增加多 Key/归属/状态页。
- S2 才引入每 Key 限额、上下文窗口、路由组和 fallback。
- S2.5 视 Codex 是否为核心演示决定是否提前。

## 11. 待确认

1. 目标用户是否只包含个人/小型团队，还是从一开始就要公网多租户。
2. Codex 是否作为 S1/S2 的核心验收；若不是，UI 必须明确“暂不支持 Codex”。
3. 管理 UI 复用现有 `web` 还是新建 `web/admin`；两者都必须单二进制可运行。
4. 免费池是否进入主线；本分支当前不依赖它。
