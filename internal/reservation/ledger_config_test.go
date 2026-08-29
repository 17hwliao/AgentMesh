package reservation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageLedgerConfigRejectsBeforeAnyOpenAndDoesNotSerializeSecrets(t *testing.T) {
	lookups := 0
	_, err := LoadUsageLedgerConfig(func(string) string { lookups++; return "" })
	if Code(err) != CodeQuotaConfigurationMissing || lookups == 0 {
		t.Fatalf("code=%q lookups=%d", Code(err), lookups)
	}
	config := UsageLedgerConfig{MySQLDSN: "secret-dsn", RedisURL: "secret-url"}
	encoded, err := json.Marshal(struct {
		Config UsageLedgerConfig        `json:"config"`
		Report UsageLedgerCommandReport `json:"report"`
	}{Config: config, Report: UnavailableUsageLedgerReport(domainError(CodeQuotaConfigurationMissing))})
	if err != nil || strings.Contains(string(encoded), "secret") {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
}
