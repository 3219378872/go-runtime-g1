package g1gc

import (
	"context"
	"testing"
	"time"
)

func TestPromotionAndIHOPPolicyAcrossCycles(t *testing.T) {
	cfg := testConfig()
	cfg.HeapSize = 8 * 1024
	cfg.MaxTenuringAge = 2
	cfg.InitiatingHeapOccupancy = 1
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	id, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Allocate(128); err != nil {
		t.Fatal(err)
	}
	if !h.ShouldStartCycle() {
		t.Fatal("IHOP threshold was not reached")
	}
	for cycle := 0; cycle < 3; cycle++ {
		if _, started, gcErr := h.MaybeCollect(context.Background()); gcErr != nil || !started {
			t.Fatalf("cycle %d: started=%v err=%v", cycle, started, gcErr)
		}
	}
	info, err := h.ObjectInfo(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != RegionOld {
		t.Fatalf("promoted kind = %v, want old", info.Kind)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPauseBudgetLimitsCollectionSet(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPause = time.Microsecond
	cfg.MaxTenuringAge = 2
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	for i := 0; i < 6; i++ {
		root, allocErr := h.Allocate(128)
		if allocErr != nil {
			t.Fatal(allocErr)
		}
		if addErr := h.AddRoot(root); addErr != nil {
			t.Fatal(addErr)
		}
		if _, allocErr = h.Allocate(400); allocErr != nil {
			t.Fatal(allocErr)
		}
		if _, allocErr = h.Allocate(400); allocErr != nil {
			t.Fatal(allocErr)
		}
	}
	stats, err := h.GC()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.SelectedRegions) == 0 || len(stats.SkippedRegions) == 0 {
		t.Fatalf("collection set ignored pause budget: selected=%v skipped=%v", stats.SelectedRegions, stats.SkippedRegions)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}
