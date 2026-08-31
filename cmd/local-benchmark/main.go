// Command local-benchmark records reproducible loopback mock measurements.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agentmesh/internal/localbench"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "local_benchmark_failed")
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer, now func() time.Time) error {
	if now == nil {
		return errors.New("benchmark_clock_missing")
	}
	config := localbench.DefaultConfig()
	flags := flag.NewFlagSet("local-benchmark", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.IntVar(&config.Warmup, "warmup", config.Warmup, "warmup requests")
	flags.IntVar(&config.Rounds, "rounds", config.Rounds, "measured rounds")
	flags.IntVar(&config.Requests, "requests", config.Requests, "requests per measured round")
	flags.IntVar(&config.Concurrency, "concurrency", config.Concurrency, "concurrent loopback requests")
	flags.IntVar(&config.RateRequests, "rate-requests", config.RateRequests, "concurrent rate-limit requests")
	flags.IntVar(&config.RateBurst, "rate-burst", config.RateBurst, "rate-limit burst")
	path := flags.String("out", defaultOutputPath(now().UTC()), "private JSON output path")
	if err := flags.Parse(arguments); err != nil {
		return errors.New("benchmark_arguments_invalid")
	}
	commit, err := gitCommit()
	if err != nil {
		return err
	}
	report, runErr := localbench.Run(config, localbench.Environment{Commit: commit})
	if err := writeReport(*path, report); err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	return runErr
}

func defaultOutputPath(now time.Time) string {
	return filepath.Join(".private", "benchmark-results", "017-local-benchmark-"+now.Format("20060102T150405Z")+".json")
}

func gitCommit() (string, error) {
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	commit := strings.TrimSpace(string(output))
	if err != nil || commit == "" {
		return "", errors.New("git_commit_unavailable")
	}
	return commit, nil
}

func writeReport(path string, report localbench.Report) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".local-benchmark-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
