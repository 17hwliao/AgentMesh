package reservation

import (
	"context"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// UsageLedgerConfig keeps endpoint values in memory only. It is deliberately
// excluded from JSON so command summaries cannot serialize credentials.
type UsageLedgerConfig struct {
	MySQLDSN string `json:"-"`
	RedisURL string `json:"-"`
}

type UsageLedgerCommandReport struct {
	Status          string `json:"status"`
	Code            string `json:"code,omitempty"`
	NetworkAttempts *int   `json:"network_attempts,omitempty"`
	Projected       int    `json:"projected,omitempty"`
}

func LoadUsageLedgerConfig(lookup func(string) string) (UsageLedgerConfig, error) {
	if lookup == nil {
		return UsageLedgerConfig{}, domainError(CodeQuotaConfigurationMissing)
	}
	config := UsageLedgerConfig{
		MySQLDSN: strings.TrimSpace(lookup(quotaMySQLDSNEnvironment)),
		RedisURL: strings.TrimSpace(lookup(quotaRedisURLEnvironment)),
	}
	if config.MySQLDSN == "" || config.RedisURL == "" {
		return UsageLedgerConfig{}, domainError(CodeQuotaConfigurationMissing)
	}
	return config, nil
}

// OpenUsageLedger opens both stores only after the pure configuration gate.
// Callers own the returned cleanup and must not log the input configuration.
func OpenUsageLedger(ctx context.Context, config UsageLedgerConfig) (*SQLRepository, *RedisQuotaStore, func(), error) {
	if config.MySQLDSN == "" || config.RedisURL == "" {
		return nil, nil, func() {}, domainError(CodeQuotaConfigurationMissing)
	}
	repository, db, err := OpenMySQLRepository(config.MySQLDSN, nil)
	if err != nil {
		return nil, nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	cleanup := func() { _ = db.Close() }
	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		cleanup()
		return nil, nil, func() {}, domainError(CodeQuotaConfigurationInvalid)
	}
	pingContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := db.PingContext(pingContext); err != nil {
		cleanup()
		return nil, nil, func() {}, domainError(quotaUnavailableCode)
	}
	client := redis.NewClient(options)
	cleanup = func() { _ = client.Close(); _ = db.Close() }
	if err := client.Ping(pingContext).Err(); err != nil {
		cleanup()
		return nil, nil, func() {}, domainError(quotaUnavailableCode)
	}
	return repository, NewRedisQuotaStore(client), cleanup, nil
}

func UnavailableUsageLedgerReport(err error) UsageLedgerCommandReport {
	code := Code(err)
	if code == "" {
		code = CodeQuotaConfigurationInvalid
	}
	zero := 0
	return UsageLedgerCommandReport{Status: "verification_unavailable", Code: code, NetworkAttempts: &zero}
}

func FailedUsageLedgerReport(err error) UsageLedgerCommandReport {
	code := Code(err)
	if code == "" {
		code = quotaUnavailableCode
	}
	return UsageLedgerCommandReport{Status: "operation_failed", Code: code}
}
