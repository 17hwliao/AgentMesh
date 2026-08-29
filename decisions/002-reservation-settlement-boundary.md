# 决策 002：配额预扣与结算不是 TCC

**状态：** 已采用（006 核心 Reservation 路径）

未来 Reservation 必须先持久化 `creating`，再 Redis Lua 预扣，再转为 `reserved`；任何 Provider attempt 已发起后，不论首块前失败、流中断或取消，都保守结算为 `settled`。只有能证明未发起 attempt 的本地拒绝才允许 `cancelled`。

该流程涉及 MySQL、Redis 和异步恢复，属于最终一致性状态机，不称为 TCC。006 已落地 MySQL Reservation/attempt 证据、Redis Lua operation key、同步 attempt hook 与显式 reconciler；004 的内存 trace 仍只是诊断摘要，不能替代上述持久化证据。usage outbox、对账、令牌桶和跨实例演练仍未实现。
