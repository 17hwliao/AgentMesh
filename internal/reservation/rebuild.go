package reservation

import (
	"context"
	"encoding/json"
	"errors"
)

// RebuildWallet is an OFFLINE operation: stop every API/relay/worker first.
// Durable grants and terminal ledger entries are authoritative. An interrupted
// active reservation conservatively holds its full budget until reconciliation.
func (r *Runtime) RebuildWallet(ctx context.Context, tenant string) error {
	if tenant == "" {
		return domainError(CodeStateInvalid)
	}
	rows, err := r.DB.QueryContext(ctx, "SELECT grant_id,units FROM tenant_quota_grants WHERE tenant_id=?", tenant)
	if err != nil {
		return err
	}
	var total uint64
	values := map[string]string{}
	deletes := make([]string, 0)
	for rows.Next() {
		var id string
		var amount uint64
		if err = rows.Scan(&id, &amount); err != nil {
			rows.Close()
			return err
		}
		if amount > 1<<52 || total > (1<<52)-amount {
			rows.Close()
			return errors.New("quota_total_overflow")
		}
		total += amount
		values[quotaKeyPrefix+":grant:"+tenant+":"+id] = "1"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if total == 0 {
		return errors.New("quota_grants_missing")
	}
	// Aggregate the entire terminal ledger in MySQL. Historical request count
	// must not make an otherwise small wallet impossible to restore. Only live
	// reservations need operation fences: every producer is stopped offline,
	// and reconciliation never reopens a durable terminal reservation.
	var spent uint64
	if err = r.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN state='settled' THEN settled_units WHEN state='cancelled' THEN 0 ELSE reserved_units END),0) FROM quota_reservations WHERE tenant_id=?`, tenant).Scan(&spent); err != nil {
		return err
	}
	if spent > total {
		return errors.New("quota_ledger_exceeds_grants")
	}
	rows, err = r.DB.QueryContext(ctx, "SELECT reservation_id,state,reserved_units,settled_units,released_units FROM quota_reservations WHERE tenant_id=? AND state NOT IN ('settled','cancelled') ORDER BY created_at LIMIT 10001", tenant)
	if err != nil {
		return err
	}
	type item struct {
		id, state                   string
		reserved, settled, released uint64
	}
	var items []item
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.state, &it.reserved, &it.settled, &it.released); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(items) > 10000 {
		return errors.New("quota_rebuild_batch_limit")
	}
	available := total - spent
	encode := func(code string, released uint64) string {
		raw, _ := json.Marshal([]any{code, available, released})
		return string(raw)
	}
	for _, it := range items {
		values[reserveMarkerKey(tenant, it.id)] = "1"
		values[operationKey(tenant, it.id, 1, "reserve")] = encode("reserved", 0)
		terminal := operationKey(tenant, it.id, 1, "terminal")
		switch it.state {
		case "settled":
			values[terminal] = encode("settled", it.released)
		case "cancelled":
			values[terminal] = encode("cancelled", it.reserved)
		default:
			deletes = append(deletes, terminal)
		}
	}
	values[balanceKey(tenant)] = encodeNumber(available)
	values[walletVersionKey(tenant)] = "2"
	raw, _ := json.Marshal(values)
	remove, _ := json.Marshal(deletes)
	return r.Redis.Eval(ctx, rebuildLua, nil, string(raw), string(remove)).Err()
}
func encodeNumber(value uint64) string { raw, _ := json.Marshal(value); return string(raw) }

const rebuildLua = `local values=cjson.decode(ARGV[1])
local remove=cjson.decode(ARGV[2])
for _,key in ipairs(remove) do redis.call('DEL',key) end
for key,value in pairs(values) do redis.call('SET',key,value) end
return 1`
