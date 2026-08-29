package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"agentmesh/internal/reservation"
)

func main() {
	report, err := reservation.ValidateRealStorage(context.Background(), os.Getenv, filepath.Join("migrations", "001_quota_reservations.sql"))
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		os.Exit(1)
	}
}
