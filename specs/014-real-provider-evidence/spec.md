---
level: L2
feature: 014-real-provider-evidence
created: 2026-08-30
---

# Ark/Ollama 显式真实 Provider 联调证据

**原始需求：** 为 002 已有 Ark SSE 与 Ollama NDJSON Adapter 提供一次受控、可重跑的真实 Provider 联调入口；环境缺失时必须保持零网络受控拒绝。

## 目标

- 新增一个仅操作者显式运行的本地验证入口，复用既有 `cmd/api`、Provider Adapter、SSE client 与 Bootstrap 内存认证，不改变它们的生产语义。
- 预检 `ARK_BASE_URL`、`ARK_MODEL`、`ARK_API_KEY`、`OLLAMA_BASE_URL`、`OLLAMA_MODEL`；任一缺失或畸形时，在启动服务和 Provider 网络尝试前输出脱敏稳定拒绝摘要。
- 配置齐全时只启动 `127.0.0.1` 临时网关，发出一个固定、无用户数据的 stream 请求；最多依 002 规则尝试 Ark 主路由与首块前 Ollama fallback。
- 输出只含状态、尝试数、稳定结果码、Provider 名称与安全 trace 摘要；绝不输出或落盘 endpoint、模型、密钥、Bearer Key、prompt、delta 或上游原文。

## 非目标

- 不新增或修改 Ark/Ollama 协议、Provider 路由、fallback、Gateway HTTP 契约、tenant/key 存储、配额、Docker 或真实 MySQL/Redis 操作。
- 不承诺任一外部服务可达、不会为出绿配置凭据、不会把受控拒绝或单次成功写成性能/成本/生产可用证明。

## 默认假设

- 真实 mode 仍使用既有 `--providers ark,ollama`；mock 与真实 Adapter 不混排，tenant route 仅声明该有序 real route。
- 验证脚本临时生成 Bootstrap/API Key，并在 finally 中恢复所有它改写的环境变量与删除临时二进制/输出；它不读取或写入 `.env`、flags 或 Git。
- 单次真实运行由操作者在自己的 shell 配置变量后执行；凭据不经聊天传递。未配置环境的正确产物是 `verification_unavailable/provider_configuration_missing` 与 0 Provider 网络尝试。

## 验收条件

1. 缺任一 Provider 配置的预检路径不启动 API、不创建 HTTP client、不向 Ark/Ollama 发送请求，且只输出稳定脱敏码。
2. 完整配置路径只监听回环、只发送一个固定 stream 请求；首块前失败最多触发一次既有 fallback，首块后不得切换。
3. 临时 Bootstrap/API Key、Provider 配置、模型输出和上游错误不写入 stdout 摘要、文件、README 示例、日志或 Git；清理和环境恢复在成功、拒绝、超时和失败时均执行。
4. 离线测试覆盖预检、环境恢复、输出脱敏和尝试上限；README 将真实成功、受控拒绝与未运行明确分列。
5. 本机真实运行仅在变量实际齐全时记录真实结果；若变量缺失，报告受控拒绝而非伪造成功。

## 实现范围与验证

- 涉及受控本地验证脚本、离线测试/夹具、README 与私有运行记录；不引入新依赖或新服务端 API。
- 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；不 push、不 tag、不启动 Docker。
