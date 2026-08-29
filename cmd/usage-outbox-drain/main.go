package main

import (
	"context"
	"encoding/json"
	"os"

	"agentmesh/internal/reservation"
)

func main() {
	config, err := reservation.LoadUsageLedgerConfig(os.Getenv)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(reservation.UnavailableUsageLedgerReport(err))
		os.Exit(1)
	}
	repository, _, cleanup, err := reservation.OpenUsageLedger(context.Background(), config)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(reservation.FailedUsageLedgerReport(err))
		os.Exit(1)
	}
	defer cleanup()
	projected, err := repository.DrainUsageOutbox(context.Background(), 100)
	report := reservation.UsageLedgerCommandReport{Status: "completed", Projected: projected}
	if err != nil {
		report.Status, report.Code = "operation_failed", reservation.Code(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		os.Exit(1)
	}
}
