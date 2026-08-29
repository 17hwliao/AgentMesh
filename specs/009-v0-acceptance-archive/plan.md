# 009 实施计划

## 归档原则

- 只补充“截至 `e354a85` 的当前事实”，不修改历史 feature 的结论、状态机或路线图原始任务框。
- 每个状态使用 `已验证`、`部分验证`、`受控拒绝` 或 `后置`；“有代码”“离线替身通过”“真实 endpoint 成功”分列，不能互相替代。
- 没有真实基础设施配置时，`make verify-real-storage` 的 exit 1 是预期证据，README 和 DoD 矩阵必须保留 0 网络尝试。
- 归档执行发现 008 的 quota-disabled typed-nil `*Coordinator` 被赋给 `StreamGate` 后绕过 nil 判断。按用户明确授权，修复为接口返回无类型 nil，并用真实 HTTP/SSE 并发回归测试锁定 stage-1 路径；这是阻断验收的最小修复，不扩展 Reservation 行为。

## 文档变更

1. 修复 `OpenConfiguredCoordinator` 的返回类型和两个回归测试，先恢复被中断的 stage-1 demo；README 新增 V0 归档入口：001–008 的能力、公开提交、可复现命令和限制；随后列出现有四份 decision 的链接与用途，以及仅两项明确后置工作（usage outbox/三方对账、操作者提供的真实 MySQL/Redis 实证）。
2. `实施步骤.md` 顶部改为当前归档状态，并在原始 V0 DoD 后插入六行事实矩阵：两个 CLI/mock 网关为已验证；Ark/Ollama 为 fixture/受控真实配置；鉴权/SSE/fallback/Reservation 为离线或受控验证；取消与内存 trace 为已测边界；全量真实数据口径为部分，原因是存储 endpoint 未配置。原始未勾选路线图保留。
3. 不新造 decision；README 仅导航 `001-provider-adapter-boundary`、`002-reservation-settlement-boundary`、`003-grpc-deferred`、`004-estimated-usage-no-refund`，以免摘要替代决策正文。

## 验证顺序

1. 执行 `make demo-stage-1` 与 `make demo-stage-4`，保留实际输出中 mock/确定性替身边界；执行 `make verify-real-storage`，接受并记录无配置时的稳定 exit 1。
2. 以 `rg` 核对 README 的八阶段、四决策、两后置项和 DoD 六状态；确认没有“真实 endpoint 已通过”“Docker 已启动”“usage outbox 已实现”等虚假表述。
3. 收尾执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive 校验；写私有复盘、提交并 fast-forward `master`，不 push/tag。

## 风险与章程

归档最大风险是把旧 README 的建议架构或 mock 结果叙述为已交付。本批只增证据链接和明确限制，发现不一致先修正文档而非改变运行时。章程暂未建立，不阻塞实施。
