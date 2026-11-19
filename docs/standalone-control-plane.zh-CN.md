# AgentMesh 单机控制面设计

**状态：** S1a 设计基线（2026-11-16）。S1a、S1b、S2、S2.5、S3 和 S4 本地模拟已有实现与阶段验收；本文保留初始设计边界，实际能力以验收记录和自动测试为准。
**适用范围：** `--standalone` 单机模式。
**相关文档：** [中转控制面路线图](RELAY-ROADMAP.zh-CN.md)、[决策 005](../decisions/005-standalone-control-plane.md)、[S1a 规格](../specs/026-standalone-control-plane/spec.md)。

## 1. 文档目的

本文把 S1a 的技术边界写到可以直接拆任务的程度：

- 进程如何启动、监听哪些地址。
- 管理员和本机调用 Key 如何认证。
- SQLite 中存什么，不存什么。
- Provider、凭据和模型如何进入不可变 Snapshot。
- 管理 API 和数据面 API 的契约。
- 启动失败、配置错误和修复模式如何表现。
- 哪些能力明确不在 S1a。

本文不改变现有企业模式。没有 `--standalone` 时，`cmd/api` 继续走现有 MySQL/Redis/Casdoor 路径。

## 2. 与现有代码的关系

### 2.1 复用

- `internal/gateway`：HTTP 路由、Chat Completions、SSE、错误归一化和流式输出。
- `internal/router`：首包前尝试、首包后不拼接不同模型的既有语义。
- `internal/provider` 与现有 OpenAI 兼容 Adapter：上游协议和工具/图片能力判断。
- `internal/auth` 的 Bearer 解析、并发限制和失败错误形状。
- `internal/observability` 的 trace/会话记录能力（不暴露上游正文）。
- 现有 `web` 的 Vite 构建链和视觉基础；S1a 只增加管理页面，不改写产品控制台。
- `internal/tenant` 的兼容接口：S1a 新增 catalog resolver 实现 `TenantResolver`，不把 Provider 名称硬编码进新路径。

### 2.2 新增

| 包/入口 | 作用 |
| --- | --- |
| `internal/identity` | Principal、Kind、Scope 和 context 存取；不依赖 auth/gateway/reservation |
| `internal/catalog` | SQLite schema、Store、模型目录、Provider 视图、Snapshot 构建 |
| `internal/secrets`（或 `internal/catalog/secrets`） | AES-256-GCM 凭据加密、keyring、掩码 |
| `internal/control` | 管理 listener、session、CSRF、管理 API 和静态页面 |
| `cmd/api` standalone 组合 | 打开数据目录、构建 Snapshot、启动两个 listener |

### 2.3 暂不修改

- `internal/reservation` 的现有 MySQL 语义。
- `internal/billing` 的现有账本语义。
- `internal/tenant.MySQLLifecycle` 的企业租户管理。
- `internal/freepool` 的未提交实现。
- 现有 `/v1/messages`、图片、embedding 和计费路由；standalone 不把它们纳入 S1a 合同。

## 3. 进程与监听面

### 3.1 CLI

```text
agentmesh --standalone
          --data-dir <dir>
          [--addr 127.0.0.1:18080]
          [--admin-addr 127.0.0.1:18081]
          [--reset-admin-token]
          [--rotate-local-key]
```

说明：

- `--standalone` 必填；没有它时保持现有企业模式。
- `--data-dir` 必填或使用明确的默认 `.agentmesh`；文档和演示使用显式目录。
- `--addr` 和 `--admin-addr` 在 S1a 都必须是 loopback 地址；`0.0.0.0`、RFC1918 和公网地址一律拒绝。
- 两个地址不能相同。
- `--reset-admin-token` 和 `--rotate-local-key` 是本地维护命令：校验数据目录、执行变更、打印一次性结果后退出，不启动 listener。
- S1a 不读取 `--providers` 来选择 standalone Provider；如果显式传入，返回 `standalone_flag_conflict`，避免两套配置来源混淆。

### 3.2 首次启动输出

首次启动只打印必要信息：

```text
AgentMesh standalone
data dir: C:\...\data
data plane: http://127.0.0.1:18080/v1
admin: http://127.0.0.1:18081
admin token: aga_... (shown once; store securely)
local key: available once in the admin onboarding page
```

管理 token 只打印一次，数据库只保存 SHA-256。原始 `local` Key 不进入日志，由管理页初始化面板在进程存活期间展示一次；如果尚未复制就重启，使用 `--rotate-local-key` 生成新 Key。

### 3.3 生命周期

```text
parse flags
  → validate loopback listeners
  → open data dir and acquire lock
  → open SQLite + run migrations
  → load/create master key
  → load/create admin token hash
  → load/create local key hash
  → compile initial Snapshot
  → start admin listener
  → start data listener
  → wait for SIGINT/SIGTERM
  → graceful shutdown both listeners
  → close SQLite
```

管理 listener 必须先于数据 listener 启动。管理 listener 绑定失败时进程退出，不能以“数据面仍可用但没有管理入口”的状态继续运行。

### 3.4 数据目录

```text
<data-dir>/
  catalog.db
  master.key
  agentmesh.lock
```

- `catalog.db`：SQLite 数据库。
- `master.key`：keyring JSON，权限尽量限制为 0600；Windows 上还要在文档中提示 ACL 风险。
- `agentmesh.lock`：防止同一数据目录被两个进程同时写入。
- 数据目录不能位于 Git 工作区中被提交的位置；后续实现把默认目录加入 `.gitignore`。

## 4. 身份与控制面权限

### 4.1 Principal

```go
package identity

type Kind string

const (
    KindAdmin  Kind = "admin"
    KindLocal  Kind = "local"
    KindAppKey Kind = "app_key"
)

type Principal struct {
    Kind      Kind
    SubjectID string
    TenantID  string
    KeyID     string
    Scopes    map[string]bool
}
```

约束：

- `identity` 不导入 `auth`、`gateway`、`reservation`、`catalog`，避免循环依赖。
- 数据面只接受 `local` 和 `app_key`；`admin` 在数据面一律 401。
- 管理面只接受 `admin`；`local`/`app_key` 不能进入管理 API。
- S1a 的 `local` Key 至少拥有 `chat.write` 和 `models.read`。
- S1b 再扩展 `app_key` 的创建、撤销、白名单和限额字段。

### 4.2 兼容现有鉴权

`internal/auth` 新增一个独立入口，避免把 catalog Store 强行实现成旧 `tenant.Store`：

```go
type PrincipalStore interface {
    AuthenticatePrincipal(context.Context, string, [sha256.Size]byte) (identity.Principal, bool, error)
}

func AuthenticateWithPrincipalStore(store PrincipalStore, next http.Handler) http.Handler
```

现有 `auth.Authenticate(tenant.Store, next)` 保持签名和行为，内部可以走兼容包装。standalone 使用新入口；Principal 写入 context 后，`auth.TenantID(ctx)` 保留为兼容包装，从 Principal 的 `TenantID` 返回值。

这样 S1a 不改现有 reservation/billing 的签名，但所有新请求已经有 Principal；S2 再把 Principal 正式传入配额和路由组。

### 4.3 管理员登录

- 管理 token 格式：`aga_` + 32 字节随机数的 base64url。
- 数据库存 SHA-256，不存明文。
- `POST /admin/session` 接收 `{ "token": "..." }`，成功后在内存创建 session，返回 CSRF token，并设置：

```text
Set-Cookie: agentmesh_admin_session=<random>;
            HttpOnly; SameSite=Strict; Path=/admin; Max-Age=43200
```

- S1a 管理面是 loopback HTTP，因此 cookie 不设 `Secure`；S4 使用 TLS 后再设 `Secure`。
- session 只存内存，进程重启后全部失效。
- 登录失败按 IP/进程做小额度速率限制，返回 `admin_rate_limited`。
- 所有修改类请求要求：
  - 同源 `Origin`；缺失时只允许无浏览器的本地测试，且必须有 CSRF。
  - `X-CSRF-Token` 与 session 绑定。
- 不使用 `localStorage` 保存 admin token、local key 或 CSRF。
- 管理响应统一 `Cache-Control: no-store`。

### 4.4 本机调用 Key

- 格式：`agl_` + 32 字节随机数的 base64url。
- 数据库保存前缀（至少前 8 字符用于索引）和 SHA-256；比较使用常量时间。
- 首次 standalone 初始化生成一个 `local` Key，绑定租户 `local`。
- 原始 Key 只返回一次；之后列表只显示 `agl_abcd…` 和 KeyID。
- `--rotate-local-key` 会禁用旧 Key、生成新 Key，并打印新 Key 一次。
- S1a 不提供通用 Key 管理 API；多 Key、撤销和设备名在 S1b。

### 4.5 不信任代理

S1a 只使用 TCP `RemoteAddr` 作为来源地址，不解析 `X-Forwarded-For`、`X-Real-IP` 或 `Forwarded`。CIDR 白名单、可信代理和公网客户端 IP 在 S4 引入。

## 5. SQLite 数据模型

### 5.1 连接参数

使用纯 Go SQLite 驱动（计划 `modernc.org/sqlite`），避免 Windows 上的 CGO 构建问题。连接初始化：

```text
journal_mode=WAL
foreign_keys=ON
busy_timeout=5000
synchronous=NORMAL
```

Catalog 写入量很小，建议单写连接（`MaxOpenConns(1)`）加短事务；读请求主要读快照，不查数据库。迁移文件嵌入二进制，启动时按版本顺序执行。

### 5.2 表

```sql
CREATE TABLE schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE catalog_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE providers (
    id                 TEXT PRIMARY KEY,
    slug               TEXT NOT NULL,
    name               TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('openai_compatible')),
    base_url           TEXT NOT NULL,
    auth_type          TEXT NOT NULL CHECK (auth_type IN ('none', 'bearer')),
    enabled            INTEGER NOT NULL DEFAULT 1,
    connect_timeout_ms INTEGER NOT NULL,
    request_timeout_ms INTEGER NOT NULL,
    revision           INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    deleted_at         TEXT
);

CREATE TABLE provider_credentials (
    id          TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    prefix      TEXT NOT NULL,
    ciphertext  BLOB NOT NULL,
    key_version INTEGER NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(provider_id, label)
);

CREATE TABLE models (
    id                TEXT PRIMARY KEY,
    provider_id       TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    upstream_id       TEXT NOT NULL,
    display_name      TEXT NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 0,
    source            TEXT NOT NULL CHECK (source IN ('discovered', 'manual', 'learned')),
    confidence        REAL,
    caps_json         TEXT NOT NULL DEFAULT '{}',
    context_window    INTEGER,
    max_output_tokens INTEGER,
    stale             INTEGER NOT NULL DEFAULT 0,
    discovered_at     TEXT,
    updated_at        TEXT NOT NULL,
    UNIQUE(provider_id, upstream_id)
);

CREATE TABLE client_keys (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL UNIQUE,
    hash         BLOB NOT NULL,
    scopes_json  TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id   TEXT,
    outcome     TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX models_provider_idx ON models(provider_id);
CREATE INDEX client_keys_prefix_idx ON client_keys(prefix);
CREATE UNIQUE INDEX providers_slug_active ON providers(slug) WHERE deleted_at IS NULL;
```

### 5.3 数据规则

- Provider `slug` 使用小写字母、数字和连字符，作为公共模型 ID 的前缀；软删除后允许重新使用同一 slug。
- `slug` 创建后不可修改，避免公共模型 ID 和客户端配置静默失效。
- `base_url` 保存时去掉尾部 `/`，必须是 `http`/`https`，不允许 URL userinfo、fragment 或 query。用户需要填写包含版本路径的 Base URL，例如 `https://api.groq.com/openai/v1`。
- `auth_type=none` 时不能有凭据；`auth_type=bearer` 时必须有一个 enabled 凭据（S1a 每 Provider 最多一个 enabled 凭据）。
- 新发现的模型 `enabled=0`；手工创建的模型可以默认启用，但必须在 UI 中明确标记为手工来源。
- 上游发现不再返回某个模型时，只把 `stale=1`，不删除、不覆盖手工字段；stale 模型不参与路由。
- `source=manual` 的展示名、能力、窗口和启用状态不被自动发现覆盖。
- `models` 的公共 ID 由 `provider.slug + "/" + upstream_id` 计算；`upstream_id` 可以包含 `/`，解析时只按第一个 `/` 分割。
- S1a 的 `caps_json` 至少支持 `chat`、`tools`、`vision`。发现结果无法证明工具/图片能力，因此默认 `chat=true, tools=false, vision=false`；管理员可在模型页手工启用。请求带 tools/images 但模型未标记对应能力时返回 `unsupported_model_input`，不能静默假设支持。
- `client_keys` 的 `tenant_id` 在 S1a 固定为 `local`；S1b 才扩展设备名和多 Key。
- `audit_log` 只记录对象 ID、动作、结果和脱敏摘要，不记录请求正文、上游 Key、密文或完整 URL query。

### 5.4 迁移

- 使用 `schema_version` 记录版本，迁移在一个事务内执行。
- 迁移失败时数据库保持在失败前的可用版本，进程不启动数据面。
- 不在 S1a 实现破坏性降级；代码回滚前必须先确认 schema 兼容。
- 迁移前备份策略：停止进程后复制 `catalog.db` 和 `master.key`；只有数据库备份不能恢复加密 Key。

## 6. 秘密管理

### 6.1 主密钥文件

`master.key` 是 keyring JSON：

```json
{
  "current": {
    "version": 1,
    "key": "base64-32-bytes"
  },
  "previous": []
}
```

- 首次运行用 `crypto/rand` 生成 32 字节，写入 `current`。
- 解密先试 `current`，再试 `previous`；写入新密文时始终用 `current`。
- 密钥轮换是后续命令，但格式从 S1a 就支持 previous。
- 主密钥丢失时，已有凭据标记 `unreadable`，对应 Provider 不进入运行快照，管理页要求重新录入；绝不回退明文。
- 日志、错误和 trace 不包含主密钥、数据库密文或解密后的密钥。
- 文档明确：数据库和主密钥必须分开备份，且都不要提交到 Git。

### 6.2 凭据密文

- 算法：AES-256-GCM。
- 密文格式：`v1:<base64(nonce || ciphertext || tag)>`。
- AAD：`agentmesh:v1:<provider_id>:<credential_id>:<key_version>`。
- 每条凭据独立 nonce。
- 数据库只存密文和掩码前缀；API 列表返回 `has_credential`、`credential_prefix`，不返回明文或密文。
- Provider PATCH 带新密钥时，保持 `credential_id` 不变，递增版本并重加密；这为 S2 的冷却状态迁移保留稳定身份。

### 6.3 日志与错误

禁止记录：

- `Authorization`、`X-Api-Key`、Cookie、CSRF token。
- Provider API Key、密文、主密钥。
- 原始请求 body、聊天正文、工具参数。
- 上游响应 body；只记录状态码、稳定错误码、延迟和脱敏的 Provider/模型 ID。

建议的错误码：`provider_credential_missing`、`provider_credential_unreadable`、`provider_url_invalid`、`provider_test_failed`、`catalog_snapshot_invalid`、`admin_auth_failed`、`admin_csrf_failed`、`model_not_found`、`model_disabled`、`model_stale`、`upstream_timeout`、`upstream_unavailable`。

## 7. Snapshot 生命周期

### 7.1 结构

```go
type Snapshot struct {
    Generation uint64
    Providers  map[string]ProviderView // provider ID
    Models     map[string]ModelRoute   // 公共 provider/model ID
    LocalKeys  map[string]KeyView
}

type ProviderView struct {
    ID            string
    Slug          string
    Name          string
    Adapter       provider.Provider
    BaseURL       string
    Enabled       bool
    CredentialID  string
}

type ModelRoute struct {
    PublicID       string
    ProviderID     string
    UpstreamID     string
    Enabled        bool
    Stale          bool
    Caps           ModelCaps
    ContextWindow  int
    MaxOutput      int
}
```

Snapshot 是只读对象。不要在 Snapshot 中放：

- `*freepool.Pool` 或其他包含可变计数器的池。
- 上次请求的错误、冷却截止时间、每分钟 token 计数。
- 可被管理请求原地修改的 map/slice。

这些运行状态放在进程级 `runtimeState`，以稳定 `laneKey` 为键：

```text
laneKey = sha256(
    providerID + "\n" +
    upstreamModelID + "\n" +
    credentialID + "\n" +
    normalizedBaseURL
)
```

S1a 还不做冷却和限额，但先使用该键记录测试结果/健康状态，避免 S2 重新换身份。

### 7.1.1 Provider Adapter 包装

现有 `internal/provider/ark` 的 `Name()` 只接受 `ark` 或 `compatible`，且其 `Models` alias 不允许 `/`。S1a 不能把多个 Provider 都命名为 `compatible`，也不能把公共 ID 直接塞进旧 alias 表。

Snapshot 构建时采用一个小包装层：

- 每个**已启用模型**构造一个现有 OpenAI 兼容 Adapter；该 Adapter 的单条 alias 固定为 `m0`，映射到 `upstream_id`。
- 模型能力（tools/vision）写入同一个子 Adapter 的 `ToolModels`/`VisionModels`，避免旧 Adapter 在内部再次拒绝工具或图片。
- 一个 Provider 下只构建已启用模型；发现但禁用的模型只留在数据库。S1a 对单 Provider 的启用模型数设上限（建议 128），避免 Snapshot 过大。
- 包装 Provider 的 `Name()` 返回稳定的 `providerID` 或 `slug`，确保健康检查和日志能区分不同 Provider。
- 包装层维护 `publicID -> 子 Adapter` 映射，并实现 `SupportsTools(publicID)`、`SupportsImages(publicID)` 和 `BillingModel(publicID)`。
- `Stream` 只在该 Provider 内把 `request.Model` 换成子 Adapter 的 `m0` alias，再委托。
- 不修改 Tenant 的硬编码 Provider 名单；新路径由 catalog resolver 直接提供 `TenantResolver`。

这样不会改变旧 Adapter 的协议行为，也不会撞上旧 Adapter 的 32 模型 alias 上限，同时保留工具/图片能力判断。

### 7.2 构建与替换

管理写入流程：

```text
begin SQLite transaction
  → validate mutation
  → write row(s)
  → read full candidate catalog
  → decrypt credentials and build provider adapters
  → validate public model IDs and duplicate slugs
  → compile candidate Snapshot
  → if any enabled provider fails: rollback
commit
  → swap snapshot atomically
  → increment persisted/audit revision metadata
```

要点：

- 构建 Snapshot 期间不发起网络请求；Provider 测试是独立操作。
- 旧请求在替换后继续持有旧 Snapshot，可以完成正在进行的 SSE。
- 旧 Snapshot 不会因为管理页删除 Provider 而立即失效；新请求才使用新 Generation。
- 每次成功替换增加 `Generation`；管理页和状态接口返回该值，便于判断配置是否真的生效。
- 不要在数据面热路径查 SQLite。

### 7.3 启动失败与修复模式

- SQLite 打不开、迁移失败、数据目录不可写：直接失败退出；没有可修复的管理面。
- SQLite 正常，但 Snapshot 编译失败（例如主密钥不匹配、所有 Provider 凭据不可读、公共模型 ID 冲突）：
  - 管理 listener 照常启动；
  - 数据 listener 的 `/healthz` 返回 200；
  - 数据 listener 的 `/readyz` 和 `/v1/*` 返回 503；
  - 管理页显示 `repair_required=true` 和脱敏错误列表；
  - 修复配置后重新编译 Snapshot，数据面自动变为 ready。
- 空 catalog 不是崩溃：管理页进入初始化引导，数据面 `/readyz` 为 503，直到至少有一个启用的 Provider 和模型。

## 8. Provider 生命周期

### 8.1 S1a 支持的 Provider

只支持一种 `kind`：`openai_compatible`。

- `GET {base_url}/models`
- `POST {base_url}/chat/completions`
- Bearer 或无认证（本地 Ollama/兼容服务）
- 不配置自定义代理、TLS 跳过校验或任意请求头。

现有 `ark.Adapter` 要求 API Key 非空。S1a 为 `auth_type=none` 增加显式的无认证选项，不保存、不发送假 Key；该选项默认关闭，现有企业模式构建路径不受影响。

### 8.2 校验

保存时校验：

- `name` 1–64 字符，不能为空。
- `slug` 1–32 字符，`[a-z0-9-]`，不能与已有未删除 Provider 冲突。
- `base_url` 是合法 `http`/`https`，没有 userinfo/fragment/query。
- 拒绝云元数据地址 `169.254.169.254` 和 IPv6 链路本地元数据地址。
- 不跟随重定向；Provider 请求的响应体限制为 1 MiB。
- `auth_type=bearer` 时必须有密钥；`auth_type=none` 时不能提交密钥。
- 连接超时默认 5 秒，请求超时默认 120 秒；管理页可改但设有上限。

### 8.3 测试

测试分两级，保存 Provider 不会自动执行付费 chat：

1. **模型探测（默认）**
   - `GET {base_url}/models`
   - 不消耗 token；仍可能有上游速率限制。
   - 返回状态码、延迟、模型数量和脱敏错误码。
2. **极小 chat（显式）**
   - 请求必须带 `confirm=true` 和明确模型。
   - 直接调用上游，`max_tokens=16`，不使用 tools/images，不流式。
   - 结果只记录成功/失败、延迟、状态码和脱敏错误码，不保存上游正文。

管理 UI 必须在付费测试按钮旁显示“可能产生极小费用”，不能默认勾选。

## 9. 模型发现

发现流程：

1. `GET {base_url}/models`，限制响应大小和模型数量。
2. 只接受 OpenAI 形状的 `data[].id`；ID 去重、长度和字符集校验。
3. 新模型写入 `models`，`enabled=0`、`source=discovered`。
4. 已存在且来源为 discovered 的记录更新 `stale=0` 和 `discovered_at`；不覆盖手工字段。
5. 手工记录保持原字段，只记录“上游仍然出现”。
6. 本次未出现、且来源为 discovered 的记录标 `stale=1`；不硬删除。
7. 返回本次新增、更新、stale 数量和脱敏模型列表。

公共模型 ID 示例：

```text
groq/llama-3.3-70b-versatile
openrouter/meta-llama/llama-3.1-70b-instruct
ollama/qwen2.5:7b
```

第一个 `/` 前是 Provider slug，后面完整保留为上游模型 ID。

## 10. 数据面请求路径

### 10.1 S1a 暴露的合同

```text
GET  /healthz
GET  /readyz
GET  /v1/models
POST /v1/chat/completions
```

公共模型 ID 含 `/`，因此 S1a 不把旧 gateway 的 `/v1/models/{model}` 当成合同；如果保留单模型详情，路径必须是 `/v1/models/{provider}/{model...}` 或改用 query，不能依赖单段 `{model}`。

standalone 不启动：

- `/v1/embeddings`
- `/v1/images`
- `/v1/billing/*`
- `/admin/tenants`、`/admin/api-keys` 等企业租户管理接口
- 图片存储和 Casdoor 账户逻辑

现有 `/v1/messages` 可以作为实验性透明路由保留，但不能出现在 S1a 验收、README 或对外能力声明中；若要保留，必须在代码中显式标注 not in standalone contract。

### 10.2 鉴权

```http
Authorization: Bearer agl_...
```

- 不合法 Key：401 `auth_failed`。
- `admin` principal 出现在数据面：401。
- 缺少 `chat.write`：403 `scope_denied`。
- S1a 不做每 Key 日限额；只保留现有并发/请求体限制。

### 10.3 模型路由

- 请求的 `model` 必须是完整公共 ID `providerSlug/upstreamModelID`。
- 找不到：404 `model_not_found`。
- 模型 disabled：403 `model_disabled`。
- 模型 stale：409 或 403 `model_stale`，不允许静默回退到其他模型。
- 找到后 Snapshot 返回单个 Provider Adapter，并把它映射到上游模型名。
- S1a 不跨 Provider fallback；上游失败按现有 gateway 错误映射返回。
- 请求里的 `stream=true` 走现有 SSE；客户端断开时取消上游请求。

### 10.4 `GET /v1/models`

返回当前 Key 可见且启用的公共模型：

```json
{
  "object": "list",
  "data": [
    {
      "id": "groq/llama-3.3-70b-versatile",
      "object": "model",
      "owned_by": "groq"
    }
  ]
}
```

不返回 Base URL、凭据前缀、上游 ID 以外的敏感信息或 Provider 内部状态。

## 11. 管理 API

管理 listener 使用 `/admin` 前缀，返回 JSON 或静态页面。所有变更接口要求 session + CSRF。

### 11.1 会话

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/admin/session` | admin token 换 session cookie 和 CSRF token |
| `DELETE` | `/admin/session` | 注销当前 session |
| `GET` | `/admin/state` | generation、ready、repair_required、Provider/模型数量、脱敏错误 |

### 11.2 Provider

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/admin/providers` | 列表；不返回密钥或密文 |
| `POST` | `/admin/providers` | 新建 Provider；可选初始凭据 |
| `GET` | `/admin/providers/{id}` | 详情；只返回凭据掩码和状态 |
| `PATCH` | `/admin/providers/{id}` | 更新名称、URL、超时、启用状态或轮换密钥 |
| `DELETE` | `/admin/providers/{id}` | 软删除；模型保留为不可路由，后续可清理 |
| `POST` | `/admin/providers/{id}/test` | 模型探测或显式极小 chat |
| `POST` | `/admin/providers/{id}/discover` | 拉取上游模型并写目录 |

创建示例：

```json
{
  "name": "Groq",
  "slug": "groq",
  "kind": "openai_compatible",
  "base_url": "https://api.groq.com/openai/v1",
  "auth_type": "bearer",
  "api_key": "gsk_...",
  "enabled": true
}
```

测试示例：

```json
{
  "mode": "chat",
  "model": "llama-3.3-70b-versatile",
  "confirm": true
}
```

`mode=models` 不需要 `confirm`；`mode=chat` 缺少 `confirm=true` 时返回 400 `provider_test_chat_requires_confirmation`。

### 11.3 模型

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/admin/models` | 支持 `provider_id`、`enabled`、`stale` 过滤 |
| `POST` | `/admin/models` | 手工创建模型；用于上游不提供 `/models` 的服务 |
| `PATCH` | `/admin/models/{id}` | 启用/禁用、展示名、能力、窗口 |

手工模型与发现模型的优先级：

- `manual` 字段优先于 discovered。
- discovered 刷新不能覆盖手工展示名、能力、窗口或启用状态。
- stale 模型不自动删除；管理页显示“上游已移除”。

### 11.4 本机 Key

S1a 不提供完整 Key CRUD。只提供：

- 初始化页一次性显示 `local` Key。
- `GET /admin/state` 返回 KeyID、前缀、启用状态和最后使用时间。
- 轮换通过本地 CLI `--rotate-local-key`。

S1b 再增加 `POST /admin/client-keys`、`DELETE /admin/client-keys/{id}`、设备名、作用域和基础用量。

### 11.5 错误形状

```json
{
  "error": {
    "code": "provider_url_invalid",
    "message": "provider_url_invalid"
  }
}
```

- `message` 默认等于稳定 code，不拼接上游正文。
- 配置错误返回 400/422；认证错误 401；CSRF/来源错误 403；不存在 404；版本冲突 409；存储/快照不可用 503。
- 管理 API 不允许 `text/html` 错误页，避免把内部路径/堆栈返回浏览器。

## 12. 管理页面

S1a 管理页只有四个区域：

1. **初始化/状态**：管理地址、generation、ready、repair 状态、本机 Key 一次性展示。
2. **服务商**：列表、添加、编辑、启用/禁用、模型探测、极小 chat 测试。
3. **模型**：发现结果、启用开关、stale 标记、能力和窗口编辑。
4. **运行状态**：Provider 最近测试、模型数量、最近错误码、数据面/管理面监听地址。

要求：

- 不显示明文凭据、密文或 admin token。
- 不用 `localStorage` 存 token。
- 静态资源本地提供，CSP 至少限制 `default-src 'self'`；不引入远程 CDN。
- 所有修改完成后刷新 `generation`，让用户知道数据面是否已加载。
- 管理页不通过数据面端口提供。

## 13. 可观测性与诊断

结构化日志建议字段：

```text
event, generation, provider_id, model_id, key_id,
status, latency_ms, error_code, listener
```

不记录：

- Authorization、Cookie、CSRF。
- prompt/messages/tools 参数。
- 上游响应正文。
- Provider Key、密文、主密钥。

管理状态端点返回：

```json
{
  "generation": 3,
  "ready": true,
  "repair_required": false,
  "providers": { "enabled": 2, "unreadable": 0 },
  "models": { "enabled": 4, "stale": 1 },
  "last_error": null
}
```

错误码和测试结果可以持久化到 `audit_log`，但原始上游错误文本只在内存中短暂使用，必须脱敏后才能展示。

## 14. 安全边界

### 14.1 S1a 必须做到

- 两个 listener 都只绑定 loopback。
- 数据面没有 `/admin` 路由。
- 管理 API 先认证再读 body。
- 请求体、响应体有大小上限。
- Provider 请求不跟随重定向，限制响应大小和超时。
- 拒绝云元数据地址和 URL userinfo。
- 凭据加密、掩码、日志脱敏。
- 管理 token 与 local key 都不进入 URL query。

### 14.2 S1a 不解决

- 局域网访问、TLS、证书管理。
- 可信代理、CIDR 白名单、IP 限流。
- 多租户隔离、SSO/Casdoor 角色。
- 多副本 SQLite、备份编排、审计外送。
- 订阅 OAuth 账号池和防盗号。
- 客户端配置文件自动改写。

这些能力在 S3/S4/S5 单独立项，不能靠 S1a 的“本地可运行”推断已经具备。

## 15. S1a 验收

### 15.1 功能闭环

1. 在临时数据目录启动 standalone，不安装 MySQL/Redis。
2. 终端出现管理地址和一次性 admin token。
3. 登录管理页，添加一个 OpenAI 兼容 Provider。
4. 执行 `/v1/models` 探测成功。
5. 发现至少一个模型，默认禁用，手工启用。
6. 管理页一次性显示 `local` Key。
7. 用 `curl` 或 SDK 调 `/v1/chat/completions`，收到完整 SSE 和 `[DONE]`。
8. 重启进程后，Provider/模型/Key 仍可用。
9. 保存一个无效 Provider 配置，返回 422，旧 Snapshot 的 `generation` 不变且旧模型仍可调用。
10. 查看日志和数据库导出，确认没有明文/密文 Key。

### 15.2 自动测试

- Snapshot 构建：原子替换、旧读者继续使用旧 generation、坏配置不替换。
- SQLite：迁移、唯一约束、软删除、stale 规则、事务回滚。
- Secrets：AES-GCM AAD、keyring current/previous、错误密钥导致 `unreadable`。
- Auth：local key 哈希/常量时间、admin 拒绝进入数据面、数据 Key 拒绝进入管理面。
- Admin：cookie 属性、CSRF、Origin、登录限速、错误不泄露凭据。
- Provider：URL 校验、重定向拒绝、响应大小限制、模型发现 upsert。
- Gateway：mock upstream SSE、`[DONE]`、模型 disabled/stale 错误、没有 fallback。
- 修复模式：无效 catalog 时 admin 可访问、数据面 503；修复后 ready。

### 15.3 验证命令

实现阶段在 Windows 上至少执行：

```powershell
gofmt -l .
go vet ./...
go test -count=1 ./...
go build ./...
git diff --check
```

如果 `go test ./...` 因本机链接内存不足失败，必须分别记录失败包和其余通过结果；不能写成“全量通过”。Linux/CI 的 race 验证在 S0 或实现阶段单独执行。

## 16. 明确不在 S1a

- 路由组、虚拟模型、权重、自动 fallback。
- 每 Key 硬限额、模型白名单、CIDR、上下文窗口筛除。
- `/v1/responses`、Codex 兼容声明。
- 多设备 Key 管理、局域网监听、TLS、Remote AgentMesh。
- 公网管理、可信代理、审计外送、备份恢复演练。
- MySQL/Redis 双 Store。
- 订阅 OAuth、插件、隐私脱敏、搜索/图片/视频工具。
- 自动改写 Claude Code/Codex/OpenCode 配置文件。

## 17. 实施顺序

1. `internal/identity` 与兼容鉴权。
2. `internal/secrets` + 数据目录/keyring。
3. `internal/catalog` schema、Store、迁移。
4. Snapshot 构建与原子替换。
5. admin session/CSRF 和 state API。
6. Provider CRUD、测试、模型发现。
7. local key 初始化与轮换。
8. 数据面 resolver、`/v1/models`、SSE 闭环。
9. 管理页面。
10. 修复模式、自动测试、文档和演示。

## 18. 待确认决策

1. 免费池是否进入主线；若进入，先完成 S0 Review，再决定是否作为 S2 的候选来源。
2. 目标用户是否包含团队局域网；若是，S1b 的 Key 归属字段需要从第一天保留设备名。
3. Codex 是否作为核心演示；若是，S2.5 的 Responses 必须排在 S3 前。
4. 管理 UI 是复用现有 `web` 构建链，还是新建 `web/admin`；两者都要嵌入单二进制，不能要求用户另起 Node 服务。
