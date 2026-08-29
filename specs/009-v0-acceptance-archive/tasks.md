# 009 任务清单

- [x] T001：更新 README 的 001–008 验收索引、四决策导航和两项后置边界；更新实施步骤的当前状态与六项 DoD 事实矩阵，不改历史任务框。
- [x] T002：修复 quota 关闭时的 typed-nil `StreamGate` 并覆盖 nil 返回、并发真实 HTTP/SSE fallback 回归；随后执行 `make demo-stage-1`、`make demo-stage-4`、`make verify-real-storage`，以 `rg` 复核八阶段/四决策/两后置项与禁止虚假表述，并将真实输出或受控拒绝如实写回文档。
- [x] T003：全量格式/build/vet/test/diff、Adaptive 校验、私有阶段复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。
