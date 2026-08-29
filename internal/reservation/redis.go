package reservation

import (
	"context"
	"errors"
	"fmt"

	redis "github.com/redis/go-redis/v9"
)

const quotaKeyPrefix = "agentmesh:quota:v1"

// RedisQuotaStore owns the three Lua operations that change available units.
// It never decides whether Cancel is legal; the Coordinator must first prove
// that no Provider attempt started.
type RedisQuotaStore struct{ evaluator redisEvaluator }

type redisEvaluator interface {
	Eval(context.Context, string, []string, ...any) (any, error)
}

type goRedisEvaluator struct{ client redis.Scripter }

func (e goRedisEvaluator) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return e.client.Eval(ctx, script, keys, args...).Result()
}

func NewRedisQuotaStore(client redis.Scripter) *RedisQuotaStore {
	return newRedisQuotaStore(goRedisEvaluator{client: client})
}

func newRedisQuotaStore(evaluator redisEvaluator) *RedisQuotaStore {
	return &RedisQuotaStore{evaluator: evaluator}
}

type QuotaOperationResult struct {
	Code          string
	Available     uint64
	ReleasedUnits uint64
}

func (s *RedisQuotaStore) Reserve(ctx context.Context, tenantID, reservationID string, version, units uint64) (QuotaOperationResult, error) {
	if s == nil || s.evaluator == nil || tenantID == "" || reservationID == "" || version == 0 {
		return QuotaOperationResult{}, domainError(CodeStateInvalid)
	}
	result, err := s.runKeys(ctx, reserveLua, []string{
		balanceKey(tenantID), operationKey(tenantID, reservationID, version, "reserve"), reserveMarkerKey(tenantID, reservationID),
	}, units)
	if err != nil {
		return QuotaOperationResult{}, err
	}
	if result.Code == "quota_exhausted" {
		return result, domainError("quota_exhausted")
	}
	if result.Code != "reserved" {
		return QuotaOperationResult{}, fmt.Errorf("unexpected redis reserve result %q", result.Code)
	}
	return result, nil
}

func (s *RedisQuotaStore) Settle(ctx context.Context, tenantID, reservationID string, version, reservedUnits, consumedUnits uint64) (QuotaOperationResult, error) {
	if consumedUnits > reservedUnits {
		return QuotaOperationResult{}, domainError(CodeStateInvalid)
	}
	result, err := s.run(ctx, settleLua, tenantID, reservationID, version, "settle", reservedUnits, consumedUnits)
	if err != nil {
		return QuotaOperationResult{}, err
	}
	if result.Code != "settled" {
		return QuotaOperationResult{}, fmt.Errorf("unexpected redis settle result %q", result.Code)
	}
	return result, nil
}

func (s *RedisQuotaStore) Cancel(ctx context.Context, tenantID, reservationID string, version, units uint64) (QuotaOperationResult, error) {
	result, err := s.run(ctx, cancelLua, tenantID, reservationID, version, "cancel", units)
	if err != nil {
		return QuotaOperationResult{}, err
	}
	if result.Code != "cancelled" {
		return QuotaOperationResult{}, fmt.Errorf("unexpected redis cancel result %q", result.Code)
	}
	return result, nil
}

// ReserveApplied lets the reconciler distinguish a creating row left before
// Redis accepted the debit from one left between Redis reserve and MySQL
// reserved. It never changes a balance.
func (s *RedisQuotaStore) ReserveApplied(ctx context.Context, tenantID, reservationID string) (bool, error) {
	if s == nil || s.evaluator == nil || tenantID == "" || reservationID == "" {
		return false, domainError(CodeStateInvalid)
	}
	raw, err := s.evaluator.Eval(ctx, reserveAppliedLua, []string{reserveMarkerKey(tenantID, reservationID)})
	if err != nil {
		return false, err
	}
	value, ok := uintValue(raw)
	if !ok {
		return false, errors.New("invalid redis reserve marker")
	}
	return value == 1, nil
}

func (s *RedisQuotaStore) run(ctx context.Context, script, tenantID, reservationID string, version uint64, operation string, args ...any) (QuotaOperationResult, error) {
	if s == nil || s.evaluator == nil || tenantID == "" || reservationID == "" || version == 0 {
		return QuotaOperationResult{}, domainError(CodeStateInvalid)
	}
	return s.runKeys(ctx, script, []string{balanceKey(tenantID), operationKey(tenantID, reservationID, version, operation)}, args...)
}

func (s *RedisQuotaStore) runKeys(ctx context.Context, script string, keys []string, args ...any) (QuotaOperationResult, error) {
	raw, err := s.evaluator.Eval(ctx, script, keys, args...)
	if err != nil {
		return QuotaOperationResult{}, err
	}
	return decodeQuotaOperation(raw)
}

func balanceKey(tenantID string) string {
	return quotaKeyPrefix + ":balance:" + tenantID
}

func operationKey(tenantID, reservationID string, version uint64, operation string) string {
	return fmt.Sprintf("%s:operation:%s:%s:%d:%s", quotaKeyPrefix, tenantID, reservationID, version, operation)
}

func reserveMarkerKey(tenantID, reservationID string) string {
	return fmt.Sprintf("%s:reservation:%s:%s:reserved", quotaKeyPrefix, tenantID, reservationID)
}

func decodeQuotaOperation(raw any) (QuotaOperationResult, error) {
	items, ok := raw.([]any)
	if !ok || len(items) != 3 {
		return QuotaOperationResult{}, errors.New("invalid redis quota result")
	}
	code, ok := stringValue(items[0])
	if !ok {
		return QuotaOperationResult{}, errors.New("invalid redis quota code")
	}
	available, ok := uintValue(items[1])
	if !ok {
		return QuotaOperationResult{}, errors.New("invalid redis quota balance")
	}
	released, ok := uintValue(items[2])
	if !ok {
		return QuotaOperationResult{}, errors.New("invalid redis quota release")
	}
	return QuotaOperationResult{Code: code, Available: available, ReleasedUnits: released}, nil
}

func stringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func uintValue(value any) (uint64, bool) {
	switch typed := value.(type) {
	case int64:
		return uint64(typed), typed >= 0
	case uint64:
		return typed, true
	case int:
		return uint64(typed), typed >= 0
	default:
		return 0, false
	}
}

const reserveLua = `local prior = redis.call('GET', KEYS[2])
if prior then return cjson.decode(prior) end
local available = tonumber(redis.call('GET', KEYS[1]) or '-1')
local units = tonumber(ARGV[1])
if not available or available < units then return {'quota_exhausted', math.max(available or 0, 0), 0} end
local remaining = redis.call('DECRBY', KEYS[1], units)
local result = {'reserved', remaining, 0}
redis.call('SET', KEYS[2], cjson.encode(result))
redis.call('SET', KEYS[3], '1')
return result`

const settleLua = `local prior = redis.call('GET', KEYS[2])
if prior then return cjson.decode(prior) end
local reserved = tonumber(ARGV[1])
local consumed = tonumber(ARGV[2])
if not reserved or not consumed or consumed > reserved then return {'invalid', 0, 0} end
local released = reserved - consumed
local available = redis.call('INCRBY', KEYS[1], released)
local result = {'settled', available, released}
redis.call('SET', KEYS[2], cjson.encode(result))
return result`

const cancelLua = `local prior = redis.call('GET', KEYS[2])
if prior then return cjson.decode(prior) end
local units = tonumber(ARGV[1])
if not units then return {'invalid', 0, 0} end
local available = redis.call('INCRBY', KEYS[1], units)
local result = {'cancelled', available, units}
redis.call('SET', KEYS[2], cjson.encode(result))
return result`

const reserveAppliedLua = `if redis.call('GET', KEYS[1]) then return 1 end
return 0`
