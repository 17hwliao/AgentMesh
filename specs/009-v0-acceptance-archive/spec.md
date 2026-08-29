---
level: L2
feature: 009-v0-acceptance-archive
created: 2026-08-29
---

# V0 验收归档与能力边界汇总

**原始需求：** 在主干阶段完成后，汇总 README、决策记录和 V0 Definition of Done，对已验证、受控拒绝、部分实现和后置项作可审计归档。

## 目标

- 在 README 建立 001–008 的能力/证据/限制索引，链接现有公开决策和可复现命令。
- 将 `实施步骤.md` 的 V0 DoD 与已实现事实逐项对照，明确完成、部分完成、未验证或后置，不把路线图写成事实。
- 运行现有离线 demo 与全量验证，记录真实输出；受控拒绝仍保留为拒绝，不改写成通过。
- 修复归档中发现的 quota 关闭 typed-nil `StreamGate` 回归，使既有 stage-1 demo 恢复其已承诺的 mock/fallback 语义。

## 非目标

- 除上述已授权的 typed-nil 回归修复外，不新增业务功能、Provider、API、schema、Redis/MySQL 写入、Docker、性能压测或真实环境配置。
- 不实现 usage_outbox、三方对账、令牌桶、管理面、OTel/Prometheus、跨实例恢复或真实 Provider 调用。
- 不修改既有决策的结论，不 push、不 tag。

## 验收条件

1. README 有可导航的 V0 验收索引，逐阶段引用公开提交/命令或证据，并列明真实基础设施的未验证边界。
2. `实施步骤.md` 的 6 项 DoD 均有事实状态：未满足或仅部分满足的项保留具体原因与下一步，不使用“完成”掩盖。
3. README 汇总现有四份决策及其适用范围，读者无需猜测 Provider、Reservation、estimated usage 与 gRPC 的选择。
4. `make demo-stage-1`、`make demo-stage-4`、`make verify-real-storage` 与全量 Go 校验的结果被如实归档；最后一项无配置时仍是 exit 1 的受控拒绝。
5. 公开变更仅限 README、实施步骤、必要决策索引/测试与本 feature 规格；私有复盘、提交及 fast-forward `master` 后工作区干净。
6. quota 未启用时 `OpenConfiguredCoordinator` 返回可正确判等的 nil `StreamGate`；认证后的并发 HTTP/SSE fallback 回归测试为 200，且不触发 Reservation。

## 默认假设

- 历史路线图保留其规划价值；归档通过补充当前状态而非伪造历史任务执行记录。
- 本机仍没有 008 所需真实存储配置，除非环境事实改变，否则只记录既有 0 网络尝试的受控拒绝。
- 归档不需要 L3 专项：不改外部接口、数据、迁移或难以回退的架构决策。

## 实现范围与验证

- 涉及 README、实施步骤、`decisions/` 的导航性补充及必要的文档一致性测试；不改运行时语义。
- 收尾执行现有 Make targets、`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验。
