# 决策 005：单机控制面与现有数据面解耦

**状态：** Accepted（S0 评审通过，2026-11-16）
**日期：** 2026-11-16
**分支：** `feat/standalone-control-plane`
**基线：** `master` / `6f9ab49`

## 背景

AgentMesh 已有一套可用的 OpenAI 兼容数据面：网关、Provider Adapter、Router、SSE、鉴权、租户路由、配额预留和账本。当前缺口不是再写一个转发循环，而是缺少 magpie 式的本地控制面：用户能够在网页里添加 Provider、发现模型、测试连通、启用模型，并让正在运行的网关无重启地读取新配置。

同时存在两个必须隔离的现实条件：

1. 主工作区的 `feat/free-tier-catalog` 有未提交的免费池改动，尚未成为可信基线。
2. 现有企业模式依赖 MySQL/Redis/Casdoor，不适合作为单机首次体验的前置条件。

## 决策

1. **数据面不重写。** `internal/gateway`、`internal/router`、`internal/provider` 的公开契约继续使用；新增控制面只负责生成它们需要的只读运行快照。
2. **单机模式先只用 SQLite。** S1a 不引入 SQLite/MySQL 双 Store，不把企业模式的 MySQL/Redis 生命周期塞进 standalone。现有企业模式保持原路径。
3. **控制面写数据库，数据面只读快照。** 管理操作在 SQLite 事务内校验并编译候选快照；编译失败不提交、不替换旧快照。成功后用 `atomic.Pointer` 原子替换。
4. **快照不持有可变的限流/冷却状态。** Snapshot 只包含不可变的 Provider、模型和 Key 元数据；quota、冷却、健康状态放在进程级 runtime，使用稳定 `laneKey` 关联，避免热加载时把状态绑定到数组下标。
5. **S1a 即引入最小 Principal。** 新包 `internal/identity` 定义 `admin`、`local`、`app_key` 三种 Kind。S1a 只实际发放一个 `local` Key；完整多 Key 管理放 S1b。`tenantID` 变成 Principal 的兼容字段，而不是唯一身份。
6. **管理面独立监听。** 数据面 `127.0.0.1:18080` 不挂 `/admin`；管理面 `127.0.0.1:18081` 独立提供 HTML、管理 API 和健康检查。S1a 不允许 `0.0.0.0`。
7. **S1a 只做直接 `provider/model` 路由。** 不实现路由组、权重、跨 Provider fallback、每 Key 限额、上下文窗口过滤、局域网和公网能力。
8. **凭据加密保存。** Provider 密钥使用 AES-256-GCM，AAD 绑定 `provider_id/credential_id/version`；数据库只存密文，列表只显示掩码。主密钥和数据库必须分开备份。
9. **免费池暂不进入本分支。** `internal/freepool` 仍是独立实验改动，是否进入主线由 S0 决策记录决定；本分支不依赖它，也不把未提交能力写成已发布能力。
10. **只借鉴 magpie 的产品边界。** 学习其单机控制面、Provider/模型目录、本地网关 Key 和热加载体验；不复制其代码，不引入客户端配置文件改写、订阅 OAuth 账号池、插件中间件、隐私脱敏或搜索/图片/视频工具。AgentMesh 为 GPL-3.0-only，magpie 为 MIT；本决策不改变许可证，也不做代码搬运。

## 理由

- 现有数据面已经解决了 SSE、首包前 fallback、Provider 边界和错误归一化，重写会丢失已有验证。
- 先用 SQLite 可以把“第一次打开就能用”与“企业级 MySQL/Redis”分开，降低首次运行门槛。
- 快照和可变运行状态分离后，配置热加载不会重置冷却、窗口计数或正在进行的请求。
- 独立管理监听和 loopback 限制是后续局域网/公网部署的安全基础；不能依赖前端隐藏菜单。
- 先做直接路由，可以在 S1a 内形成可验收闭环，同时给 S2 的限额和路由组保留稳定接口。

## 被否决的方案

| 方案 | 否决原因 |
| --- | --- |
| 直接从 magpie 复制控制面/插件代码 | 许可证、产品边界和维护成本不匹配；AgentMesh 只需要其中的本地控制面思路 |
| S1a 同时实现 SQLite 与 MySQL Store | 双重事务语义和迁移成本过高，且企业模式当前已有独立路径 |
| 管理 API 与 `/v1` 共用端口 | 需要额外的路径白名单和反向代理假设，容易把管理面暴露到公网 |
| 控制面直接修改运行中的 Provider/Pool | 请求会看到半更新状态，无法保证旧请求和失败回滚 |
| 先用每 Key 限额/路由组再补目录 | 鉴权签名、配额原子性和模型元数据会反复返工 |
| 把免费池作为 S1a 的前置 | 免费池仍未提交、未形成 S0 决策；不能把实验能力当作主线基线 |

## 影响

- `cmd/api` 增加 `--standalone`、`--data-dir`、`--admin-addr`，企业模式不带这些参数时行为不变。
- 新增 `internal/identity`、`internal/catalog`、`internal/secrets`（或等价包）；Provider 构建从环境变量改为从快照构建。
- `auth` 和 `gateway` 需要增加 Principal 兼容读取，但 S1a 不改现有 reservation/billing 语义。
- S1a 首次启动会生成管理 token 和 `local` API Key；管理 token 只打印一次，`local` Key 只在初始化页面展示一次，之后只能轮换。
- S2 需要把 Principal 传入配额和路由组；S2.5 视 Codex 是否为核心演示决定是否提前做 Responses。

## 后续决策

1. S0 评审完成后，决定免费池是进入主线、保持独立分支，还是只作为导入来源。
2. 确认目标用户是个人、团队局域网还是公网多租户；这决定 S2 是否强制每 Key 限额、S4 是否必须 MySQL/Redis。
3. 确认 Codex 是否为核心演示；若是，S2.5 必须排在 S3 之前。
