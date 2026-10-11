package localbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Explicitly opt-in: these measurements are mock loopback baselines, not CI
// latency thresholds or database/model/cloud capacity claims.
func TestLocalResourceEvidence(t *testing.T) {
	dir := os.Getenv("LOCAL_BENCHMARK_OUTPUT_DIR")
	if dir == "" {
		t.Skip("explicit benchmark output directory required")
	}
	scenarios := []struct {
		name                 string
		chunks, bytes, delay int
	}{
		{"short-stream", 0, 0, 0}, {"long-stream", 64, 64, 0}, {"slow-reader", 16, 64, 2},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			c := DefaultConfig()
			c.Warmup = 8
			c.Rounds = 3
			c.Requests = 80
			c.Concurrency = 8
			c.RateRequests = 200
			c.RateBurst = 10
			c.StreamChunks = scenario.chunks
			c.ChunkBytes = scenario.bytes
			c.ReadDelayMS = scenario.delay
			report, err := Run(c, Environment{Commit: os.Getenv("LOCAL_BENCHMARK_REVISION")})
			if err != nil {
				t.Fatal(err)
			}
			if report.Resources.AllocatedBytes == 0 || report.Resources.Allocations == 0 {
				t.Fatal("resource sampling missing")
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, scenario.name+".json"), append(data, '\n'), 0600); err != nil {
				t.Fatal("evidence write failed")
			}
		})
	}
}
