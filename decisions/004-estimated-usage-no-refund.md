# 决策 004：估算用量不能证明退款

**状态：** 已采用（006）

Provider 未返回完整且非 estimated usage 时，网关保存已成功 SSE 转发的 rune 下界、attempt started evidence 与 heartbeat，统一标记为 `estimated`。这些数据可支持故障诊断和 reconciler 的保守 `settled(estimated)`，但不是 tokenizer 估值，更不是 Provider 精确 Token 账单。

因此 006 不根据该下界释放预扣额度：只有所有 attempt 的 Provider usage 都完整、非 estimated 时，才结算并退款 `reserved - consumed`。这会在不确定场景对租户更保守，却避免将可能已经被上游消耗的额度释放；后续若引入经验证 tokenizer 或账单对账，必须作为独立 L3 变更重新评估该策略。
