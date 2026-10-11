# 千问初次上线配置

聊天与 SQL 分析共用百炼千问，逻辑 Provider 为 `compatible`。调用方继续使用网关应用 key，百炼 key 只挂载到 gateway。`sql-review` 与 `domestic-chat` 两个别名默认映射 `qwen-plus`；模型 ID 可在初始化时指定。此方案保留 Ollama embedding，不需要在服务器运行聊天大模型。Ollama 可以部署在服务器或可信私网，不能向公网暴露无认证接口。

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

本次本地验证覆盖官方响应形状、输出参数、租户别名、密钥挂载、可选账本覆盖层、旧初始化兼容与文件保留。无需真实 key 的检查可运行 `python3 test-qwen-setup.py`。真实千问、真实域名和云服务器验收仍需账户及服务器资料。
