# S1a 任务清单

**状态：** 全部未开始。本文只拆任务，不代表代码已实现、已提交或已发布。

## 依赖顺序

```text
T001 → T002 → T003 → T004 → T005
                          ├→ T006
                          ├→ T007
                          └→ T008 → T009
T005 + T006 + T008 → T010 → T011
T005 + T007 + T008 + T010 → T012
全部 → T013 → T014
```

## T001：standalone 组合入口

- [ ] 为 `cmd/api` 增加 `--standalone`、`--data-dir`、`--admin-addr`。
- [ ] 增加 `--reset-admin-token`、`--rotate-local-key` 维护分支。
- [ ] 校验两个 listener 都是 loopback，且端口不相同。
- [ ] standalone 与 `--providers` 冲突时返回稳定错误，不静默忽略。
- [ ] 保持非 standalone 分支现有启动顺序和行为不变。

## T002：数据目录和 SQLite 基础

- [ ] 实现数据目录创建、权限检查、进程锁和 `--data-dir` 缺失错误。
- [ ] 引入纯 Go SQLite 驱动；配置 WAL、foreign keys、busy timeout、synchronous。
- [ ] 嵌入迁移并实现 `schema_version`、事务失败回滚。
- [ ] 增加 `catalog_metadata`、`providers`、`provider_credentials`、`models`、`client_keys`、`audit_log` 初始迁移。
- [ ] 增加迁移、唯一约束、软删除和重启持久化测试。

## T003：秘密管理和 keyring

- [ ] 实现 `master.key` 的 current/previous keyring JSON。
- [ ] 使用 AES-256-GCM，AAD 绑定 `provider_id/credential_id/version`。
- [ ] 实现凭据加密/解密、掩码和不可读错误。
- [ ] 保证日志/错误/trace 不输出明文、密文、主密钥。
- [ ] 增加错误密钥、previous 解密、AAD 换绑失败测试。

## T004：catalog Store

- [ ] 实现 Provider 创建、读取、更新、软删除。
- [ ] 实现单 Provider 单 enabled 凭据约束和密钥轮换（credential_id 不变）。
- [ ] 实现模型发现 upsert：新模型 disabled、手工字段不被覆盖、缺失标 stale。
- [ ] 实现手工模型创建，并保证发现刷新不覆盖手工字段。
- [ ] 模型能力默认 `chat=true, tools=false, vision=false`，由管理员显式启用。
- [ ] 实现公共模型 ID `providerSlug/upstreamModelID` 的生成、解析和冲突校验。
- [ ] 实现本地 Key 的创建、前缀/哈希、禁用和轮换。
- [ ] 增加 Store 单元测试和事务回滚测试。

## T005：Snapshot 构建和原子替换

- [ ] 定义不可变 `Snapshot`、`ProviderView`、`ModelRoute`、generation。
- [ ] 在事务内构建候选 Snapshot；失败不提交、不替换旧 Snapshot。
- [ ] 成功后用 `atomic.Pointer` 替换，旧请求保持旧 Snapshot。
- [ ] Snapshot 不持有 `*freepool.Pool` 或可变冷却/限额状态。
- [ ] 定义稳定 `laneKey` 和进程级 runtime 状态接口（S1a 可只记健康/测试状态）。
- [ ] 增加并发读、坏配置回滚、generation 递增测试。

## T006：identity 与鉴权兼容

- [ ] 新增 `internal/identity`，定义 Kind、Principal、Scope 和 context。
- [ ] `auth` 增加独立 `PrincipalStore` 入口；旧 `tenant.Store`/`Authenticate` 路径保持兼容。
- [ ] `auth.TenantID(ctx)` 从 Principal 兼容读取，不破坏现有 gateway/reservation 调用。
- [ ] 数据面拒绝 admin；管理面拒绝 local/app_key。
- [ ] 增加 local key 哈希、常量时间比较和 Principal context 测试。

## T007：管理 listener 和会话

- [ ] 独立管理 listener，只绑定 loopback；数据面不挂 `/admin`。
- [ ] 首次初始化生成 admin token 哈希并打印一次明文。
- [ ] `POST /admin/session` 换 HttpOnly + SameSite=Strict cookie，返回 CSRF。
- [ ] 所有修改请求校验 Origin 和 CSRF；不使用 localStorage。
- [ ] 登录失败限速；session 只存内存，重启失效。
- [ ] 增加 cookie、CSRF、Origin、限速和数据面隔离测试。

## T008：Provider API、测试和模型发现

- [ ] 实现 Provider CRUD API，列表/详情不返回明文或密文。
- [ ] 实现 `/admin/models` 的列表、手工创建和 PATCH。
- [ ] 为无认证 Provider 增加显式 no-auth 选项，不生成/发送假 Key，且默认关闭。
- [ ] 实现 Base URL 校验：http/https、无 userinfo/fragment/query、拒绝云元数据地址。
- [ ] Provider HTTP 客户端不跟随重定向，限制响应体 1 MiB，连接/请求超时。
- [ ] 实现 `POST /admin/providers/{id}/test` 的 models 探测。
- [ ] 实现显式 `chat` 测试：`confirm=true`、`max_tokens=16`、不流式、不自动执行。
- [ ] 实现 `POST /admin/providers/{id}/discover` 和发现结果写目录。
- [ ] 增加 URL、重定向、响应大小、测试不泄漏秘密的测试。

## T009：管理页面

- [ ] 复用现有 Vite 构建链或新增 `web/admin`；最终嵌入单二进制。
- [ ] 实现初始化/状态、服务商、模型、运行状态四个区域。
- [ ] 初始化页一次性展示 local key；之后只显示掩码/轮换提示。
- [ ] 所有修改后刷新 generation 和 repair 状态。
- [ ] CSP `default-src 'self'`，不引入远程 CDN，不用 localStorage 存 token。
- [ ] 增加页面基本交互和敏感信息不落 DOM/localStorage 的检查。

## T010：数据面 resolver 和模型合同

- [ ] 实现 catalog resolver，满足现有 gateway `TenantResolver` 接口。
- [ ] 模型 `providerSlug/upstreamModelID` 映射到单 Provider Adapter 和上游模型名。
- [ ] 实现 Provider Adapter 包装：唯一 Name、每模型单 alias 子 Adapter、能力/计费模型透传。
- [ ] 对单 Provider 启用模型数设上限，禁用模型不构建 Adapter。
- [ ] `/v1/models` 只返回当前 Key 可见且启用的模型。
- [ ] disabled/stale/not found 返回明确稳定错误；S1a 没有跨 Provider fallback。
- [ ] Chat Completions 继续复用现有 SSE、首包后错误和 `[DONE]` 语义。
- [ ] standalone 不启动 embeddings/images/billing/企业 admin 路由。

## T011：本机 Key 初始化和轮换

- [ ] 首次启动生成一个 `local` Key，保存前缀/哈希，原始值不写日志。
- [ ] 管理页初始化面板一次性返回原始 Key；之后不可再次读取。
- [ ] `--rotate-local-key` 禁用旧 Key、生成新 Key、打印一次后退出。
- [ ] `GET /admin/state` 返回 KeyID、前缀、状态和最后使用时间。
- [ ] 增加 Key 丢失/重启/轮换测试。

## T012：修复模式、就绪和错误面

- [ ] 空 catalog：管理页引导，数据面 `/readyz` 503。
- [ ] Snapshot 编译失败：管理面可访问，数据面 `/healthz` 200、`/readyz` 503、`/v1/*` 503。
- [ ] 修复后自动编译新 Snapshot，数据面恢复。
- [ ] SQLite/迁移失败直接退出，不伪造管理面。
- [ ] 统一稳定错误码和 JSON 错误形状；管理错误不返回 HTML/堆栈。

## T013：验证和证据

- [ ] 单元测试覆盖 catalog、secrets、identity、admin、resolver。
- [ ] 集成测试用 mock upstream 完成 Provider 添加→发现→启用→local Key→SSE→`[DONE]`。
- [ ] 集成测试覆盖重启持久化、坏配置回滚、修复模式。
- [ ] 检查日志/数据库导出的秘密泄漏。
- [ ] 执行 `gofmt -l .`、`go vet ./...`、`go test -count=1 ./...`、`go build ./...`、`git diff --check`。
- [ ] Windows 全量测试若因链接内存不足失败，记录失败包和其余通过结果。
- [ ] 不在本阶段强行跑 Windows race；Linux/CI race 由 S0 或实现阶段单独记录。

## T014：文档和演示收尾

- [ ] 更新 README/docs 的运行方式和“尚未实现/不支持”边界。
- [ ] 记录一次本地真实 Provider 或本机 Ollama 的脱敏演示结果。
- [ ] 记录一次重启持久化和一次坏配置回滚证据。
- [ ] 明确不提交、不推送、不创建 PR/标签/Release。
- [ ] 根据用户确认决定是否把本分支设计转为实施阶段。

## S1a 完成门槛

- [ ] 无 MySQL/Redis 的机器能启动。
- [ ] 网页添加 Provider、探测 `/models`、发现并启用模型。
- [ ] local Key 调通 SSE，收到 `[DONE]`。
- [ ] 重启后配置仍可用。
- [ ] 坏配置不替换旧 Snapshot，管理页可修复。
- [ ] 日志无明文/密文 Key。
- [ ] 管理面只 loopback，数据面没有 `/admin`。

## 明确不在本清单

- 路由组、权重、fallback。
- 每 Key 限额、CIDR、上下文窗口过滤。
- 多设备 Key、局域网、TLS、Remote AgentMesh。
- `/v1/responses`/Codex。
- MySQL/Redis 双 Store。
- 订阅 OAuth、插件、隐私脱敏、搜索/图片/视频。
- 自动改写客户端配置文件。
