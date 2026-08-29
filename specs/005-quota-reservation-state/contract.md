# Reservation 状态与幂等契约

## 状态

| 状态 | 含义 | 可达后继 |
| --- | --- | --- |
| `creating` | 仅创建领域记录，未声称预扣 | `reserved`、`cancelled`、未来 `expired_pending` |
| `reserved` | 领域校验通过，005 不代表 Redis/余额预扣 | `settled`、`cancelled`、`attempt_started`、未来 `expired_pending` |
| `settled` | 终态：任一 attempt started 后的唯一安全终结 | 无 |
| `cancelled` | 终态：只证明没有 started attempt 的本地取消 | 无 |
| `expired_pending` | 非终态，等待 future reconciler 基于持久证据裁决 | future `settled` 或 `cancelled` |

`attempt_started` 是 `reserved` 上的持久语义事件，不是额外状态；在真正调用 Provider Adapter 前记录。005 的内存实现只模拟该顺序，未持久化。

## 幂等与拒绝

每个变更带 `reservation_id`、`expected_version` 和 operation。成功变更递增 version；相同三元组重放返回第一次结果，不再次创建 attempt 或变更终态。相同 id/version 的不同 operation、旧/未来 version、归属 tenant 不符均拒绝：`reservation_version_conflict` 或 `reservation_not_found`。非法状态变更为 `reservation_state_invalid`。

Cancel 在 `creating/reserved` 且 started attempt 为零时才成功；否则返回 `reservation_must_settle`。Settle 可用于已 started attempt 的成功、首块前失败、流中断、Context 取消和未知上游结果；终态重放不产生第二次效果。`expired_pending` 不允许普通 Cancel。

## 安全与范围

Repository 不保存 raw key、prompt、Provider body、endpoint 或 Token 文本。005 没有 HTTP endpoint、余额、Redis Lua、MySQL/SQLite 表、迁移、reconciler 或真实计费；所有返回仅为内部稳定领域码，不能写成生产配额结论。
