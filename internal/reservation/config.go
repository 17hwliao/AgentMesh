package reservation

import (
	"context"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	quotaModeEnvironment          = "AGENTMESH_QUOTA_MODE"
	quotaMySQLDSNEnvironment      = "AGENTMESH_QUOTA_MYSQL_DSN"
	quotaRedisURLEnvironment      = "AGENTMESH_QUOTA_REDIS_URL"
	quotaTenantUnitsEnvironment   = "AGENTMESH_BOOTSTRAP_TENANT_QUOTA_UNITS"
	quotaReservationUnitsEnv      = "AGENTMESH_RESERVATION_UNITS"
	quotaModeReservation          = "reservation"
	CodeQuotaConfigurationMissing = "quota_configuration_missing"
	CodeQuotaConfigurationInvalid = "quota_configuration_invalid"
)

// OpenConfiguredCoordinator keeps quota off unless explicitly opted in. When
// enabled, both stores are verified before the HTTP server starts; later store
// failures still reject the request before any Provider attempt.
func OpenConfiguredCoordinator(lookup func(string) string) (*Coordinator, func(), error) {
	if lookup == nil {
		return nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	mode := strings.TrimSpace(lookup(quotaModeEnvironment))
	if mode == "" {
		return nil, func() {}, nil
	}
	if mode != quotaModeReservation {
		return nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	dsn := strings.TrimSpace(lookup(quotaMySQLDSNEnvironment))
	redisURL := strings.TrimSpace(lookup(quotaRedisURLEnvironment))
	tenantID := strings.TrimSpace(lookup("AGENTMESH_BOOTSTRAP_TENANT_ID"))
	quotaUnits, quotaOK := positiveUnits(lookup(quotaTenantUnitsEnvironment))
	reservationUnits, reservationOK := positiveUnits(lookup(quotaReservationUnitsEnv))
	if dsn == "" || redisURL == "" || tenantID == "" || !quotaOK || !reservationOK {
		return nil, func() {}, domainError(CodeQuotaConfigurationMissing)
	}
	repository, db, err := OpenMySQLRepository(dsn, nil)
	if err != nil {
		return nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	cleanup := func() { _ = db.Close() }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		cleanup()
		return nil, func() {}, domainError(quotaUnavailableCode)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		cleanup()
		return nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	client := redis.NewClient(options)
	cleanup = func() { _ = client.Close(); _ = db.Close() }
	if err := client.Ping(ctx).Err(); err != nil {
		cleanup()
		return nil, func() {}, domainError(quotaUnavailableCode)
	}
	if _, err := client.SetNX(ctx, balanceKey(tenantID), quotaUnits, 0).Result(); err != nil {
		cleanup()
		return nil, func() {}, domainError(quotaUnavailableCode)
	}
	coordinator, err := NewCoordinator(repository, NewRedisQuotaStore(client), CoordinatorConfig{ReservationUnits: reservationUnits}, nil)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return coordinator, cleanup, nil
}

func positiveUnits(value string) (uint64, bool) {
	units, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return units, err == nil && units > 0
}

const quotaUnavailableCode = "quota_unavailable"
