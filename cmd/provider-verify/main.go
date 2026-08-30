package main

import (
	"encoding/json"
	"os"

	"agentmesh/internal/providerverify"
)

func main() {
	_, report, err := providerverify.Load(os.Getenv)
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		os.Exit(1)
	}
}
