package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/localbench"
)

func TestDefaultOutputPathStaysPrivateAndTimestamped(t *testing.T) {
	path := defaultOutputPath(time.Date(2026, 8, 31, 12, 34, 56, 0, time.UTC))
	if path != filepath.Join(".private", "benchmark-results", "017-local-benchmark-20260831T123456Z.json") {
		t.Fatalf("path=%q", path)
	}
}

func TestWriteReportAtomicallyCreatesPrivateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := writeReport(path, localbench.Report{Scope: localbench.Scope, GitCommit: "test"}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(payload), `"git_commit": "test"`) {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}
