package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type node64 struct {
	a, b *node64
	data [48]byte
}

type node256 struct {
	a, b *node256
	data [232]byte
}

// fragNode mixes size classes on purpose: payload cycles through tiny, mid,
// and multi-kilobyte lengths (the largest crosses into the large-object
// path), so the heap interleaves many span sizes inside the same regions.
type fragNode struct {
	next    *fragNode
	payload []byte
}

type result struct {
	Runtime             string    `json:"runtime"`
	Scenario            string    `json:"scenario"`
	DurationSeconds     float64   `json:"duration_seconds"`
	Workers             int       `json:"workers"`
	GOMAXPROCS          int       `json:"gomaxprocs"`
	GOGC                int       `json:"gogc"`
	Operations          uint64    `json:"operations"`
	OperationsPerSecond float64   `json:"operations_per_second"`
	TotalAllocBytes     uint64    `json:"total_alloc_bytes"`
	HeapAllocBytes      uint64    `json:"heap_alloc_bytes"`
	HeapInuseBytes      uint64    `json:"heap_inuse_bytes"`
	HeapSysBytes        uint64    `json:"heap_sys_bytes"`
	HeapObjects         uint64    `json:"heap_objects"`
	NumGC               uint32    `json:"num_gc"`
	GCCPUFraction       float64   `json:"gc_cpu_fraction"`
	PauseTotalNs        uint64    `json:"pause_total_ns"`
	PauseCount          int       `json:"pause_count"`
	MaxPauseNs          int64     `json:"max_pause_ns"`
	PauseP99Ns          int64     `json:"pause_p99_ns"`
	CyclesTotal         uint64    `json:"cycles_total"`
	HeapObjectsMetric   uint64    `json:"heap_objects_metric"`
	RSSMaxMB            float64   `json:"rss_max_mb"`
	RSSAvgMB            float64   `json:"rss_avg_mb"`
	RSSFinalMB          float64   `json:"rss_final_mb"`
	RssSamplesMB        []float64 `json:"rss_samples_mb,omitempty"`
	GCTrace             string    `json:"gc_trace_file,omitempty"`
}

type config struct {
	scenario string
	duration time.Duration
	workers  int
	live     int
	batch    int
	size     int
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.scenario, "scenario", "pointer64", "pointer64, pointer256, alloc, or frag")
	flag.DurationVar(&cfg.duration, "duration", 5*time.Second, "workload duration")
	flag.IntVar(&cfg.workers, "workers", 1, "mutator worker count")
	flag.IntVar(&cfg.live, "live", 1024, "live roots per pointer worker")
	flag.IntVar(&cfg.batch, "batch", 128, "allocations per scheduling batch")
	flag.IntVar(&cfg.size, "size", 256, "allocation size for alloc scenario")
	flag.Parse()
	if cfg.duration <= 0 || cfg.workers <= 0 || cfg.live <= 0 || cfg.batch <= 0 || cfg.size <= 0 {
		fatal("duration, workers, live, batch, and size must be positive")
	}

	runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	ready := make(chan struct{}, cfg.workers)
	start := make(chan struct{})
	stop := make(chan struct{})
	var operations atomic.Uint64
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			switch cfg.scenario {
			case "pointer64":
				roots := makeRoots64(cfg.live)
				ready <- struct{}{}
				<-start
				run64(roots, cfg.batch, id, stop, &operations)
				runtime.KeepAlive(roots)
			case "pointer256":
				roots := makeRoots256(cfg.live)
				ready <- struct{}{}
				<-start
				run256(roots, cfg.batch, id, stop, &operations)
				runtime.KeepAlive(roots)
			case "alloc":
				ready <- struct{}{}
				<-start
				runAlloc(cfg.size, cfg.batch, id, stop, &operations)
			case "frag":
				roots := makeFrag(cfg.live, id)
				ready <- struct{}{}
				<-start
				runFrag(roots, cfg.batch, id, stop, &operations)
				runtime.KeepAlive(roots)
			default:
				fatal("unknown scenario: " + cfg.scenario)
			}
		}(worker)
	}
	for worker := 0; worker < cfg.workers; worker++ {
		<-ready
	}
	// Establish the live set before taking the measurement baseline.
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var beforeCycles uint64
	beforeCycles, _ = readMetrics()
	started := time.Now()
	// Track the resident set over the measured window: Go-level heap
	// counters do not see whether the scavenger actually returned memory,
	// which is the observable payoff of compaction on fragmented heaps.
	rssStop := make(chan struct{})
	var rssWG sync.WaitGroup
	var rssSamples []float64
	rssWG.Add(1)
	go func() {
		defer rssWG.Done()
		rssSamples = sampleRSS(rssStop, 50*time.Millisecond)
	}()
	var profileFile *os.File
	if profilePath := os.Getenv("CPU_PROFILE"); profilePath != "" {
		var err error
		profileFile, err = os.Create(profilePath)
		if err != nil {
			fatal(err.Error())
		}
		if err := pprof.StartCPUProfile(profileFile); err != nil {
			profileFile.Close()
			fatal(err.Error())
		}
	}
	close(start)
	timer := time.NewTimer(cfg.duration)
	<-timer.C
	close(stop)
	wg.Wait()
	close(rssStop)
	rssWG.Wait()
	if profileFile != nil {
		pprof.StopCPUProfile()
		if err := profileFile.Close(); err != nil {
			fatal(err.Error())
		}
	}
	elapsed := time.Since(started)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	afterCycles, afterObjects := readMetrics()
	pauses := pauseWindow(before, after, started)
	if len(pauses) == 0 && after.NumGC > before.NumGC {
		// PauseEnd is a bounded ring. PauseTotalNs remains the authoritative
		// total even when more than 256 cycles completed.
		pauses = []time.Duration{time.Duration(after.PauseTotalNs - before.PauseTotalNs)}
	}
	maxPause, p99 := pauseQuantiles(pauses)
	rssMax, rssAvg, rssFinal := rssSummary(rssSamples)
	output := result{
		Runtime:             runtime.Version(),
		Scenario:            cfg.scenario,
		DurationSeconds:     elapsed.Seconds(),
		Workers:             cfg.workers,
		GOMAXPROCS:          runtime.GOMAXPROCS(0),
		GOGC:                debug.SetGCPercent(-1),
		Operations:          operations.Load(),
		OperationsPerSecond: float64(operations.Load()) / elapsed.Seconds(),
		TotalAllocBytes:     after.TotalAlloc - before.TotalAlloc,
		HeapAllocBytes:      after.HeapAlloc,
		HeapInuseBytes:      after.HeapInuse,
		HeapSysBytes:        after.HeapSys,
		HeapObjects:         after.HeapObjects,
		NumGC:               after.NumGC - before.NumGC,
		GCCPUFraction:       after.GCCPUFraction,
		PauseTotalNs:        after.PauseTotalNs - before.PauseTotalNs,
		PauseCount:          len(pauses),
		MaxPauseNs:          maxPause,
		PauseP99Ns:          p99,
		CyclesTotal:         afterCycles - beforeCycles,
		HeapObjectsMetric:   afterObjects,
		RSSMaxMB:            rssMax,
		RSSAvgMB:            rssAvg,
		RSSFinalMB:          rssFinal,
		RssSamplesMB:        rssSamples,
	}
	// Restore the process setting before any deferred runtime work can observe
	// the temporary read mode used above.
	debug.SetGCPercent(output.GOGC)
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fatal(err.Error())
	}
}

// sampleRSS polls the process resident set until stop is closed. It reads
// /proc/self/statm directly so the series reflects what the kernel actually
// holds, including pages the scavenger has or has not returned.
func sampleRSS(stop <-chan struct{}, interval time.Duration) []float64 {
	var samples []float64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return samples
		case <-ticker.C:
			if mb := currentRSSMB(); mb > 0 {
				samples = append(samples, mb)
			}
		}
	}
}

func currentRSSMB() float64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return float64(pages*uint64(os.Getpagesize())) / 1e6
}

func rssSummary(samples []float64) (maxMB, avgMB, finalMB float64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	sum := 0.0
	maxMB = samples[0]
	for _, v := range samples {
		sum += v
		if v > maxMB {
			maxMB = v
		}
	}
	return maxMB, sum / float64(len(samples)), samples[len(samples)-1]
}

func makeRoots64(n int) []*node64 {
	roots := make([]*node64, n)
	for i := range roots {
		roots[i] = &node64{data: [48]byte{byte(i)}}
	}
	return roots
}

func makeRoots256(n int) []*node256 {
	roots := make([]*node256, n)
	for i := range roots {
		roots[i] = &node256{data: [232]byte{byte(i)}}
	}
	return roots
}

func run64(roots []*node64, batch, worker int, stop <-chan struct{}, operations *atomic.Uint64) {
	seed := uint64(worker + 1)
	for {
		select {
		case <-stop:
			return
		default:
		}
		for i := 0; i < batch; i++ {
			seed = seed*6364136223846793005 + 1442695040888963407
			idx := int(seed % uint64(len(roots)))
			other := int((seed >> 17) % uint64(len(roots)))
			old := roots[idx]
			next := &node64{a: old.a, b: roots[other].b, data: [48]byte{byte(seed)}}
			roots[idx] = next
			operations.Add(1)
		}
	}
}

func run256(roots []*node256, batch, worker int, stop <-chan struct{}, operations *atomic.Uint64) {
	seed := uint64(worker + 7)
	for {
		select {
		case <-stop:
			return
		default:
		}
		for i := 0; i < batch; i++ {
			seed = seed*2862933555777941757 + 3037000493
			idx := int(seed % uint64(len(roots)))
			other := int((seed >> 19) % uint64(len(roots)))
			old := roots[idx]
			next := &node256{a: old.a, b: roots[other].b, data: [232]byte{byte(seed)}}
			roots[idx] = next
			operations.Add(1)
		}
	}
}

func runAlloc(size, batch, worker int, stop <-chan struct{}, operations *atomic.Uint64) {
	seed := byte(worker)
	for {
		select {
		case <-stop:
			return
		default:
		}
		for i := 0; i < batch; i++ {
			buf := make([]byte, size)
			buf[0] = seed
			seed++
			runtime.KeepAlive(buf)
			operations.Add(1)
		}
	}
}

// fragPayloadSizes mixes size classes so consecutive replacements allocate
// from small spans, mid spans, and the large-object path in turn.
var fragPayloadSizes = [4]int{24, 264, 3072, 40 * 1024}

func makeFrag(n, worker int) []*fragNode {
	roots := make([]*fragNode, n)
	seed := uint64(worker + 13)
	for i := range roots {
		seed = seed*6364136223846793005 + 1442695040888963407
		size := fragPayloadSizes[seed%uint64(len(fragPayloadSizes))]
		payload := make([]byte, size)
		payload[0] = byte(seed)
		roots[i] = &fragNode{payload: payload}
	}
	return roots
}

// runFrag replaces random slots with fresh nodes (churn) and, once every
// fragEpoch operations, migrates a contiguous quarter of the live set: the
// old band dies while its replacement is allocated at the allocation
// frontier. The abandoned band leaves sparse survivors behind, which is the
// fragmentation pressure a compacting collector is supposed to relieve.
func runFrag(roots []*fragNode, batch, worker int, stop <-chan struct{}, operations *atomic.Uint64) {
	const epoch = 50000
	seed := uint64(worker*1000003 + 31)
	untilMigration := epoch
	for {
		select {
		case <-stop:
			return
		default:
		}
		for i := 0; i < batch; i++ {
			seed = seed*2862933555777941757 + 3037000493
			idx := int(seed % uint64(len(roots)))
			old := roots[idx]
			size := fragPayloadSizes[(seed>>13)%uint64(len(fragPayloadSizes))]
			payload := make([]byte, size)
			payload[0] = byte(seed)
			roots[idx] = &fragNode{next: old.next, payload: payload}
			if old.payload != nil {
				payload[len(payload)-1] = old.payload[0]
			}
			operations.Add(1)
			untilMigration--
			if untilMigration == 0 {
				untilMigration = epoch
				start := int((seed >> 29) % uint64(len(roots)))
				band := len(roots) / 4
				for k := 0; k < band; k++ {
					j := (start + k) % len(roots)
					psize := fragPayloadSizes[uint64(k+worker)%uint64(len(fragPayloadSizes))]
					ppayload := make([]byte, psize)
					ppayload[0] = byte(j)
					roots[j] = &fragNode{payload: ppayload}
					operations.Add(1)
				}
			}
		}
	}
}

func readMetrics() (uint64, uint64) {
	samples := []metrics.Sample{
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/memory/classes/heap/objects:bytes"},
	}
	metrics.Read(samples)
	return samples[0].Value.Uint64(), samples[1].Value.Uint64()
}

func pauseWindow(before, after runtime.MemStats, start time.Time) []time.Duration {
	count := after.NumGC - before.NumGC
	if count > uint32(len(after.PauseNs)) {
		count = uint32(len(after.PauseNs))
	}
	pauses := make([]time.Duration, 0, count)
	startUnixNano := start.UnixNano()
	for i := uint32(0); i < count; i++ {
		cycle := after.NumGC - 1 - i
		index := cycle % uint32(len(after.PauseNs))
		if int64(after.PauseEnd[index]) < startUnixNano {
			continue
		}
		pauses = append(pauses, time.Duration(after.PauseNs[index]))
	}
	return pauses
}

func pauseQuantiles(pauses []time.Duration) (int64, int64) {
	if len(pauses) == 0 {
		return 0, 0
	}
	values := make([]int64, len(pauses))
	for i, pause := range pauses {
		values[i] = pause.Nanoseconds()
	}
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
	p99Index := (len(values) * 99) / 100
	if p99Index >= len(values) {
		p99Index = len(values) - 1
	}
	return values[len(values)-1], values[p99Index]
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
