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
	repository, operations, cleanup, err := reservation.OpenUsageLedger(context.Background(), config)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(reservation.FailedUsageLedgerReport(err))
		os.Exit(1)
	}
	defer cleanup()
	report, err := reservation.ReconcileUsageLedger(context.Background(), repository, operations, 100)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(reservation.FailedUsageLedgerReport(err))
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
