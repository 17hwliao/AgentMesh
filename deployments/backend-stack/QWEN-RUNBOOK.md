# 千问初次上线配置

OpenCode 与 Claude Code 接入见 [客户端配置和真实测试范围](../../docs/client-integration.md)。支持工具的实际模型在初始化时添加 `--qwen-tools`，支持图片再添加 `--qwen-vision`；已有部署显式更新对应别名声明并重建服务，不覆盖已有私有配置。

聊天与 SQL 分析共用百炼千问，逻辑 Provider 为 `compatible`。调用方继续使用网关应用 key，百炼 key 只挂载到 gateway。`sql-review` 与 `domestic-chat` 两个别名默认映射 `qwen-plus`；模型 ID 可在初始化时指定。默认保留 Ollama embedding，也可按下节改用云端 embedding。Ollama 可以部署在服务器或可信私网，不能向公网暴露无认证接口。

官方资料核对日期：2026-10-11。

- [首次调用与账户地域、业务空间地址](https://help.aliyun.com/zh/model-studio/first-api-call-to-qwen)
- [OpenAI 兼容 Chat 参数和流式返回](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions)
- [思考模式](https://help.aliyun.com/zh/model-studio/deep-thinking)
- [缓存 token 用量](https://help.aliyun.com/zh/model-studio/context-cache)

## 初始化

准备域名、百炼 key 的私有文件、控制台显示的完整 OpenAI 兼容 Base URL、可调用模型 ID，以及可达的 Ollama embedding 服务。Base URL 包含账户地域及可能的业务空间 ID；不要直接使用文档中的 `{WorkspaceId}` 占位符，也不要把 `/chat/completions` 拼入 Base URL。API Key 与地域/业务空间的权限需匹配。

在**全新部署目录和新的 Compose project**中执行。源码目录仍须保持 `AgentMesh/`、`sql-sentinel/`、`deploy/` 同级。替换下列占位值，禁止使用本机验收 `.env`、secrets 和 volumes。

```sh
cd deploy
export COMPOSE_PROJECT_NAME=internship-qwen
export COMPOSE_FILE=compose.yml:qwen.yml
python3 setup.py --chat-provider qwen --domain api.example.com \
  --qwen-key-file /secure/bailian-key \
  --qwen-base-url 'https://YOUR_WORKSPACE.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' \
  --qwen-model qwen-plus \
  --ollama-url http://YOUR_PRIVATE_OLLAMA:11434 \
  --embedding-model nomic-embed-text --embedding-dimension 768
docker compose config --quiet
docker compose build am-migrate ss-migrate proxy
docker compose up -d control baseline candidate redis kafka
docker compose up am-migrate ss-migrate topics
docker compose --profile setup run --rm seed
docker compose up -d gateway usage-relay usage-worker sentinel-api sentinel-relay sentinel-worker proxy
docker compose ps
python3 commission.py --url https://api.example.com
```

Compose v2.24.4+，Linux 环境要求与 [联合部署手册](README.md)一致。`seed` 仅用于新建、隔离的演示影子库。embedding 模型必须事先拉取，返回维度应与配置一致。HTTPS 验收会执行真实聊天、embedding 和 SQL 任务，消耗模型额度。Base URL、key、实际网络、模型权限或影子库缺失时，不能宣称真实云端验收通过。

`qwen.yml` 使用独立 `:qwen` 镜像标签，便于与现有本机验收镜像区分。始终带同一 `COMPOSE_FILE` 和 project 执行后续命令。不要与 `acceptance.yml` 或完整 `extensions.yml` 混用；完整扩展拓扑仍按其独立手册部署。

## 接口与输出控制

### 全云向量配置（2026-10-15）

在全新部署目录中，初始化时加入 `--embedding-provider compatible`，即可与聊天共用同一百炼工作空间地址和私有 Key，无需 Ollama。默认向量模型为 `qwen3.7-text-embedding-flash`、512 维；可通过 `--embedding-model` 和 `--embedding-dimension` 明确指定。该 Flash 模型只接受 256/512/768/1024 维。示例：

```sh
python3 setup.py --chat-provider qwen --domain api.example.com \
  --qwen-key-file /secure/bailian-key \
  --qwen-base-url 'https://YOUR_WORKSPACE.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' \
  --qwen-model qwen3.8-omni-flash --embedding-provider compatible
```

网关将租户的 `embedding` 别名绑定到服务端配置，发送真实模型 ID、`dimensions` 和 `encoding_format=float`。每批最多 20 条，响应按 index 还原顺序，严格校验数量、维度和有限数值；拒绝重定向，不返回供应商原始错误正文。新配置应使用新向量数据空间并重新向量化，不能把旧 Ollama 向量与同维度的新模型向量混合。

使用 `qwen-billing.yml` 时，云向量初始化生成的 `EMBEDDING_CNY_BILLING=true` 会启用向量人民币账本；已有部署需显式设置该变量。价格目录增加 `provider=compatible, model=embedding, upstream_model=qwen3.7-text-embedding-flash`，输入每百万 token 单价 `input_nano_per_million=125000000`、输出价格为 0、`max_input_tokens=65536`，不填写缓存输入价；这对应 2026-10-15 核实的北京区公开价，实际部署仍需核实账户价格并设置目录有效期。每个上游批次单独记录实际 usage；未收到可信 usage 时保留冻结金额，等待回执核销。语义缓存查找使用云向量时也会产生向量扣费，聊天缓存命中只免去再次生成的聊天费用。Ollama 或未启用金额账本时继续使用原预算单位。

官方模型与价格：[Qwen3.8 Omni](https://help.aliyun.com/zh/model-studio/qwen3-8-omni-flash)、[Embedding Flash](https://help.aliyun.com/zh/model-studio/qwen3-7-text-embedding-flash)、[北京区价格](https://help.aliyun.com/zh/model-studio/model-pricing)。本模型目前只通过本网关的纯文本接口使用，不开放音视频输入或工具执行。

公开聊天接口仍是 `POST /v1/chat/completions`，请求 `model=domestic-chat` 或 `sql-review`、`stream=true`，使用本项目应用 key。SQL Sentinel 的 `EINO_MODEL=sql-review` 保持不变，通过网关调用千问。

初始化默认非思考模式：HTTP 请求顶层 `enable_thinking=false`，适合当前聊天演示和 SQL JSON 输出。选择支持关闭思考的文本模型；仅思考模型需要另行评估超时、输出与收费规则。内部解析不把 `reasoning_content` 当作回答，但 `completion_tokens` 中包含的思考用量仍按上游实际总输出统计。

默认发送 `max_tokens`，兼容既有模型。官方支持 `max_completion_tokens` 的较新模型可以加初始化参数 `--qwen-use-max-completion-tokens`；只发送一种限额字段。确认账户模型支持该字段后再启用，不按名称自动猜测。网关仍限制单请求输出最多 4096 token，适配器 HTTP 调用总超时 30 秒；初始演示使用非思考模式，耗时超出该上限会按现有中断规则处理。

流式调用始终发送 `stream_options.include_usage=true`。只转发非空回答；最终读取 `prompt_tokens`、`completion_tokens`、`prompt_tokens_details.cached_tokens`。不完整、负数、越界或重复用量将失败，避免错误结算。上游错误正文与密钥不返回给用户；认证错误、限流、账户余额等具体原因通过百炼账户侧核实。

## 余额收费（可选）

基础千问部署使用现有 token 预算门禁。若启用此前实现的人民币余额扣费，先根据**实际账户地域、模型、非思考模式、输入区间和缓存规则**确认价格，在私有 `secrets/billing_catalog` 中提供有效 CNY 价格目录，然后使用：

```sh
export COMPOSE_FILE=compose.yml:qwen.yml:qwen-billing.yml
docker compose config --quiet
docker compose up -d gateway
```

目录的 `prices` 必须包含 `provider=compatible` 的 `sql-review` 和 `domestic-chat` 两项，`upstream_model` 与 `COMPATIBLE_MODELS` 中实际 ID 一致。每百万 token 的人民币金额乘以 1,000,000,000，写为整数 `input_nano_per_million`、`output_nano_per_million` 和 `cached_input_nano_per_million`。填写来源和不超过 93 天的到期时间；输入上限必须落在所确认的单一价格区间内。价格目录结构及管理充值接口见 [账本接口文档](../AgentMesh/docs/backend-roadmap.md)。

启用后先经私有管理入口为 operator 充值；没有余额不会自动免单。根据真实 token 扣款、缓存输入折价；缺失/不可信用量保留待核实金额，不虚构精确账单。本配置没有接入在线支付收款。不要直接把旧示例价格当作当前账户价，也不要把初始 token 配额当作人民币余额。

## 已有部署与验证边界

`setup.py` 保留已有文件，不会轮换数据库密码或模型密钥。已有 Ark/fixture 目录切换 Qwen 时直接拒绝。数据库中的 operator 路由也不会因为重跑迁移自动改写；已有部署须先备份并明确迁移已有租户和价格绑定，不能删除 volumes 来绕过。

本次本地验证覆盖官方响应形状、输出参数、租户别名、密钥挂载、可选账本覆盖层、旧初始化兼容与文件保留。无需真实 key 的检查可运行 `python3 test-qwen-setup.py`。2026-10-15 已使用账户真实千问及云向量完成本地 Linux 容器联调；真实域名和云服务器部署仍需服务器资料。测试凭证保存在本地验收目录，源码包不包含模型 Key。


### 基础识图和多轮上下文（2026-10-15）

`qwen3.8-omni-flash` 支持图片输入、文本回答。新部署显式添加 `--qwen-vision` 可为 `domestic-chat` 开启图片；现有部署设置 `COMPATIBLE_VISION_MODELS='["domestic-chat"]'` 并重建 gateway。不自动为所有模型/SQL 别名开放图片。若使用独立 model-runner，双方均须声明相同视觉别名并更新镜像。

内容兼容字符串或 `[{"type":"text","text":"问题"},{"type":"image_url","image_url":{"url":"https://公共图片地址"}}]`。也支持完整 PNG/JPEG Base64 data URL。每次请求需携带完整历史；网关不保存会话或静默压缩。图片跨 HTTP/mTLS 保留，图片请求绕过现有文本语义缓存，按供应商实际 prompt_tokens 计费。普通预算单位模式须将 `QWEN_RESERVATION_UNITS=65536`；新安装 `--qwen-vision` 自动生成该值，人民币钱包沿用有效价格目录。

图片及上下文边界、内部调用顺序、计费与 OpenCode 工具设计见源码 `docs/gateway-protocol-design.md`。当前未适配或验收 OpenCode 客户端，tools/tool_calls 仍明确拒绝。
