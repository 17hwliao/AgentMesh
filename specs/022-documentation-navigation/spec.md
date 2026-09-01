---
level: L1
feature: 022-documentation-navigation
created: 2026-09-01
---

# 文档导航与项目简历整理

**原始需求：** 整理 SQL Sentinel 与 AgentMesh 的文档，但不删除既有资料；产出一份可投递的项目型简历版本。

## 目标

- 只新增文档导航及 README 入口，分类指向既有 README、实施步骤、规格、决策和私有阶段资料。
- 提供一份仓库外、只含已验证项目事实的中文简历版本。

## 非目标

- 不改任何网关、Adapter、认证、配额或实验语义；不移动或删除历史文档。
- 不把 loopback/mock、单次 Ollama 或传输联桥扩写为生产或多 Provider 实证。

## 默认假设

- README 继续是公开说明主入口；`docs/README.md` 只用于导航。
- `.private/` 保持忽略，不作为公开复现材料。

## 验收

1. 导航可以定位所有主要公开文档及私有证据入口，原文件不删除。
2. 简历中的数据与 README/私有实证一致，并保留适用范围。
3. Adaptive 校验和 `git diff --check` 通过，两个 master 合并但不 push。

## 任务

- [x] T001 新增 AgentMesh 文档导航和 README 入口。
- [x] T002 与 SQL Sentinel 的导航、私有简历资料交叉核对。
- [x] T003 生成仓库外项目型简历，验证边界并提交。
