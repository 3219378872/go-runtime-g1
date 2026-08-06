package main

import (
	"context"
	"fmt"

	g1 "go-runtime-g1"
)

func main() {
	cfg := g1.DefaultConfig(8 << 20)
	cfg.GCWorkers = 4
	cfg.MaxTenuringAge = 2
	h, err := g1.New(cfg)
	if err != nil {
		panic(err)
	}
	defer h.Close()

	root, err := h.AllocateObject(256, 2)
	if err != nil {
		panic(err)
	}
	left, err := h.Allocate(256)
	if err != nil {
		panic(err)
	}
	right, err := h.Allocate(256)
	if err != nil {
		panic(err)
	}
	if err := h.SetReference(root, 0, left); err != nil {
		panic(err)
	}
	if err := h.SetReference(root, 1, right); err != nil {
		panic(err)
	}
	if err := h.AddRoot(root); err != nil {
		panic(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := h.Allocate(512); err != nil {
			panic(err)
		}
	}

	stats, err := h.Collect(context.Background(), g1.CauseExplicit)
	if err != nil {
		panic(err)
	}
	resolved, alive := h.Resolve(root)
	fmt.Printf("cycle=%d phases=%v moved=%d reclaimed=%d used=%d root=%d alive=%v\n", stats.Cycle, stats.Phases, stats.MovedObjects, stats.ReclaimedBytes, h.UsedBytes(), resolved, alive)
	for _, region := range h.RegionSnapshot() {
		if region.Kind != g1.RegionFree {
			fmt.Printf("region=%d kind=%s used=%d objects=%d remembered=%v\n", region.ID, region.Kind, region.Used, region.ObjectCount, region.RememberedFrom)
		}
	}
}
