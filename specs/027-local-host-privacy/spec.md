---
level: L2
feature: 027-local-host-privacy
created: 2026-11-18
status: in-progress
---

# S3.5 本机宿主与本地模型控制面

**原始需求：** 在现有 AgentMesh 单机控制面上增加本机 Agent 只读发现、Ollama/LM Studio 本地模型目录，并将订阅、配置写入、隐私、真实局域网和备份硬化拆成独立可验收阶段。

**设计文档：** [S3.5 设计](../../docs/S3.5-local-host-privacy.zh-CN.md)
**决策：** [决策 007](../../decisions/007-local-host-subscription-privacy.md)
**S3.5a 状态** 已通过 [Windows 自动验收](../../docs/acceptance/S3.5a.md)。

**后续设计与集成边界：** [并行开发设计](parallel-design.md)

## 1. 目标

在 S1a-S4 已实现的 standalone 控制面基础上，S3.5a 形成一个可演示闭环：

1. 用户打开管理页。
2. 触发只读宿主扫描。
3. 看到本机已知 Agent 的安装与配置状态。
4. 触发 Ollama/LM Studio loopback 发现。
5. 看到本地模型列表。
6. 显式导入后得到本地 Provider 和禁用状态的模型目录。
7. 重启后 Provider 和模型仍存在。
8. 扫描和响应中不出现 token、Cookie 或完整配置正文。

## 2. 范围

### 2.1 包含

- `internal/hostscan` 只读扫描框架。
- Codex、Claude Code、Gemini CLI、OpenCode 的已知路径和 PATH 发现。
- symlink/junction/reparse point 拒绝。
- Ollama `/api/tags` 发现。
- LM Studio `/v1/models` 发现。
- 本地模型 Provider 导入和模型目录持久化。
- `ollama`、`lmstudio` Provider 类型。
- 管理 API、最小管理 UI、单元/集成测试和验收记录。

### 2.2 不包含

- 订阅账号导入、刷新和撤销。
- Agent 配置文件自动写入。
- 隐私脱敏。
- 真实局域网监听或 TLS。
- `.agmbackup`。
- 公网、反向代理、多副本或生产部署。

## 3. 安全边界

- 扫描根目录只来自已知目录、PATH 绝对目录和用户显式添加的根目录。
- PATH 相对项、UNC 路径、symlink、junction、reparse point 一律跳过。
- 扫描不执行任何发现到的二进制。
- 扫描不读取浏览器 Cookie、密码库或无关文件。
- 扫描结果不包含 token、Cookie、配置正文。
- Ollama/LM Studio endpoint 只接受 loopback HTTP。
- discovery 结果默认不启用模型。
- 导入是显式管理操作，必须通过 session、CSRF 和 Origin 校验。

## 4. 功能需求

### 4.1 宿主扫描

- 已知客户端：

| Agent | 可执行文件 | 已知目录 |
| --- | --- | --- |
| Codex | `codex` | `%USERPROFILE%\\.codex` |
| Claude Code | `claude` | `%USERPROFILE%\\.claude` |
| Gemini CLI | `gemini` | `%USERPROFILE%\\.gemini` |
| OpenCode | `opencode` | `%USERPROFILE%\\.config\\opencode`、`%APPDATA%\\opencode` |

- 返回 `id`、显示名、状态、安装路径（如找到）、配置目录（如找到）、可读性和最后修改时间。
- 状态至少包括：`detected`、`config_found`、`permission_denied`、`invalid`。
- 第一版不判断登录状态；不得通过读取登录文件伪造 `needs_login`。
- 单次扫描限制根目录数量、文件检查数量和执行时间；超时返回部分结果和稳定 `scan_truncated` 标记。

### 4.2 本地模型发现

- 接受显式 loopback base URL；缺省使用默认地址。
- Ollama 请求 `/api/tags`，兼容 `{ "models": [{ "name": "...", "model": "..." }] }`。
- LM Studio 请求 `/v1/models`，兼容 OpenAI `{ "data": [{ "id": "..." }] }`。
- 响应体上限 1 MiB，超时 5 秒，不跟随重定向。
- 发现结果只包含来源、模型 ID、类型和可选元数据；不包含 endpoint 凭据。
- 本地服务不可达时返回稳定错误码，不导致控制面启动失败。

### 4.3 导入

- 导入请求包含来源类型、base URL 和确认标志，可指定 Provider slug 和显示名。服务端重新发现模型并在导入结果返回实际目录。
- 发现失败或结果为空时，导入返回稳定错误且保留现有目录。成功取得非空权威目录时才按缺失项标记 stale。
- Provider slug 由用户提供或使用稳定默认值；导入后 slug 不可变。
- Provider 为无认证；不需要保存假 API Key。
- 导入复用现有模型 upsert：
  - 新模型 disabled；
  - 已有手工能力字段不被覆盖；
  - 缺失模型标 stale；
  - 公共模型 ID 为 `<provider-slug>/<upstream-model>`。
- 导入后 Snapshot 热加载；不重启进程。

### 4.4 Provider 类型和路由

- `catalog` 增加 `ollama`、`lmstudio`。
- Snapshot 对本地 Provider 复用现有 OpenAI 兼容 adapter，base URL 必须指向其 `/v1` 端点。
- `AuthType` 为 `none`；不得发送 Authorization。
- 能力默认 tools=false、vision=false，除非手工启用。

## 5. 管理 API

```text
GET  /admin/host/agents
POST /admin/host/scan
POST /admin/host/local-models/scan
POST /admin/host/local-models/import
```

请求和响应不得返回 token、Cookie、配置正文、原始上游错误正文或任意文件内容。
管理模型路由的 `{id}` 使用目录记录 ID。包含 `/` 的公共模型 ID 供客户端选模和鉴权策略使用。

## 6. 验收

- `go test -count=1 ./internal/hostscan ./internal/catalog ./internal/standalone`
- `go vet ./internal/... ./cmd/api`
- `go build ./cmd/api`
- `node --check internal/standalone/ui/app.js`
- `git diff --check`
- 自动测试覆盖扫描、reparse 拒绝、mock 本地发现、导入、默认禁用、stale、重启持久化和敏感信息不泄漏。

## 7. 后续任务

S3.5b-S3.5e、S3.6 任务见 [tasks.md](tasks.md)。未完成阶段不得写成已实现能力。
