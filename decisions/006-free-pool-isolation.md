# 决策 006：免费池与控制面分支隔离

**状态：** Accepted（S0，2026-11-16）
**基线：** `master` / `6f9ab49`
**分支：** `feat/standalone-control-plane`

## 背景

主工作区 `AgentMesh` 的 `feat/free-tier-catalog` 分支存在未提交的 `internal/freepool` 改动。这些改动尚未经过本分支评审，也没有形成可复用的稳定契约。控制面 S1a 需要先建立独立的 SQLite catalog、身份和 Provider 生命周期，不能把实验性免费池同时引入，否则会混合两个尚未收口的变更集。

## 决策

1. 免费池继续留在主工作区未提交改动中，不合并、不复制、不依赖。
2. `feat/standalone-control-plane` 的 Provider 目录以 SQLite catalog 为唯一来源。
3. S1a 不实现多平台 Key 轮询、免费额度账本或 429 自动切换；这些能力只在后续阶段用独立设计和测试评估。
4. 控制面测试不得引用主工作区的 `internal/freepool` 文件。
5. 如果后续需要把免费池导入 catalog，必须单独设计迁移和回滚路径，不能直接复用未提交的运行时状态。

## 理由

- 先冻结控制面契约，才能判断免费池是否需要新的 Provider 类型或额度接口。
- 免费池的运行时冷却状态和 catalog 的持久配置是不同生命周期，不能混在 Snapshot 中。
- 隔离后，控制面可以在干净基线上独立编译、测试和验收，不会被主工作区的未提交文件污染。

## 影响

- S1a 只支持直接 `providerSlug/upstreamModelID` 路由。
- S2 的配额和冷却使用自己的持久化与 lane 状态，不依赖 `freepool.Pool`。
- 主工作区的免费池改动保持原样，后续由用户单独验收。
