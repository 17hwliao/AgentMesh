package localbench

import (
	"runtime"
	"sync"
	"time"
)

type ResourceReport struct {
	Scope                 string   `json:"scope"`
	AllocatedBytes        uint64   `json:"allocated_bytes"`
	Allocations           uint64   `json:"allocations"`
	SampledPeakHeapBytes  uint64   `json:"sampled_peak_heap_bytes"`
	SampledPeakGoroutines int      `json:"sampled_peak_goroutines"`
	StartGoroutines       int      `json:"start_goroutines"`
	EndGoroutines         int      `json:"end_goroutines"`
	ProcessCPUSeconds     *float64 `json:"process_cpu_seconds,omitempty"`
	CPUStatus             string   `json:"cpu_status"`
}
type resourceMonitor struct {
	start      runtime.MemStats
	report     ResourceReport
	cpu        *float64
	stop, done chan struct{}
	once       sync.Once
}

func startResources() *resourceMonitor {
	m := &resourceMonitor{stop: make(chan struct{}), done: make(chan struct{}), cpu: processCPU()}
	runtime.ReadMemStats(&m.start)
	m.report = ResourceReport{Scope: "client+mock_gateway+warmup+rate_limit; 25ms samples may miss peaks", StartGoroutines: runtime.NumGoroutine(), SampledPeakHeapBytes: m.start.HeapAlloc}
	go func() {
		defer close(m.done)
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			m.sample()
			select {
			case <-tick.C:
			case <-m.stop:
				return
			}
		}
	}()
	return m
}
func (m *resourceMonitor) sample() {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.HeapAlloc > m.report.SampledPeakHeapBytes {
		m.report.SampledPeakHeapBytes = stats.HeapAlloc
	}
	if n := runtime.NumGoroutine(); n > m.report.SampledPeakGoroutines {
		m.report.SampledPeakGoroutines = n
	}
}
func (m *resourceMonitor) finish() ResourceReport {
	m.once.Do(func() {
		close(m.stop)
		<-m.done
		m.sample()
		var end runtime.MemStats
		runtime.ReadMemStats(&end)
		m.report.AllocatedBytes = end.TotalAlloc - m.start.TotalAlloc
		m.report.Allocations = end.Mallocs - m.start.Mallocs
		m.report.EndGoroutines = runtime.NumGoroutine()
		m.report.CPUStatus = "unavailable_on_this_platform"
		if cpu := processCPU(); cpu != nil && m.cpu != nil {
			delta := *cpu - *m.cpu
			m.report.ProcessCPUSeconds = &delta
			m.report.CPUStatus = "measured_process_user_plus_system"
		}
	})
	return m.report
}
