---
level: L1
feature: 021-single-provider-verification
created: 2026-08-31
---

# 单 Provider 真实验证预检修正

**原始需求：** 修复真实 Provider 验证入口无条件要求 Ark 与 Ollama 五项变量的问题，使本机 Ollama 可独立获得真实流式验证证据。

## 目标

- 新增必填 `AGENTMESH_REAL_PROVIDERS`，只接受 `ark`、`ollama` 或顺序固定的 `ark,ollama`；预检仅校验所选 Provider 的变量。
- `ollama` 仅要求 `OLLAMA_BASE_URL` 与 `OLLAMA_MODEL`；`ark` 仅要求 `ARK_BASE_URL`、`ARK_MODEL` 与 `ARK_API_KEY`；双 Provider 要求全部五项。
- 验证脚本以同一选择启动对应的 `--providers` 和 tenant route；仍只发送一条 chat，仍清理临时 Key/进程/文件，并只输出脱敏摘要。
- 缺选择、非法选择或缺少所选变量时，保持连接前 `verification_unavailable`、稳定码和 0 网络/Provider attempt。

## 非目标

- 不改变 Ark/Ollama Adapter、Gateway、002 的路由/fallback 语义、Provider API 或密钥处理；不新增真实 Provider、持久化、Docker 或 010 改动。
- 不把单次 Ollama 成功写成 Ark 成功、性能/成本/SLO 或生产可用性。

## 验收

1. 离线测试覆盖单 Ark、单 Ollama、双 Provider、缺失/非法选择与未选 Provider 变量不影响预检；配置/报告不序列化 endpoint、模型或 Key。
2. 脚本测试覆盖选择驱动的 `--providers`/route、单请求、finally 清理；README/DoD 如实记录选择方式与真实结果。
3. 用当前进程的 Ollama `qwen2.5:7b` 实际运行一次；记录脱敏摘要和退出码，成功或失败均如实留档。
4. 执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`、Adaptive 校验；私有复盘、提交并 fast-forward `master`，不 push/tag/Docker。

## 任务

- [x] T001 选择驱动的预检与离线配置测试。
- [x] T002 选择驱动的验证脚本、脚本级测试和 README/DoD。
- [x] T003 用本机 Ollama 运行验证并留存脱敏结果。
- [x] T004 全量验证、私有复盘、提交和 fast-forward `master`。
