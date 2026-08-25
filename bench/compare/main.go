package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type result struct {
	Runtime             string  `json:"runtime"`
	Scenario            string  `json:"scenario"`
	OperationsPerSecond float64 `json:"operations_per_second"`
	TotalAllocBytes     uint64  `json:"total_alloc_bytes"`
	NumGC               uint32  `json:"num_gc"`
	PauseTotalNs        uint64  `json:"pause_total_ns"`
	MaxPauseNs          int64   `json:"max_pause_ns"`
	PauseP99Ns          int64   `json:"pause_p99_ns"`
	GCCPUFraction       float64 `json:"gc_cpu_fraction"`
	HeapSysBytes        uint64  `json:"heap_sys_bytes"`
	RSSMaxMB            float64 `json:"rss_max_mb"`
	RSSAvgMB            float64 `json:"rss_avg_mb"`
	RSSFinalMB          float64 `json:"rss_final_mb"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: compare official.json candidate.json")
		os.Exit(2)
	}
	official := load(os.Args[1])
	candidate := load(os.Args[2])
	if official.Scenario != candidate.Scenario {
		fmt.Fprintln(os.Stderr, "scenario mismatch")
		os.Exit(2)
	}
	fmt.Printf("scenario=%s\n", official.Scenario)
	fmt.Printf("metric                  official             candidate             candidate/official\n")
	row("throughput ops/s", official.OperationsPerSecond, candidate.OperationsPerSecond, false)
	row("total alloc bytes", float64(official.TotalAllocBytes), float64(candidate.TotalAllocBytes), true)
	row("GC cycles", float64(official.NumGC), float64(candidate.NumGC), true)
	row("STW total ns", float64(official.PauseTotalNs), float64(candidate.PauseTotalNs), true)
	row("STW max ns", float64(official.MaxPauseNs), float64(candidate.MaxPauseNs), true)
	row("STW p99 ns", float64(official.PauseP99Ns), float64(candidate.PauseP99Ns), true)
	row("GC CPU fraction", official.GCCPUFraction, candidate.GCCPUFraction, true)
	row("heap sys bytes", float64(official.HeapSysBytes), float64(candidate.HeapSysBytes), true)
	row("rss max MB", official.RSSMaxMB, candidate.RSSMaxMB, true)
	row("rss avg MB", official.RSSAvgMB, candidate.RSSAvgMB, true)
	row("rss final MB", official.RSSFinalMB, candidate.RSSFinalMB, true)
}

func load(path string) result {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var value result
	if err := json.Unmarshal(data, &value); err != nil {
		panic(err)
	}
	return value
}

func row(name string, official, candidate float64, lowerIsBetter bool) {
	ratio := 0.0
	if official != 0 {
		ratio = candidate / official
	}
	if lowerIsBetter {
		fmt.Printf("%-24s %18.3f %18.3f %18.3fx\n", name, official, candidate, ratio)
		return
	}
	fmt.Printf("%-24s %18.3f %18.3f %18.3fx\n", name, official, candidate, ratio)
}
