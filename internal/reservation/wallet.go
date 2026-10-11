package reservation

import (
	"agentmesh/internal/provider"
	"context"
	"database/sql"
	"errors"
	redis "github.com/redis/go-redis/v9"
	"strconv"
	"strings"
	"time"
)

// Runtime retains durable grant history. Loss of a used Redis wallet is a
// recovery incident, never permission to issue the bootstrap quota again.
type Runtime struct {
	*Coordinator
	DB           *sql.DB
	Redis        *redis.Client
	DefaultUnits uint64
}

func (r *Runtime) Health(ctx context.Context) error {
	if err := r.DB.PingContext(ctx); err != nil {
		return errors.New("quota_store_unavailable")
	}
	if err := r.Redis.Ping(ctx).Err(); err != nil {
		return errors.New("quota_store_unavailable")
	}
	rows, err := r.DB.QueryContext(ctx, "SELECT DISTINCT tenant_id FROM tenant_quota_grants LIMIT 10001")
	if err != nil {
		return errors.New("quota_store_unavailable")
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var tenant string
		if rows.Scan(&tenant) != nil {
			return errors.New("quota_store_unavailable")
		}
		keys = append(keys, walletVersionKey(tenant), balanceKey(tenant))
	}
	if len(keys) > 20000 || rows.Err() != nil {
		return errors.New("quota_store_unavailable")
	}
	if len(keys) > 0 {
		values, err := r.Redis.MGet(ctx, keys...).Result()
		if err != nil {
			return errors.New("quota_store_unavailable")
		}
		for i := 0; i < len(values); i += 2 {
			version, ok := values[i].(string)
			balance, balanceOK := values[i+1].(string)
			if !ok || !balanceOK || version != "2" {
				return errors.New("quota_recovery_required")
			}
			amount, err := strconv.ParseUint(balance, 10, 64)
			if err != nil || amount > 1<<52 {
				return errors.New("quota_recovery_required")
			}
		}
	}
	return nil
}

func (r *Runtime) Provision(ctx context.Context, tenant string) error {
	return r.Grant(ctx, tenant, "initial", r.DefaultUnits)
}

func (r *Runtime) Grant(ctx context.Context, tenant, id string, units uint64) error {
	if tenant == "" || len(tenant) > 64 || id == "" || len(id) > 128 || strings.ContainsAny(id, ":\r\n") || units == 0 || units > 1<<52 {
		return domainError(CodeStateInvalid)
	}
	var enabled bool
	if err := r.DB.QueryRowContext(ctx, "SELECT enabled FROM tenants WHERE tenant_id=?", tenant).Scan(&enabled); err != nil || !enabled {
		return errors.New("tenant_not_enabled")
	}
	exists, err := r.Redis.Exists(ctx, balanceKey(tenant)).Result()
	if err != nil {
		return err
	}
	version, _ := r.Redis.Get(ctx, walletVersionKey(tenant)).Result()
	if exists == 0 || version != "2" {
		var history int
		if err := r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_reservations WHERE tenant_id=?", tenant).Scan(&history); err != nil {
			return err
		}
		if history > 0 {
			return errors.New("quota_recovery_required")
		}
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize total issuance per tenant, including grants whose Redis step
	// has not run yet. Available balance alone cannot bound durable issuance.
	if err = tx.QueryRowContext(ctx, "SELECT enabled FROM tenants WHERE tenant_id=? FOR UPDATE", tenant).Scan(&enabled); err != nil || !enabled {
		return errors.New("tenant_not_enabled")
	}
	var total uint64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(units),0) FROM tenant_quota_grants WHERE tenant_id=?", tenant).Scan(&total); err != nil {
		return err
	}
	var prior uint64
	priorErr := tx.QueryRowContext(ctx, "SELECT units FROM tenant_quota_grants WHERE tenant_id=? AND grant_id=?", tenant, id).Scan(&prior)
	if priorErr != nil && priorErr != sql.ErrNoRows {
		return priorErr
	}
	if priorErr == sql.ErrNoRows && (total > 1<<52 || units > (1<<52)-total) {
		return errors.New("quota_total_overflow")
	}
	if priorErr == nil && prior != units {
		return errors.New("quota_grant_conflict")
	}
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO tenant_quota_grants (tenant_id,grant_id,units) VALUES (?,?,?)", tenant, id, units); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var stored uint64
	if err := r.DB.QueryRowContext(ctx, "SELECT units FROM tenant_quota_grants WHERE tenant_id=? AND grant_id=?", tenant, id).Scan(&stored); err != nil {
		return err
	}
	if stored != units {
		return errors.New("quota_grant_conflict")
	}
	// Retry all durable grants, including a crash between the MySQL insert and
	// the Redis script. Each grant has a stable independent operation key.
	rows, err := r.DB.QueryContext(ctx, "SELECT grant_id,units FROM tenant_quota_grants WHERE tenant_id=? ORDER BY created_at,grant_id", tenant)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grantID string
		var amount uint64
		if err := rows.Scan(&grantID, &amount); err != nil {
			return err
		}
		if err := r.Redis.Eval(ctx, grantLua, []string{balanceKey(tenant), quotaKeyPrefix + ":grant:" + tenant + ":" + grantID, walletVersionKey(tenant)}, amount).Err(); err != nil {
			return err
		}
	}
	return rows.Err()
}

const grantLua = `if redis.call('EXISTS',KEYS[2])==1 then return 0 end
if tonumber(redis.call('GET',KEYS[1]) or '0')+tonumber(ARGV[1])>4503599627370496 then return redis.error_reply('quota_total_overflow') end
local balance=redis.call('INCRBY',KEYS[1],ARGV[1])
redis.call('SET',KEYS[2],'1')
redis.call('SET',KEYS[3],'2')
return balance`

func walletVersionKey(tenant string) string { return quotaKeyPrefix + ":wallet_version:" + tenant }

func (r *Runtime) Begin(ctx context.Context, tenant, model string, messages []provider.Message) (StreamSession, error) {
	exists, err := r.Redis.Exists(ctx, balanceKey(tenant)).Result()
	version, versionErr := r.Redis.Get(ctx, walletVersionKey(tenant)).Result()
	if err != nil || exists == 0 || versionErr != nil || version != "2" {
		return nil, errors.New("quota_recovery_required")
	}
	return r.Coordinator.Begin(ctx, tenant, model, messages)
}

func (r *Runtime) RunRecovery(ctx context.Context, interval, age time.Duration, observe func(error)) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			step, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := NewReconciler(r.records, r.quota).Reconcile(step, time.Now().Add(-age), 100)
			cancel()
			if err != nil && observe != nil {
				observe(err)
			}
		}
	}
}
