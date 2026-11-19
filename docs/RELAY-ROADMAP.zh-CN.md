# AgentMesh 中转控制面路线图

**状态：** 设计基线与本地阶段验收（2026-11-19）。S1a、S1b、S2、S2.5 已具备本地实现和自动测试；S3/S4 完成本地 loopback + httptest TLS 模拟验收，但均未构成真实局域网、公网或生产发布。S3.5a 的只读扫描和 Ollama/LM Studio 模型目录已通过 [Windows 自动验收](acceptance/S3.5a.md)。
**分支：** `codex/local-host-privacy`（承接 `feat/standalone-control-plane` 的本地工作）
**Git 基线：** `master` / `6f9ab49`
**阶段验收：** [S0](acceptance/S0.md)、[S1a](acceptance/S1a.md)、[S1b](acceptance/S1b.md)、[S2](acceptance/S2.md)、[S2.5](acceptance/S2.5.md)、[S3](acceptance/S3.md)、[S4 本地模拟](acceptance/S4-local-simulation.md)；统一命令见 [复验入口](acceptance/README.md)。

本文描述 AgentMesh 的新项目方向：保留现有网关数据面，增加 magpie 式的单机控制面，再按阶段走向局域网共享和公网中转。本文是计划和边界说明；已落地部分以阶段验收和自动测试为准，未落地部分仍是设计。设计路线图不等于发布能力。

## 1. 目标

AgentMesh 的目标不是再实现一个聊天转发接口，而是形成一个可管理的模型接入层：

```text
单机可用
  → 网页管理 Provider / 模型 / 本机 Key
  → 配置热加载，网关不重启
  → 每设备独立 Key
  → 局域网共享
  → 公网 HTTPS 中转
```

第一阶段的用户价值是：在一台干净的 Windows 机器上，不安装 MySQL、Redis 或 Casdoor，启动一个二进制，打开网页添加两个 OpenAI 兼容服务商，发现并启用模型，然后让 OpenCode、SDK 或 `curl` 通过本机 Key 使用这些模型。重启后配置仍在。

第一阶段不承诺公网、路由组、自动故障转移或 Codex 完整兼容。

## 2. 三条不可破坏的约束

### 2.1 请求先有 Principal

所有经过数据面的请求先完成身份解析，得到 `identity.Principal`，再由 Principal 携带 `TenantID`、`KeyID`、Kind 和 Scopes。`tenantID` 不再是鉴权链的唯一身份。

S1a 只实际使用：

- `admin`：只能进入独立管理面，不能调用 `/v1`。
- `local`：本机自动生成的唯一调用 Key。
- `app_key`：在 S1b 引入，用于每设备/每应用 Key。

### 2.2 控制面写配置，数据面只读快照

管理页的写操作不会修改正在服务的对象。操作流程是：

1. 校验输入。
2. 在 SQLite 事务中写入候选配置。
3. 从候选配置编译不可变 Snapshot。
4. 编译成功后提交事务。
5. 用 `atomic.Pointer` 原子替换运行快照。

编译或提交失败时，旧 Snapshot 继续服务。旧请求持有旧 Snapshot，不因热加载被中断。

### 2.3 限额先预留，再调用，最后结算

S1a 不做每 Key 限额，因此没有“先调用再补记”的新路径。S2 引入限额时必须遵循：

```text
Reserve → 调用上游 → Settle / Release
```

禁止靠请求结束后的异步计数实现硬限额，否则并发会穿透。

## 3. 与 magpie 的关系

参考项目：[yetone/magpie](https://github.com/yetone/magpie)。它的强项是“把多个模型来源放到一个本地网关前面，并提供统一控制面”。AgentMesh 只取其中适合本项目的部分。

### 3.1 S1a 借鉴的能力

| magpie 能力 | AgentMesh S1a 对应实现 |
| --- | --- |
| Provider 列表与添加 | 管理页添加 OpenAI 兼容 Provider，保存 Base URL、认证方式和密钥 |
| 模型目录 | `GET {base_url}/models` 发现模型，保存为本地目录 |
| `provider/model` 命名 | 公共模型 ID 使用 `providerSlug/upstreamModelID` |
| 一个本地网关 | `127.0.0.1:18080/v1`，客户端只需换 Base URL 和 Key |
| 模型选择与即时生效 | Snapshot 热加载；不重启进程 |
| 本地管理界面 | 独立 `127.0.0.1:18081` 管理页，不与数据面混用 |
| 本地网关 Key | 首次启动生成一个 `local` Key，数据面用它鉴权 |

### 3.2 后续阶段借鉴的能力

| 阶段 | 借鉴点 | AgentMesh 的差异化边界 |
| --- | --- | --- |
| S1b | 多设备 Key、Provider/模型状态页 | 先做最小调用归属，不复制客户端配置改写 |
| S2 | 路由组、额度感知、fallback、用量 | 保持显式限额和账本语义，不把“自动换号”做成不可解释行为 |
| S2.5 | Responses / Codex 接入 | 只做可验证的最小状态机，不保存服务端会话 |
| S3 | 局域网共享、Remote 网关 | 每设备独立 Key、明确模型白名单，不做多副本高可用承诺 |
| S4 | 公网 Docker/NAS、远程管理 | 管理面默认不公网；只暴露 `/v1/*` 和健康检查 |

### 3.3 明确不抄的部分

- 不复制 magpie 的代码、UI 或资源文件。
- 不做几十个 Agent 配置文件的自动改写。
- 不做订阅 OAuth 账号池、Cookie 复用或绕过服务商条款的能力。
- 不做插件中间件、隐私脱敏、搜索/图片/视频工具。
- 不在 S1a 堆 Provider 预设；先支持通用 OpenAI 兼容端点。

## 4. 目标架构

```text
                           ┌────────────────────────────┐
                           │ Admin UI (loopback only)   │
                           │ 127.0.0.1:18081            │
                           └─────────────┬──────────────┘
                                         │ cookie + CSRF
                                         ▼
                           ┌────────────────────────────┐
                           │ Control API / Admin Auth   │
                           └─────────────┬──────────────┘
                                         │ transaction
                                         ▼
                           ┌────────────────────────────┐
                           │ SQLite catalog             │
                           │ providers / credentials    │
                           │ models / client_keys       │
                           └─────────────┬──────────────┘
                                         │ compile
                                         ▼
                           ┌────────────────────────────┐
                           │ immutable Snapshot N       │
                           │ atomic.Pointer              │
                           └─────────────┬──────────────┘
                                         │ read per request
                                         ▼
[client] ── Bearer local key ──► [Data plane 127.0.0.1:18080]
                                         │
                                         ▼
                              [Gateway / Router / Provider]
                                         │
                                         ▼
                              [OpenAI-compatible upstream]
```

运行状态（冷却、窗口计数、健康状态）不放进 Snapshot。它以稳定的 `laneKey` 为键保存在进程级 runtime，S2 再把需要恢复的部分写入 SQLite。

## 5. 阶段路线

| 阶段 | 周期 | 核心能力 | 演示结果 | 明确不包含 |
| --- | --- | --- | --- | --- |
| S0 收口 | 约 1 周 | 免费池 Review、Linux/CI race、决策记录 | 有可信基线，知道免费池去留 | 不新增功能 |
| S1a 单机目录 | 1–1.5 周 | SQLite catalog、Provider CRUD、模型发现/测试、Snapshot、本机 Key、管理页 | 无 MySQL/Redis 也能添加 Provider 并调通 SSE | 路由组、限额、局域网、Responses |
| S1b 设备 Key | 约 1 周 | `app_key`、多 Key 管理、调用归属、状态页、基础用量 | 每设备一个 Key，撤销一个不影响其他 | 路由组、公网 |
| S2 策略与路由 | 1.5–2 周 | 每 Key 限额、白名单、CIDR、上下文窗口、`Reserve → Settle`、路由组、fallback | 多 Provider 自动切换，限额不超卖 | 多副本共享 |
| S2.5 Responses | 约 1 周 | 最小 Responses 状态机、流式、function tools | Codex 只读工具往返 | 服务端会话、完整 Codex 功能 |
| S3 局域网 | 约 1 周 | LAN 绑定、设备 Key、Remote AgentMesh、可选 TLS、二维码/mDNS | 台式机服务，笔记本/手机接入 | 高可用、多副本 SQLite |
| S3.5 本机资源 | 分阶段 | S3.5a 只读扫描和本地模型目录；后续再拆配置、隐私、订阅和真实 LAN | 本机 Agent 与本地模型可见、可导入 | 不在 S3.5a 做订阅、自动改写、隐私或公网 |
| S4 公网 | 2–3 周 | 域名/TLS、管理隔离、可信代理、限流、成本上限、审计、备份恢复 | 外网 HTTPS 调通，管理面不可公网访问 | 订阅 OAuth 公网池 |
| S5 生态 | 持续 | Gemini 原生、完整 Anthropic、媒体能力、SDK、客户端接入导出 | 更多客户端原生兼容 | 按榜单堆 Provider |

## 6. S0：先把基线收干净

S0 不新增用户功能，只做三件事：

1. 对 `feat/free-tier-catalog` 的未提交免费池改动做聚焦 Review：`internal/freepool`、网关接入、配置、鉴权和持久化边界。
2. 在 Linux/CI 环境执行 race、构建、配置校验和最小真实联调；Windows 本机内存不足导致的链接失败要如实记录，不能写成全量通过。
3. 形成免费池决策记录：进入主线、保持独立实验分支，还是只作为 S2 的导入来源。

S0 完成前，本分支不从免费池实现中继承任何未评审能力。S1a 的 Provider 目录以 `master` 的现有 Adapter 为基线。

## 7. S1a：单机控制面 MVP

### 7.1 启动方式

```powershell
go run ./cmd/api --standalone `
  --data-dir .\data `
  --addr 127.0.0.1:18080 `
  --admin-addr 127.0.0.1:18081
```

首次启动打印管理地址和一次性 admin token。管理页登录后展示一次本机 `local` Key；之后只显示掩码和轮换入口。

### 7.2 功能闭环

1. 打开管理页。
2. 添加一个 OpenAI 兼容 Provider，填写 Base URL 和 API Key。
3. 执行 `GET {base_url}/models` 免费探测。
4. 发现模型，默认禁用，人工启用。
5. 执行一次显式确认的极小 chat 测试（可选，`max_tokens=16`）。
6. 在初始化页复制 `local` Key。
7. 用 `curl`、OpenAI SDK 或 OpenCode 调用 `/v1/chat/completions`。
8. 得到完整 SSE 和 `[DONE]`。
9. 重启进程，配置和 Key 仍然有效。

### 7.3 S1a 的“效果”定义

S1a 交付的是“可管理的本机模型入口”，不是中转站：

- 客户端只需要一个 Base URL 和 Key，不再把每个上游 Key 写进每个客户端。
- 添加/修改 Provider 不需要改环境变量、重启进程。
- 模型来自上游发现，不在代码里硬编码。
- 配置、凭据和本机 Key 持久化在本地 SQLite。
- 管理面默认只在 loopback；数据面不会自动绑定局域网。

### 7.4 S1a 不包含

- 路由组、虚拟模型、权重、跨 Provider fallback。
- 每 Key 限额、模型白名单、CIDR、上下文窗口过滤。
- 多设备 Key 管理（S1b）。
- 局域网监听、TLS、Remote AgentMesh。
- `/v1/responses` 或 Codex 兼容声明。
- 订阅 OAuth、插件、隐私脱敏、搜索/图片/视频。
- MySQL/Redis 双 Store。

## 8. S1b：设备 Key 与状态

S1b 在 S1a 的快照和 Principal 上增加：

- 多个 `app_key`，每个有名称、前缀、哈希、作用域和撤销状态。
- 调用归属：请求 trace 和状态页显示 KeyID、Provider、模型、结果。
- 每 Key 基础状态：最后调用时间、最近错误码、请求数；暂不做硬限额。
- Provider/模型状态页：启用状态、最近测试、发现时间、stale 标记。
- Key 轮换和撤销；旧 Key 立即失效，不影响其他 Key。

S1b 仍不实现硬限额和路由组，避免在签名链未稳定时先做策略。

## 9. S2：策略、配额和路由组

S2 先做三件会改签名的事：

1. 把 `identity.Principal` 传入 auth、gateway、reservation 和 billing。
2. 用 `Reserve → Settle / Release` 替代“先调用后记账”。
3. 把 `context_window`、`max_output_tokens`、`caps` 放进 catalog 和候选过滤。

然后才做路由组：

- 模型能力过滤：tools、vision、reasoning。
- 上下文窗口过滤：输入估算 + 输出预留不能超过窗口安全比例。
- 每 Key 限额：RPM、日/周/月 token 或请求数、模型白名单、过期、CIDR。
- fallback：首包前切换；首包后不换；429/401/5xx 分别冷却。
- 冷却和窗口计数持久化；单机只有一个写者，不需要分布式锁。

S2 的候选顺序固定为：

```text
模型授权
  → 能力
  → 上下文窗口
  → Key 限额
  → 冷却
  → 打分/选择
```

## 10. S2.5：Responses 的判断

如果 Codex 是核心演示，Responses 不能拖到 S5。最小闭环只做：

- `POST /v1/responses`，支持 `input`、`instructions`、`tools`、`stream`、`max_output_tokens`。
- SSE 事件映射：`response.created`、`response.output_text.delta`、`response.completed`、`response.failed`。
- `previous_response_id` 明确返回“不支持”或只做短期内存映射，不能静默忽略后假装成功。
- 验收：Codex CLI 完成一次只读工具往返。

如果 S1/S2 不打算做 Codex，就必须在文档、README 和 UI 中明确写“暂不支持 Codex”，不能让用户踩坑后才发现。

## 11. S3：局域网共享

S3 才允许数据面绑定 `0.0.0.0` 或 RFC1918 地址，并且必须：

- 保留显式 `--allow-lan-listen` 安全开关。
- 每设备一个 `app_key`，独立撤销和基础限额。
- 可选自签 TLS；HTTP 时 UI 明确提示明文风险。
- 新增 Provider 类型 `agentmesh`，把另一台 AgentMesh 当 OpenAI 兼容上游。
- 状态页显示设备、调用量、最近错误；可选 mDNS/二维码。

S3 仍是单机进程，不宣称高可用。SQLite 不支持多副本共享。

## 12. S4：公网中转

S4 才把服务变成公网中转站，重点是边界而不是功能数量：

- Caddy/域名/HTTPS，SSE 不被缓冲。
- 公网只暴露 `/v1/*` 和健康检查；`/admin`、`/metrics`、控制台只允许内网或 SSH 隧道。
- 每 Key/IP 限流、并发上限、请求体上限、日成本上限、模型白名单、Key 过期和轮换。
- 可信代理：只有 `RemoteAddr` 在 `AGENTMESH_TRUSTED_PROXIES` 内才解析 `X-Forwarded-For`，从右向左找第一个不可信地址，最多 8 跳；不信任 `X-Real-IP`。
- 审计日志：谁、何时、哪个 Key、哪个模型、多少 token、结果；不记录聊天正文、上游 Key 或原始凭据。
- MySQL/Redis 模式恢复；SQLite 只保留为小型单机部署。
- 备份/恢复演练、迁移脚本、告警、就绪检查、优雅退出。
- 计费只按可信 usage 结算；未知 usage 进入待对账，不估算自动退款。

## 13. 决策门槛

进入实现前必须明确以下三项。本文采用默认假设，但假设不等于已批准：

| 决策 | 默认假设 | 影响 |
| --- | --- | --- |
| 目标用户 | 个人 + 小型团队局域网，后续公网 | 决定 S2 是否强制每 Key 限额、S4 是否必须 MySQL/Redis |
| Codex 是否核心 | 先不作为 S1a 验收，S2.5 再评估 | 若是核心，Responses 必须排在 S3 前 |
| 免费池是否主线 | 暂不进入本分支，等待 S0 | 决定 S1a 是只管理正式 Provider，还是兼容免费池目录 |

## 14. 风险登记

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 把 S1a 做成“大而全” | 无法按时形成可演示闭环 | 固定只做 Provider/模型/本机 Key/热加载 |
| 双 Store 过早 | 事务和迁移复杂，质量下降 | S1a 只做 SQLite，企业模式不动 |
| Snapshot 含可变状态 | 热加载重置冷却/计数，线上行为漂移 | runtime 状态按 `laneKey` 独立保存 |
| 管理面误暴露 | 公网可直接改 Provider/Key | 独立 loopback listener，数据面不挂 `/admin` |
| 密钥进入日志/备份 | 凭据泄露 | AES-GCM、AAD、掩码、日志脱敏、密钥与数据库分离备份 |
| 过早承诺 Codex/Responses | 用户按错误预期接入 | S1a 明确不支持；S2.5 单独验收 |
| 免费池未评审就合并 | 把实验能力包装成发布能力 | S0 决策后再动，本分支不依赖 |

## 15. 成功标准

S1a 完成的唯一判断标准不是“代码能编译”，而是：

1. 一台没有 MySQL/Redis 的机器能启动 standalone。
2. 管理页能添加 Provider、测试 `/v1/models`、发现并启用模型。
3. 数据面能用 `local` Key 完成一次 SSE chat。
4. 重启后配置仍可用。
5. 保存无效配置时，旧 Snapshot 继续服务，管理页可修复。
6. 日志中没有明文或密文 Key。
7. 管理面未被挂到数据面端口，也未绑定非 loopback 地址。

后续阶段只有在上一阶段验收通过并有真实证据后才能开始。
