# S3.5 任务清单

**状态** S3.5a 已通过 Windows 自动验收。后续阶段的独立交付见 [阶段复验入口](../../docs/acceptance/README.md)。

S3.5a 的实际验收范围见 [验收记录](../../docs/acceptance/S3.5a.md)。后续工作边界见 [分工与设计稿](parallel-design.md)。

## S3.5a：只读扫描与本地模型目录

### T001：宿主扫描框架

- [x] 新增 `internal/hostscan` 类型、扫描根目录限制和超时。
- [x] 实现 PATH 绝对目录发现，拒绝相对项和 UNC。
- [x] 实现已知 Codex、Claude Code、Gemini CLI、OpenCode 目录发现。
- [x] 实现 symlink、Windows junction、reparse point 拒绝。
- [x] 不执行发现到的二进制，不读取浏览器 Cookie 或密码库。
- [x] 扫描结果只返回元数据和状态。

### T002：本地模型发现

- [x] 实现 Ollama `/api/tags` 解析。
- [x] 实现 LM Studio `/v1/models` 解析。
- [x] 只接受 loopback endpoint；限制响应体、超时和重定向。
- [x] 服务不可达时返回稳定错误，不影响 standalone 启动。

### T003：Provider 和模型目录

- [x] 增加 `ollama`、`lmstudio` Provider 类型。
- [x] 实现本地 Provider 导入，使用 no-auth 和 `/v1` 聊天端点。
- [x] 复用模型 upsert：新模型 disabled、手工字段保留、缺失标 stale。
- [x] 验证重启后 Provider、模型和启用状态持久化。

### T004：管理 API 和 UI

- [x] `GET /admin/host/agents`
- [x] `POST /admin/host/scan`
- [x] `POST /admin/host/local-models/scan`
- [x] `POST /admin/host/local-models/import`
- [x] 所有管理写操作使用 session、CSRF、Origin 校验。
- [x] 管理页增加“本机资源”区域，显示 Agent 状态和本地模型发现。

### T005：测试与验收

- [x] 单元测试扫描路径、reparse 拒绝和敏感信息不泄漏。
- [x] mock Ollama/LM Studio 测试发现、导入、默认禁用和 stale。
- [x] 集成测试重启持久化。
- [x] `go test`、`go vet`、`go build`、`node --check`、`git diff --check`。
- [x] 创建 `docs/acceptance/S3.5a.md`。
- [x] 更新文档导航和阶段复验入口。

## S3.5b：安全接入片段与单 Adapter 自动写入

- [x] 只读生成可复制的 OpenCode 1.x Chat Completions 片段。
- [ ] 选择并验证唯一自动写入 Adapter。
- [ ] `config_change_plans`、`plan_id`、`expected_hash`、diff 和加密快照。
- [ ] allowlist、ACL、运行中检测和 rollback 条件。
- [ ] hash 漂移、symlink/junction、失败写入和回滚测试。

## S3.5c：隐私 profiles

- [x] 请求级随机占位符、输入校验及展示文本恢复单元测试。

- [ ] `privacy_profiles` 和每 Key 绑定。
- [ ] JSON 字符串值遍历，不修改字段名、工具 schema 和结构。
- [ ] Chat 文本 delta、tool call arguments、Responses 事件状态机。
- [ ] 有界占位符拆包缓存；standard masked、strict 失败关闭。
- [ ] 日志/trace/audit/backup 敏感值测试。

## S3.5d：订阅实验适配器

- [x] 核对 OpenAI 官方开源授权路径与 Anthropic 个人订阅限制。

- [ ] `--enable-subscription-adapters`，默认 false。
- [ ] 一个订阅账号对应一个 Provider。
- [ ] 显式导入、外部引用、刷新单飞、撤销和失败冷却。
- [ ] local-only 在 Snapshot/路由层强制。
- [ ] 条款/风控提示和不共享测试。

## S3.5e：真实局域网

- [x] RFC1918 地址和已分配接口校验单元测试。

- [ ] `AllowLANListen` 和显式启动参数。
- [ ] 管理面继续 loopback。
- [ ] 数据面 LAN 绑定、设备 Key 和 CIDR/撤销边界。
- [ ] 同网两台真实设备验收；记录管理面不可达。

## S3.6：备份硬化

- [x] `agentmesh.lock` 单写者锁及 Windows 跨进程竞争测试。
- [ ] 版本化 `.agmbackup`、认证加密和 manifest。
- [ ] 离线恢复、预检、校验和原子替换。
- [ ] 错误密码、篡改、截断、磁盘不足和中断测试。
- [ ] 旧明文目录备份兼容导入。
