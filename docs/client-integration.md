# OpenCode 与 Claude Code 接入

2026-10-16：已在 Windows 本机客户端 → Linux Docker Gateway → 北京百炼千问的链路完成实际 Read 工具往返。版本为 OpenCode 1.19.0、Claude Code 2.1.234，上游 qwen3.8-omni-flash。客户端读取测试目录的随机文件，将结果交回模型，再获得包含文件内容的最终回答。每个客户端两次模型请求，实际人民币扣款与供应商 token 回执一致。本次仅开放 Read；不据此宣称编辑、Shell、MCP、所有插件或复杂长任务已验收。

## 服务端准备

客户端使用**网关颁发的应用 Key**。百炼 API Key 留在服务端私有配置中。模型名用租户获准的别名 `domestic-chat`，不要把上游模型名或上游 Key 填到客户端。

```dotenv
COMPATIBLE_MODELS='{"domestic-chat":"qwen3.8-omni-flash","sql-review":"qwen3.8-omni-flash"}'
COMPATIBLE_TOOL_MODELS='["domestic-chat"]'
COMPATIBLE_VISION_MODELS='["domestic-chat"]'
COMPATIBLE_DISABLE_THINKING=true
```

只有实际支持工具/图片的模型才声明相应能力。普通文本 Ollama Adapter 当前不支持工具和图片。mTLS 模式需 Gateway 与 ModelRunner 配置一致。新部署可给 `deployments/backend-stack/setup.py` 添加 `--qwen-tools --qwen-vision`；已有配置不自动覆盖，修改后重建相应服务。另需租户路由、有效人民币价格目录、钱包可用余额，以及 Redis 配额/限流。钱包按最大三次 attempt 的上界冻结，只有几分钱余额可能不足以准入，即使实际短请求费用更低。

## 生成配置并启动

从仓库根目录执行，示例域名和路径换成自己的。`gateway-key.txt` 是**应用 Key 文件**。生成器仅 GET 模型、余额、价格和 readiness，不发模型请求、不充值、不改全局客户端设置、不在生成文件里嵌入 Key。

```powershell
python scripts/client-config.py --base-url https://api.example.com --model domestic-chat --key-file C:\private\gateway-key.txt --out C:\private\agentmesh-clients

# OpenCode 交互模式
& C:\private\agentmesh-clients\run-opencode.ps1 -KeyFile C:\private\gateway-key.txt

# Claude Code 交互模式
& C:\private\agentmesh-clients\run-claude.ps1 -KeyFile C:\private\gateway-key.txt
```

模型不可见、工具能力未启用、价格过期、余额不足或服务未就绪时，生成器拒绝输出“可用”配置。输出目录已有文件也拒绝覆盖；更新时生成新目录检查后切换。启动脚本读取 Key 放到子进程环境，结束后恢复；CLI 参数直接附在命令后，包括 Claude 的 `-p`。

| 客户端 | Provider/协议 | Base URL | 模型 |
|---|---|---|---|
| OpenCode | `@ai-sdk/openai-compatible`，Chat Completions | `https://api.example.com/v1` | `agentmesh/domestic-chat` |
| Claude Code | Anthropic Messages | `https://api.example.com`，**不加 `/v1`** | `domestic-chat` |

Linux/macOS 使用同一份 `opencode.json`，设置 `OPENCODE_CONFIG` 指向它、`AGENTMESH_API_KEY` 为应用 Key，再运行 `opencode`。Claude 设置 `ANTHROPIC_API_KEY`，清除冲突的 `ANTHROPIC_AUTH_TOKEN`，执行 `claude --settings /private/agentmesh-clients/claude-settings.json`。不要把 Key 放进命令参数/终端录屏。远程连接用可信 HTTPS，生成器只允许本机 HTTP。

## 工具循环、上下文和图片

网关转发有界工具定义、`tool_choice`、`parallel_tool_calls`；保存消息顺序和工具 ID 对应关系；流中参数按片转发，完成后校验完整 JSON 对象。客户端执行工具并回传结果，网关不执行本地文件读取、编辑或 Shell。任意工具增量发出后禁止切换供应商重试，避免重复工具调用。客户端自己管理工具权限。

每次请求携带完整所需历史，包含图片和工具结果；网关不保存对话记忆、不静默裁剪。最多 128 条消息、64 个工具定义、16 个单轮工具调用；单工具 Schema 32 KiB，工具参数流累计 64 KiB。HTTP 请求 2 MiB，输入总预算 65536 单位，输出最多 4096 token。预算包含工具 Schema、历史参数和图片 allowance；真实费用只按供应商 usage 结算。工具/图片请求绕过文本语义缓存。

图片支持 OpenAI `image_url` 和 Messages `image` 转换，受既有 PNG/JPEG、大小和 URL 校验约束。Messages 的工具结果目前仅接受文本；工具结果里的图片、PDF、音频、server tools、extended/adaptive thinking、Responses API、Claude 专属 reasoning blocks 不在支持范围，明确返回错误。基础图片协议此前已有千问真实识图证据；这次客户端验收没有另外宣称附件 UI 已验证。

Claude Code 会发送 `metadata`、`cache_control` 和 `output_config.effort`。metadata 不作为身份依据；cache_control 不向千问承诺 Anthropic 缓存行为；当前固定非思考模型不实现 effort，返回 `X-AgentMesh-Unsupported-Options: output_config.effort`。结构化 output_config.format 和非 disabled thinking 拒绝。缓存读 token 只来自真实回执，缺少 usage 返回错误并沿用待对账账务状态，不伪装免费成功。客户端自身显示的美元估价不代表人民币账单。

[Anthropic 官方网关说明](https://code.claude.com/docs/en/llm-gateway)不承诺通过网关将 Claude Code 用于非 Claude 模型的受支持体验。因此这里交付的是实测版本下的 Messages 转换与 Read 往返，不宣称千问等同 Claude 或获得官方完整支持。OpenCode 格式依据[官方自定义 Provider 文档](https://opencode.ai/docs/providers/#custom-provider)。客户端升级后应围绕变化核实协议。

## 自动检查

```powershell
python scripts/test-client-config.py
go test ./internal/provider/... ./internal/gateway ./internal/modelrpc ./internal/router ./internal/runtime
```

以上不调用付费模型。私有真实测试保留累计预算账本、两种客户端输出、供应商用量与钱包对应关系。公开仓库不包含真实 Key、私有配置、聊天内容或测试凭据。受控计费中继是测试设施，不是生产部署依赖。

另在零费用可控上游下，验证了客户端实际声明的多工具集合（本次 OpenCode 10 个、Claude Code bare 模式 3 个）可通过网关并完成 Read 往返；模型只选择 Read，不据此声称其他工具已执行验证。Linux 临时 MySQL/Redis/Kafka 全量 race 最终 300 项通过，2 项跳过（独立本机资源证据入口、仅由父测试调用的崩溃子进程入口）；必需真实依赖测试无缺失。配置测试在本机 6 项通过，Linux CI 缺少 PowerShell 时仅跳过脚本原生参数转发这一项。
