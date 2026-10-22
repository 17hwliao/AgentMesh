// system-benchmark drives an independently running gateway; no model keys are accepted.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentmesh/internal/sse"
)

type sample struct {
	Status       int     `json:"status"`
	Completed    bool    `json:"completed"`
	Cancelled    bool    `json:"cancelled"`
	Error        string  `json:"error,omitempty"`
	FirstMS      float64 `json:"first_content_ms"`
	TotalMS      float64 `json:"total_ms"`
	Bytes        int     `json:"content_bytes"`
	RetryMS      int     `json:"retry_after_ms,omitempty"`
	ArrivalLagMS float64 `json:"arrival_delay_ms"`
}
type report struct {
	Scope              string             `json:"scope"`
	Mode               string             `json:"mode"`
	Seconds            float64            `json:"seconds"`
	OfferedSeconds     float64            `json:"offered_seconds"`
	SampleLimitReached bool               `json:"sample_limit_reached"`
	Scheduled          int                `json:"scheduled"`
	Dropped            int                `json:"dropped"`
	Completed          int                `json:"completed"`
	Statuses           map[int]int        `json:"statuses"`
	Unexpected         int                `json:"unexpected"`
	Throughput         float64            `json:"throughput"`
	First              map[string]float64 `json:"first_content_ms"`
	Latency            map[string]float64 `json:"complete_latency_ms"`
	ArrivalDelay       map[string]float64 `json:"arrival_delay_ms"`
	Samples            []sample           `json:"samples"`
}

func percentile(values []float64) map[string]float64 {
	out := map[string]float64{}
	if len(values) == 0 {
		return out
	}
	sort.Float64s(values)
	for _, p := range []int{50, 95, 99} {
		i := (len(values)*p+99)/100 - 1
		if i < 0 {
			i = 0
		}
		out[fmt.Sprintf("p%d", p)] = values[i]
	}
	return out
}
func request(ctx context.Context, client *http.Client, endpoint, key string, maxTokens int, readDelay, cancelAfter time.Duration) sample {
	start := time.Now()
	result := sample{}
	reqCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	if cancelAfter > 0 {
		timer := time.AfterFunc(cancelAfter, stop)
		defer timer.Stop()
	}
	body, _ := json.Marshal(map[string]any{"model": "domestic-chat", "messages": []map[string]string{{"role": "user", "content": "local performance fixture request 42"}}, "max_tokens": maxTokens, "temperature": 0, "stream": true, "stream_options": map[string]bool{"include_usage": true}})
	req, _ := http.NewRequestWithContext(reqCtx, "POST", endpoint+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	response, err := client.Do(req)
	if err != nil {
		result.Error = "transport"
		result.Cancelled = reqCtx.Err() != nil && cancelAfter > 0
		result.TotalMS = float64(time.Since(start).Microseconds()) / 1000
		return result
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	if response.StatusCode != 200 {
		if response.StatusCode == 429 {
			result.RetryMS = 1000
			if seconds, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && seconds > 0 && seconds <= 60 {
				result.RetryMS = seconds * 1000
			}
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		result.TotalMS = float64(time.Since(start).Microseconds()) / 1000
		return result
	}
	reader := sse.NewReader(response.Body)
	for {
		event, e := reader.Next()
		if e != nil {
			result.Error = "truncated_stream"
			result.Cancelled = reqCtx.Err() != nil && cancelAfter > 0
			break
		}
		if strings.TrimSpace(event.Data) == "[DONE]" {
			result.Completed = true
			break
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(event.Data), &chunk) != nil || len(chunk.Error) > 0 {
			result.Error = "invalid_or_error_event"
			var upstream struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(chunk.Error, &upstream) == nil && len(upstream.Code) < 64 {
				valid := upstream.Code != ""
				for _, ch := range upstream.Code {
					if !(ch >= 'a' && ch <= 'z' || ch == '_') {
						valid = false
					}
				}
				if valid {
					result.Error = upstream.Code
				}
			}
			break
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if result.Bytes == 0 {
					result.FirstMS = float64(time.Since(start).Microseconds()) / 1000
				}
				result.Bytes += len(choice.Delta.Content)
			}
		}
		if readDelay > 0 {
			select {
			case <-reqCtx.Done():
			case <-time.After(readDelay):
			}
		}
	}
	result.TotalMS = float64(time.Since(start).Microseconds()) / 1000
	return result
}
func execute(ctx context.Context, client *http.Client, endpoint, key string, duration time.Duration, concurrency, rate, maxTokens int, readDelay, cancelAfter time.Duration) report {
	start := time.Now()
	end := start.Add(duration)
	r := report{Scope: "external_http_gateway; fixture capacity only", Mode: "closed", OfferedSeconds: duration.Seconds(), Statuses: map[int]int{}, Samples: []sample{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(planned time.Time) sample {
		defer wg.Done()
		lag := time.Since(planned)
		s := request(ctx, client, endpoint, key, maxTokens, readDelay, cancelAfter)
		s.ArrivalLagMS = float64(lag.Microseconds()) / 1000
		mu.Lock()
		r.Samples = append(r.Samples, s)
		mu.Unlock()
		return s
	}
	if rate == 0 {
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(end) && ctx.Err() == nil {
					mu.Lock()
					if r.Scheduled >= 200000 {
						r.SampleLimitReached = true
						mu.Unlock()
						return
					}
					r.Scheduled++
					mu.Unlock()
					wg.Add(1)
					if response := run(time.Now()); response.Status == 429 {
						delay := time.Duration(response.RetryMS) * time.Millisecond
						if remaining := time.Until(end); delay > remaining {
							delay = remaining
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(delay):
						}
					}
				}
			}()
		}
	} else {
		r.Mode = "open"
		slots := make(chan struct{}, concurrency)
		interval := time.Second / time.Duration(rate)
	arrivals:
		for next := start; next.Before(end) && ctx.Err() == nil; next = next.Add(interval) {
			delay := time.Until(next)
			if delay > 0 {
				select {
				case <-ctx.Done():
					break arrivals
				case <-time.After(delay):
				}
			}
			r.Scheduled++
			select {
			case slots <- struct{}{}:
				wg.Add(1)
				go func(planned time.Time) { defer func() { <-slots }(); run(planned) }(next)
			default:
				r.Dropped++
			}
		}
	}
	wg.Wait()
	r.Seconds = time.Since(start).Seconds()
	var first, total, arrivals []float64
	for _, s := range r.Samples {
		arrivals = append(arrivals, s.ArrivalLagMS)
		r.Statuses[s.Status]++
		if s.Completed {
			r.Completed++
			total = append(total, s.TotalMS)
			if s.Bytes > 0 {
				first = append(first, s.FirstMS)
			}
		} else if !s.Cancelled && s.Status != 429 {
			r.Unexpected++
		}
	}
	r.First = percentile(first)
	r.Latency = percentile(total)
	r.ArrivalDelay = percentile(arrivals)
	r.Throughput = float64(r.Completed) / r.Seconds
	return r
}
func main() {
	endpoint := flag.String("url", "http://127.0.0.1:29880", "explicit gateway/proxy base URL")
	keyFile := flag.String("key-file", "", "private tenant API key file")
	duration := flag.Duration("duration", time.Minute, "offered load duration (1s..2h)")
	concurrency := flag.Int("concurrency", 8, "max outstanding requests (1..256)")
	rate := flag.Int("rate", 0, "open arrival rate per second; zero means closed workers (0..1000)")
	readDelay := flag.Duration("read-delay", 0, "per-event slow consumption delay")
	cancelAfter := flag.Duration("cancel-after", 0, "cancel requests after this duration")
	maxTokens := flag.Int("max-tokens", 100, "output budget (1..4096)")
	out := flag.String("out", ".private/system-benchmark.json", "private result")
	caFile := flag.String("ca-file", "", "optional trusted proxy CA; TLS verification stays enabled")
	flag.Parse()
	u, e := url.Parse(*endpoint)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || *keyFile == "" || *duration < time.Second || *duration > 2*time.Hour || *concurrency < 1 || *concurrency > 256 || *rate < 0 || *rate > 1000 || duration.Seconds()*float64(*rate) > 200000 || *maxTokens < 1 || *maxTokens > 4096 || *readDelay < 0 || *readDelay > time.Second || *cancelAfter < 0 {
		fmt.Fprintln(os.Stderr, "invalid bounded benchmark configuration")
		os.Exit(2)
	}
	raw, e := os.ReadFile(*keyFile)
	if e != nil || len(raw) > 16384 || strings.TrimSpace(string(raw)) == "" {
		fmt.Fprintln(os.Stderr, "key file unavailable")
		os.Exit(2)
	}
	transport := &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, MaxConnsPerHost: *concurrency, IdleConnTimeout: 10 * time.Second}
	if *caFile != "" {
		ca, e := os.ReadFile(*caFile)
		roots := x509.NewCertPool()
		if e != nil || !roots.AppendCertsFromPEM(ca) {
			fmt.Fprintln(os.Stderr, "invalid CA")
			os.Exit(2)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	defer transport.CloseIdleConnections()
	r := execute(context.Background(), &http.Client{Transport: transport}, strings.TrimRight(*endpoint, "/"), strings.TrimSpace(string(raw)), *duration, *concurrency, *rate, *maxTokens, *readDelay, *cancelAfter)
	data, _ := json.MarshalIndent(r, "", "  ")
	if os.WriteFile(*out, append(data, '\n'), 0600) != nil {
		fmt.Fprintln(os.Stderr, "cannot write report")
		os.Exit(2)
	}
	fmt.Printf("mode=%s scheduled=%d complete=%d rejected429=%d dropped=%d unexpected=%d\n", r.Mode, r.Scheduled, r.Completed, r.Statuses[429], r.Dropped, r.Unexpected)
	if r.SampleLimitReached || r.Unexpected > 0 || (r.Completed == 0 && *cancelAfter == 0) {
		os.Exit(1)
	}
}
