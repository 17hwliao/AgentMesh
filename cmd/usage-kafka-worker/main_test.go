package main

import "testing"

func TestLoadConfigUsesSafeDefaults(t *testing.T) {
	values := map[string]string{mysqlDSNEnvironment: "user:pass@tcp(mysql:3306)/agentmesh", brokersEnvironment: "kafka:9092"}
	cfg, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadConfig error = %v", err)
	}
	if cfg.GroupID != "agentmesh-usage-projection-v1" || cfg.MaxAttempts != 3 || len(cfg.Brokers) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadConfigRejectsInvalidMaxAttempts(t *testing.T) {
	values := map[string]string{mysqlDSNEnvironment: "dsn", brokersEnvironment: "broker", maxAttemptsEnvironment: "0"}
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("zero max attempts was accepted")
	}
}
