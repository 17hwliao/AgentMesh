# 实施计划

## 实现决策

- 新增 `internal/observability` 的同步、锁保护、固定容量 Recorder；record 在请求结束后才可查询，保存安全摘要和可选的 Provider-observed usage，不写任何 prompt/delta。
- 给 Router 增加可注入的 attempt/first-chunk/finish observer，并由 Gateway 在认证成功后生成 trace ID、把 tenant/model 元数据交给 Recorder；时钟和 ID 生成器可注入，测试不依赖时间或随机值。
- Gateway 在认证后的 chat 响应设置服务端生成的 `X-AgentMesh-Trace-ID`，并新增认证后的 trace 查询；访问者 tenant 与 record tenant 不一致、缺失或未完成一律返回同一 `trace_not_found` 404，查询路径不创建 Router/Provider attempt。
- 固定容量淘汰最旧的已完成 trace；满且没有可淘汰完成记录时新请求继续流式执行但不承诺可查询记录，以稳定内部状态标记，不能阻塞业务请求。
- `Makefile` 的 demo 使用当前 shell 临时生成 key、本机 loopback mock 和进程清理；私有 resume/FAQ 不入 Git；`decisions/` 放三份只基于已实现事实的短记录。
- 对新增 HTTP 行为使用 `contract.md`；Recorder 非持久化、无迁移且不改变配额状态机，因此不建 migration/ADR。

## 数据流

`authenticated chat → server trace ID → Recorder pending → Router attempt observer → first chunk/usage/result → completed safe trace → authenticated same-tenant GET`。

trace 只观察调用；它不能选择 Provider、扣减配额、重放请求或把未观测 usage 标成精确值。

## 任务顺序

1. 实现 Recorder、Router observer 和安全 record schema；覆盖容量、淘汰、usage 缺失、attempt/fallback/interrupted/cancel 的离线测试。
2. 接 Gateway trace 生命周期和 tenant-scoped HTTP 查询；覆盖认证、跨 tenant/未知/未完成同码 404、零 Provider 查询及不泄露。
3. 运行本机 mock trace 验收；实现 stage-1 demo、私有 resume/FAQ、三份 decisions，更新 README/plan，不接外部依赖。
4. 全量格式/build/vet/test、Adaptive 校验、私有阶段复盘、提交并 fast-forward `master`；不 push/tag。

## 风险与降级

- Recorder 是进程内诊断环，不是审计或计费存储；进程重启、容量淘汰和未完成记录不可查询是预期降级，必须写明。
- trace ID 不承担认证；认证仍由 003 的 API Key 中间件完成。任何记录过滤失败都以 404 收敛，宁可少见不可跨 tenant 多见。
- OTel/Prometheus、持久化 usage 和配额一致性需要独立规格；本阶段的 attempt 摘要不能替代后续 Reservation 的持久 attempt 痕迹。

## 章程

暂未建立；本 feature 不以章程为阻塞条件。
