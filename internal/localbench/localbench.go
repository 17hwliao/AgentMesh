// Package localbench runs reproducible, local-only AgentMesh benchmark cases.
package localbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/gateway"
	"agentmesh/internal/provider"
	"agentmesh/internal/ratelimit"
	"agentmesh/internal/tenant"
)

const (
	Scope               = "local_loopback_mock_only"
	defaultWarmup       = 20
	defaultRounds       = 5
	defaultRequests     = 200
	defaultConcurrency  = 20
	defaultRateRequests = 1_000
	defaultRateBurst    = 50
	rateMaxConnections  = 100
)

type Config struct {
	Warmup       int `json:"warmup"`
	Rounds       int `json:"rounds"`
	Requests     int `json:"requests_per_round"`
	Concurrency  int `json:"concurrency"`
	RateRequests int `json:"rate_limit_requests"`
	RateBurst    int `json:"rate_limit_burst"`
}

func DefaultConfig() Config {
	return Config{Warmup: defaultWarmup, Rounds: defaultRounds, Requests: defaultRequests, Concurrency: defaultConcurrency, RateRequests: defaultRateRequests, RateBurst: defaultRateBurst}
}

type Environment struct {
	Commit string `json:"git_commit"`
}

type Report struct {
	Scope          string          `json:"scope"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     time.Time       `json:"finished_at"`
	GitCommit      string          `json:"git_commit"`
	GoVersion      string          `json:"go_version"`
	GOOS           string          `json:"goos"`
	GOARCH         string          `json:"goarch"`
	NumCPU         int             `json:"num_cpu"`
	GOMAXPROCS     int             `json:"gomaxprocs"`
	Config         Config          `json:"config"`
	Warmup         Round           `json:"warmup"`
	Rounds         []Round         `json:"rounds"`
	Summary        Summary         `json:"summary"`
	RateLimit      RateLimitReport `json:"rate_limit"`
	ValidationCode string          `json:"validation_code,omitempty"`
}

type Round struct {
	Samples []Sample `json:"samples"`
	Summary Summary  `json:"summary"`
}

type Sample struct {
	Index       int     `json:"index"`
	DurationMS  float64 `json:"duration_ms"`
	Status      int     `json:"status"`
	ResultCode  string  `json:"result_code,omitempty"`
	SSEComplete bool    `json:"sse_complete"`
}

type Summary struct {
	Requests      int      `json:"requests"`
	Succeeded     int      `json:"succeeded"`
	Failed        int      `json:"failed"`
	ElapsedMS     float64  `json:"elapsed_ms"`
	ThroughputRPS float64  `json:"throughput_rps"`
	P50MS         *float64 `json:"p50_ms,omitempty"`
	P95MS         *float64 `json:"p95_ms,omitempty"`
	P99MS         *float64 `json:"p99_ms,omitempty"`
}

type RateLimitReport struct {
	Requests         int            `json:"requests"`
	Burst            int            `json:"burst"`
	Allowed          int            `json:"allowed"`
	RateLimited      int            `json:"rate_limited"`
	OtherStatuses    int            `json:"other_statuses"`
	StatusCounts     map[string]int `json:"status_counts"`
	MaxConnections   int            `json:"max_loopback_connections"`
	ProviderAttempts int            `json:"provider_attempts"`
}

func Run(config Config, environment Environment) (Report, error) {
	if err := valid(config); err != nil {
		return Report{}, err
	}
	report := Report{
		Scope:      Scope,
		StartedAt:  time.Now().UTC(),
		GitCommit:  environment.Commit,
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		Config:     config,
	}
	server, key, _ := newServer(nil)
	defer server.Close()
	client := server.Client()
	report.Warmup = runRound(client, server.URL, key, config.Warmup, config.Concurrency)
	if report.Warmup.Summary.Failed != 0 {
		report.FinishedAt = time.Now().UTC()
		report.ValidationCode = "warmup_failed"
		return report, errors.New(report.ValidationCode)
	}
	report.Rounds = make([]Round, 0, config.Rounds)
	for range config.Rounds {
		round := runRound(client, server.URL, key, config.Requests, config.Concurrency)
		report.Rounds = append(report.Rounds, round)
	}
	report.Summary = summarizeRounds(report.Rounds)
	report.RateLimit = runRateLimit(config)
	report.FinishedAt = time.Now().UTC()
	if report.Summary.Failed != 0 {
		report.ValidationCode = "chat_samples_failed"
		return report, errors.New(report.ValidationCode)
	}
	if report.RateLimit.Allowed > config.RateBurst || report.RateLimit.RateLimited != config.RateRequests-report.RateLimit.Allowed || report.RateLimit.OtherStatuses != 0 || report.RateLimit.ProviderAttempts != report.RateLimit.Allowed {
		report.ValidationCode = "rate_limit_assertion_failed"
		return report, errors.New(report.ValidationCode)
	}
	return report, nil
}

func valid(config Config) error {
	if config.Warmup < 0 || config.Rounds <= 0 || config.Requests <= 0 || config.Concurrency <= 0 || config.RateRequests <= 0 || config.RateBurst <= 0 {
		return errors.New("benchmark_configuration_invalid")
	}
	return nil
}

func newServer(gate ratelimit.Gate) (*httptest.Server, string, *provider.Mock) {
	key := randomKey()
	store := tenant.NewMemory([]tenant.Tenant{{ID: "benchmark-tenant", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"mock"}}}}, []tenant.APIKeyRecord{{Prefix: key[:8], Hash: sha256.Sum256([]byte(key)), TenantID: "benchmark-tenant", Enabled: true}})
	mock := provider.NewMock(provider.MockConfig{Name: "benchmark-mock", Chunks: []string{"benchmark response"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(context.Background(), store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		panic(err)
	}
	server := gateway.NewWithTenantRouting(resolver)
	server.SetRateGate(gate)
	handler := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
	return httptest.NewServer(handler), key, mock
}

func randomKey() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes)
}

func runRound(client *http.Client, baseURL, key string, count, concurrency int) Round {
	started := time.Now()
	samples := make([]Sample, count)
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(count, concurrency) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				samples[index] = chat(client, baseURL, key, index)
			}
		}()
	}
	for index := range count {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	summary := summarize(samples)
	summary.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	if summary.ElapsedMS > 0 {
		summary.ThroughputRPS = float64(summary.Succeeded) / (summary.ElapsedMS / 1000)
	}
	return Round{Samples: samples, Summary: summary}
}

func chat(client *http.Client, baseURL, key string, index int) Sample {
	started := time.Now()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"mock-model","messages":[{"role":"user","content":"benchmark"}],"stream":true}`))
	if err != nil {
		return Sample{Index: index, DurationMS: elapsedMS(started), ResultCode: "request_construction_failed"}
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Sample{Index: index, DurationMS: elapsedMS(started), ResultCode: "http_client_failed"}
	}
	payload, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	sample := Sample{Index: index, DurationMS: elapsedMS(started), Status: response.StatusCode}
	if readErr != nil {
		sample.ResultCode = "response_read_failed"
		return sample
	}
	if response.StatusCode == http.StatusOK {
		sample.SSEComplete = bytes.Contains(payload, []byte("data: [DONE]"))
		if !sample.SSEComplete {
			sample.ResultCode = "sse_incomplete"
		}
		return sample
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Error.Code != "" {
		sample.ResultCode = body.Error.Code
	} else {
		sample.ResultCode = "unexpected_http_status"
	}
	return sample
}

func runRateLimit(config Config) RateLimitReport {
	gate, err := ratelimit.New(ratelimit.Config{PerMinute: 1, Burst: config.RateBurst}, nil)
	if err != nil {
		panic(err)
	}
	server, key, mock := newServer(gate)
	defer server.Close()
	transport := &http.Transport{MaxIdleConns: rateMaxConnections, MaxIdleConnsPerHost: rateMaxConnections, MaxConnsPerHost: rateMaxConnections}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	round := runConcurrentRound(client, server.URL, key, config.RateRequests)
	report := RateLimitReport{Requests: config.RateRequests, Burst: config.RateBurst, StatusCounts: map[string]int{}, MaxConnections: rateMaxConnections, ProviderAttempts: mock.Calls()}
	for _, sample := range round.Samples {
		report.StatusCounts[fmt.Sprintf("%d:%s", sample.Status, sample.ResultCode)]++
		switch sample.Status {
		case http.StatusOK:
			report.Allowed++
		case http.StatusTooManyRequests:
			if sample.ResultCode == "rate_limited" {
				report.RateLimited++
			} else {
				report.OtherStatuses++
			}
		default:
			report.OtherStatuses++
		}
	}
	return report
}

func runConcurrentRound(client *http.Client, baseURL, key string, count int) Round {
	started := time.Now()
	samples := make([]Sample, count)
	ready := sync.WaitGroup{}
	ready.Add(count)
	start := make(chan struct{})
	finished := sync.WaitGroup{}
	finished.Add(count)
	for index := range count {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			samples[index] = chat(client, baseURL, key, index)
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	summary := summarize(samples)
	summary.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	if summary.ElapsedMS > 0 {
		summary.ThroughputRPS = float64(summary.Succeeded) / (summary.ElapsedMS / 1000)
	}
	return Round{Samples: samples, Summary: summary}
}

func summarize(samples []Sample) Summary {
	summary := Summary{Requests: len(samples)}
	latencies := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.Status == http.StatusOK && sample.SSEComplete && sample.ResultCode == "" {
			summary.Succeeded++
			latencies = append(latencies, sample.DurationMS)
		} else {
			summary.Failed++
		}
	}
	if len(latencies) == 0 {
		return summary
	}
	sort.Float64s(latencies)
	summary.P50MS = percentile(latencies, 0.50)
	summary.P95MS = percentile(latencies, 0.95)
	summary.P99MS = percentile(latencies, 0.99)
	return summary
}

func percentile(sorted []float64, quantile float64) *float64 {
	if len(sorted) == 0 || quantile <= 0 || quantile > 1 {
		return nil
	}
	index := int(math.Ceil(float64(len(sorted))*quantile)) - 1
	value := sorted[index]
	return &value
}

func flatten(rounds []Round) []Sample {
	var samples []Sample
	for _, round := range rounds {
		samples = append(samples, round.Samples...)
	}
	return samples
}

func summarizeRounds(rounds []Round) Summary {
	summary := summarize(flatten(rounds))
	for _, round := range rounds {
		summary.ElapsedMS += round.Summary.ElapsedMS
	}
	if summary.ElapsedMS > 0 {
		summary.ThroughputRPS = float64(summary.Succeeded) / (summary.ElapsedMS / 1000)
	}
	return summary
}

func elapsedMS(started time.Time) float64 {
	return float64(time.Since(started)) / float64(time.Millisecond)
}
