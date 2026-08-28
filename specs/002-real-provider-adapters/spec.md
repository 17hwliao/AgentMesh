---
level: L2
feature: 002-real-provider-adapters
created: 2026-08-28
---

# Ark 与 Ollama 真实 Provider Adapter

## 原始需求

在已验证的 mock SSE 网关上，接入 Ark/豆包与 Ollama 两个独立 Provider，使既有 Router 能在首个输出前按显式顺序 fallback，同时保持离线可测和零凭据落盘。

## 目标

- 为 Ark 与 Ollama 分别实现 `provider.Provider` Adapter：请求映射、流式 chunk 解码、Context 取消和健康检查。
- 通过显式 `--providers` 选择 `mock` 或逗号分隔的真实 Provider 顺序；保留 mock 为默认，真实调用必须显式 opt-in。
- 从环境变量读取每个 Adapter 的 endpoint、模型和 Ark 密钥；配置缺失时返回稳定拒绝码，且不创建网络请求。
- 沿用 001 Router 语义：首 chunk 前的上游失败可 fallback，首 chunk 后仅 `stream_interrupted`。
- 用 `httptest` fixture 覆盖两种上游协议、错误帧、取消和 health；真实运行只作为 T003 受控实验。

## 非目标

- 不实现 API Key、租户、Redis、MySQL、配额、账单、Token 精确计费、Eino、管理面、Docker 编排或第三个 Provider。
- 不把密钥写入 flags、配置文件、日志、错误响应、README 示例、测试 fixture 或 Git；不在流开始后切换 Provider。

## 默认假设

- `--providers mock` 为默认；`--providers ark,ollama` 仅在操作者显式选择时加载真实配置。
- Ark 配置为 `ARK_BASE_URL`、`ARK_MODEL`、`ARK_API_KEY`；Ollama 配置为 `OLLAMA_BASE_URL`、`OLLAMA_MODEL`。所有值都只从进程环境读取。
- 测试使用本地 `httptest`；T003 若没有安全可用的环境配置，输出配置缺失拒绝码并记录 0 网络尝试，而不伪造真实 Provider 成功。

## 验收条件

- 两个 Adapter 都能用 fixture 解析流式文本、上游错误和 health 结果，并在请求 Context 取消后停止读取。
- 非 mock Provider 的缺失/无效配置在服务启动前以稳定码拒绝；日志和响应不含 endpoint 中的凭据或 API Key。
- 指定 `ark,ollama` 时，Ark 首块前失败能由 Ollama fixture 完成；Ark 已输出首块后失败不调用 Ollama。
- `GET /health/providers` 只返回 Provider 名称与健康状态，不返回配置或密钥。
- README 说明 opt-in 环境变量、mock 默认值、真实运行结果或受控拒绝原因，以及真实调用不等于计费/配额能力。
