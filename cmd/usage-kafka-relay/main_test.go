package main

import "testing"

func TestLoadConfig(t *testing.T) {
	values := map[string]string{
		mysqlDSNEnvironment: "user:pass@tcp(mysql:3306)/agentmesh",
		brokersEnvironment:  "kafka:9092, kafka-2:9092",
		intervalEnvironment: "250ms",
		batchEnvironment:    "32",
	}
	cfg, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadConfig error = %v", err)
	}
	if cfg.Interval.String() != "250ms" || cfg.Batch != 32 || len(cfg.Brokers) != 2 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadConfigRejectsIncompleteOrInvalidValues(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("empty config was accepted")
	}
	values := map[string]string{mysqlDSNEnvironment: "dsn", brokersEnvironment: "broker", intervalEnvironment: "zero"}
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("invalid interval was accepted")
	}
}
