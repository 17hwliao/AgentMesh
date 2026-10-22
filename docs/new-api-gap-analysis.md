# 与 New API 对比及改进优先级

观察日期 2026-10-16。参考 [New API README](https://github.com/QuantumNous/new-api)及其 [relay 路由](https://github.com/QuantumNous/new-api/blob/main/router/relay-router.go)，并核对 [OpenCode](https://opencode.ai/docs/providers/#custom-provider)、[Claude Code](https://code.claude.com/docs/en/llm-gateway)官方契约。网络限制未取得参考仓库固定 commit，因此本表针对当日可读取的 main，不能作为不可变版本审计。未复制 New API 源码，不用其功能数量代替本项目真实验收。

用户本轮指定先测 OpenCode、Claude Code。顺序依据“用户拿到 Key 能否直接接入 → 是否完整且正确计费 → 多平台运维 → 产品运营”。New API 是功能广泛的成熟参考，不意味着其每一项都是求职项目的上线前置条件。

## 聚焦 Review：本轮修复的接入阻断

审查对象为本轮开始时本地源码快照，不把既有未提交改动全部记成本轮成果。以下为已确认发现及修复位置；原始快照和联调回执留在本地验证目录。

| 优先级 | 原有缺口与触发方式 | 本轮结果/定位 |
|---|---|---|
| P0 | `/v1/models` 缺失，Key 没有可发现的模型和能力 | 租户范围列表/详情及工具/视觉能力；`internal/gateway/models.go`、`internal/tenant/models.go` |
| P0 | 严格 DTO 拒绝 tools；无 tool_calls/tool_call_id，编程 Agent 首轮或回传失败 | 有界类型、历史配对、参数增量/终态校验；`internal/provider/tools.go`、`message.go` |
| P0 | SSE 缺标准元数据、finish_reason、usage，stream=false 拒绝 | 共享流式/非流式响应，标准 usage 加兼容旧别名；`internal/gateway/completion.go` |
| P0 | Claude Code 的 x-api-key、Messages 和命名 SSE 无入口 | `/v1/messages` 转换至同一鉴权、路由、钱包链路，避免双扣费；`internal/gateway/anthropic.go` |
| P0 | 工具 Schema/参数未计入预算，工具可能误入文本缓存 | 准入包含 Schema/历史/输出参数；禁用工具/复杂请求语义缓存；`reservation/coordinator.go`、`semanticcache/cache.go` |
| P0 | 只有自有 HTTP 客户端，不能证明知名 harness 可用 | 隔离配置生成、只读预检、两个实际 CLI Read 往返；`scripts/client-config.py` |

实测还发现 PowerShell 参数属性把 Claude 的 `-p` 绑定为公共参数，导致启动失败；已修复并增加真实 PowerShell 参数转发回归。失败未发出供应商请求。协议转换缺少 usage 时明确报错，不能把零 token 当成已确认免费成功。已有“发出任意 chunk 后禁止重试”规则新增纯工具增量回归。

## 完整差异清单与后续顺序

| 顺序 | 能力 | AgentMesh 当前情况 | 后续实现与最低证据 |
|---|---|---|---|
| P0 完成 | Key → 模型发现 → 客户端配置 | 本轮补齐；配置无明文 Key | 两个实际 CLI 通过，保留版本与只读权限范围 |
| P0 完成 | 文本/基础图片/历史/工具/流式及非流式 | Chat + Messages 支持有界子集；图片已有真实千问测试 | 未支持字段显式拒绝，不能任意透传 |
| P0 已有，扩展测试完成 | 余额/token 账单/冻结/取消/恢复 | 整数 CNY、价格版本、MySQL 持久化、Redis 准入、Kafka 用量 | 两种 CLI 扣款均等于真实回执，客户端 USD 估价不作账单 |
| P1 下一步 1 | Responses API / Codex CLI / 更多 SDK | 无 `/v1/responses`，不能宣称 Codex CLI 兼容 | 独立事件状态机、item/call_id、取消与计费，实际客户端验收；不把 Chat 改名冒充 Responses |
| P1 下一步 2 | 渠道管理 | 静态路由、顺序 fallback、健康检查、mTLS Runner 已有；缺权重/优先级/多 Key 池管理 | 持久渠道配置、版本热更、熔断冷却、选择统计；输出后不切换，重试有界 |
| P1 下一步 3 | 每 Key 精细控制 | 租户隔离/禁用/路由/限流/钱包已有；缺完整 Key 独立到期/IP/额度产品契约 | Key ID 策略、撤销、限模型/金额/RPM、可信代理/CIDR 与绕过回归 |
| P1 下一步 4 | 请求诊断 | 错误/trace/Prometheus/OTel/outbox/恢复已有；缺集中诊断入口 | 脱敏请求到 attempt/余额检索、供应商错误分类；不保存 Key/图片正文 |
| P1 下一步 5 | 长上下文和客户端特性 | 有界完整历史透传；无自动摘要/PDF/reasoning blocks/服务端会话 | 先按实际客户端需求实现一种；保持工具配对，摘要损失和成本有证据；当前超限拒绝 |
| P2 | 更多原生协议/模型任务 | 兼容供应商/Ark/Ollama/真实 embeddings；缺 Gemini 原生、音频/图片生成/视频/实时/通用 rerank | 每任务独立取消/admission/响应/账单，新 Provider 真实低预算联调及故障回归 |
| P2 | 自服务/控制台/Playground | 管理后端已有；缺完整渠道/Key/余额自助 UI | 从复制配置、列模型、余额和请求状态开始，复用服务端权限 |
| P2 | 商业运营 | 管理充值与可选支付接口已有；无本轮可证明的实际支付上线、订阅/促销/OAuth 自助闭环 | 取得商户信息后验证签名、幂等、退款/拒付、对账；模拟充值不写成收款成功 |
| P2 | 多机/公网生产证据 | Linux Compose/HTTPS 模板/监控/恢复已有；本轮为本机容器调用真实模型 | 云阶段再验真实域名、网络隔离、备份恢复/灰度/容量；不声称公网已上线 |

New API 的渠道、运营和协议覆盖更宽；AgentMesh 的重点是对外契约、工具状态机、成本冻结/结算/恢复与 SQL Sentinel 消费者链路。上表后置功能没有全部实现；一轮 Read 测试也不等于长任务代理或规模化生产验收。

## 本轮验证

OpenCode 1.19.0、Claude Code 2.1.234：实际客户端本地 Read → 模型工具结果往返通过。先用零费用可控上游验证分片，再用 qwen3.8-omni-flash 验证；新增 4 次真实调用费用 0.0047301 CNY，连同此前累计 0.008234375 CNY，原始 20 CNY 上限与账本未重置，无未结算冻结。

回归覆盖非法工具历史、参数交错/截断/超限、Schema 预算、标准流/非流式、Messages 错误/usage/截止时间、租户模型隔离、mTLS 工具、输出后禁止 fallback，以及配置余额/价格/能力/Key 保密和 CLI 参数转发。Linux race 使用临时 MySQL/Redis/Kafka，不暴露主机端口、不复用生产数据。

使用方式与已知协议限制见 [客户端接入](client-integration.md)；接口见 [OpenAPI](openapi.json)。未新建分支、提交、推送、PR、标签或 Release。
