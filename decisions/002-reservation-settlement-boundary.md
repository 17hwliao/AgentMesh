# 决策 002：配额预扣与结算不是 TCC

**状态：** 后续 L3 的已定边界，尚未实现

未来 Reservation 必须先持久化 `creating`，再 Redis Lua 预扣，再转为 `reserved`；任何 Provider attempt 已发起后，不论首块前失败、流中断或取消，都保守结算为 `settled`。只有能证明未发起 attempt 的本地拒绝才允许 `cancelled`。

该流程涉及 MySQL、Redis 和异步恢复，属于最终一致性状态机，不称为 TCC。004 的内存 trace 只是诊断摘要，不能替代持久化 attempt 痕迹、余额变更、reconciler 或账单依据。
