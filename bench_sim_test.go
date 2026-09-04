package g1gc

import (
	"context"
	"testing"
)

func benchConfig() Config {
	return Config{
		HeapSize:                 64 << 20,
		RegionSize:               1 << 20,
		GCWorkers:                4,
		MaxTenuringAge:           3,
		MixedGCCount:             8,
		EvacuationReservePercent: 10,
	}
}

func BenchmarkAllocate4K(b *testing.B) {
	h, _ := New(benchConfig())
	defer h.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Allocate(128); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSetReference(b *testing.B) {
	h, _ := New(benchConfig())
	defer h.Close()
	owner, _ := h.AllocateObject(64, 1)
	target, _ := h.Allocate(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.SetReference(owner, 0, target)
	}
}

func BenchmarkUsedBytesQuery(b *testing.B) {
	h, _ := New(benchConfig())
	defer h.Close()
	for i := 0; i < 5000; i++ {
		_, _ = h.Allocate(128)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.UsedBytes()
	}
}

func BenchmarkCollect10KLive(b *testing.B) {
	h, _ := New(benchConfig())
	defer h.Close()
	var root ObjectID
	for i := 0; i < 10000; i++ {
		id, _ := h.AllocateObject(128, 1)
		if i == 0 {
			root = id
			_ = h.AddRoot(root)
		} else {
			_ = h.SetReference(root, 0, id)
			// chain: keep root pointing at latest, older stay reachable via SATB? simpler: root refs latest only;
			// add each as root-adjacent by linking
			prev := id
			_ = prev
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = h.Collect(context.Background(), CauseExplicit)
	}
}

// BenchmarkCollectOldHeavy models a heap with a large stable Old generation
// and small Eden churn. The RSet-filtered rewrite should touch ~Eden instead
// of the whole heap here.
func BenchmarkCollectOldHeavy(b *testing.B) {
	cfg := benchConfig()
	cfg.HeapSize = 64 << 20
	cfg.RegionSize = 1 << 20
	h, _ := New(cfg)
	defer h.Close()
	// Stable Old: 12000 ref-free objects, 200 rooted.
	for i := 0; i < 12000; i++ {
		id, err := h.Allocate(128)
		if err != nil {
			b.Fatal(err)
		}
		if i%60 == 0 {
			_ = h.AddRoot(id)
		}
	}
	// Promote through two cycles so they settle as Old.
	for i := 0; i < 2; i++ {
		if _, err := h.Collect(b.Context(), CauseExplicit); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Small Eden churn: 300 garbage + 1 survivor chain link.
		for g := 0; g < 300; g++ {
			if _, err := h.Allocate(64); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := h.Collect(b.Context(), CauseExplicit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvacHeavy(b *testing.B) {
	cfg := benchConfig()
	cfg.HeapSize = 16 << 20
	cfg.RegionSize = 1 << 20
	h, _ := New(cfg)
	defer h.Close()
	// Fill multiple regions with cross-region refs to force evac work.
	roots := make([]ObjectID, 0, 64)
	for i := 0; i < 8000; i++ {
		id, err := h.AllocateObject(256, 1)
		if err != nil {
			break
		}
		if i%100 == 0 {
			_ = h.AddRoot(id)
			roots = append(roots, id)
		} else if len(roots) > 0 {
			_ = h.SetReference(roots[len(roots)-1], 0, id)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = h.Collect(context.Background(), CauseExplicit)
	}
}
