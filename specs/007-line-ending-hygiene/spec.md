---
level: L1
feature: 007-line-ending-hygiene
created: 2026-08-29
---

# Go 源码行尾规范化

**原始需求：** 以独立卫生批次消除 Windows 工作区的 CRLF 格式校验噪音。

## 目标

- 通过仓库级 `.gitattributes` 将受版本控制的 `*.go` 规范为 LF 行尾。
- 规范化现有 Go 源码，使 `gofmt -l .` 不再因行尾差异报告旧文件。
- 保持所有业务逻辑、测试语义、依赖和运行时行为不变。

## 非目标

- 不更改全局 Git 配置、Docker、数据库、Redis 或 Provider 配置。
- 不修改非 Go 文件的既有行尾策略，也不进行业务功能重构。
- 不 push、不打 tag。

## 验收标准

1. `.gitattributes` 明确声明 `*.go text eol=lf`。
2. 仅 `.gitattributes` 与受版本控制的 Go 源码可产生公开变更；忽略行尾后不得有源码内容差异。
3. `gofmt -l .` 无输出，且 `go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 均通过。
4. 阶段复盘如实记录规范化范围与验证结果；特性分支提交后 fast-forward `master`。

## 默认假设

- Git 的全局 `autocrlf` 设置不在本批控制范围；仓库属性优先保证 Go 文件在检出与提交时使用 LF。
- 现有 CRLF 报告是工作区卫生问题，不代表 Go 格式或业务缺陷。

## 任务

- [x] T001 新增仓库级 Go 行尾属性并规范化已跟踪 Go 文件。
- [x] T002 完成全量验证、私有阶段复盘、提交并 fast-forward `master`。
