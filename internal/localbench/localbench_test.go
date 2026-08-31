package localbench

import (
	"net/http"
	"testing"
)

func TestSummarizeKeepsFailuresOutOfPercentiles(t *testing.T) {
	summary := summarize([]Sample{
		{Status: http.StatusOK, SSEComplete: true, DurationMS: 30},
		{Status: http.StatusOK, SSEComplete: true, DurationMS: 10},
		{Status: http.StatusOK, SSEComplete: true, DurationMS: 20},
		{Status: http.StatusTooManyRequests, ResultCode: "rate_limited", DurationMS: 1},
	})
	if summary.Requests != 4 || summary.Succeeded != 3 || summary.Failed != 1 || summary.P50MS == nil || *summary.P50MS != 20 || summary.P95MS == nil || *summary.P95MS != 30 || summary.P99MS == nil || *summary.P99MS != 30 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSummarizeDoesNotInventPercentilesForFailedSamples(t *testing.T) {
	summary := summarize([]Sample{{Status: http.StatusTooManyRequests, ResultCode: "rate_limited"}})
	if summary.Succeeded != 0 || summary.Failed != 1 || summary.P50MS != nil || summary.P95MS != nil || summary.P99MS != nil {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSummarizeRoundsAggregatesElapsedTimeAndThroughput(t *testing.T) {
	summary := summarizeRounds([]Round{
		{Samples: []Sample{{Status: http.StatusOK, SSEComplete: true, DurationMS: 1}}, Summary: Summary{ElapsedMS: 10}},
		{Samples: []Sample{{Status: http.StatusOK, SSEComplete: true, DurationMS: 2}}, Summary: Summary{ElapsedMS: 30}},
	})
	if summary.ElapsedMS != 40 || summary.ThroughputRPS != 50 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestRunConsumesLoopbackSSEAndValidatesRateLimit(t *testing.T) {
	report, err := Run(Config{Warmup: 1, Rounds: 1, Requests: 4, Concurrency: 2, RateRequests: 20, RateBurst: 3}, Environment{Commit: "test-commit"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scope != Scope || report.GitCommit != "test-commit" || report.Summary.Succeeded != 4 || report.Summary.Failed != 0 || report.Summary.ElapsedMS <= 0 || report.Summary.ThroughputRPS <= 0 || report.RateLimit.Allowed > 3 || report.RateLimit.RateLimited != 20-report.RateLimit.Allowed || report.RateLimit.OtherStatuses != 0 || report.RateLimit.MaxConnections != rateMaxConnections || report.RateLimit.StatusCounts["200:"] != report.RateLimit.Allowed || report.RateLimit.StatusCounts["429:rate_limited"] != report.RateLimit.RateLimited || report.RateLimit.ProviderAttempts != report.RateLimit.Allowed {
		t.Fatalf("report=%+v", report)
	}
}
