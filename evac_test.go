package g1gc

import (
	"errors"
	"testing"
)

func TestEvacuationUpdatesOldHandlesAndCrossRegionReferences(t *testing.T) {
	h, err := New(Config{
		HeapSize:                 8 * 1024,
		RegionSize:               1024,
		GCWorkers:                2,
		MaxTenuringAge:           2,
		MixedGCCount:             8,
		EvacuationReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	owner, err := h.AllocateObject(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	target, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(owner, 0, target); err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(owner); err != nil {
		t.Fatal(err)
	}
	beforeOwner, err := h.ObjectInfo(owner)
	if err != nil {
		t.Fatal(err)
	}
	beforeTarget, err := h.ObjectInfo(target)
	if err != nil {
		t.Fatal(err)
	}
	if beforeOwner.Region == beforeTarget.Region {
		t.Fatalf("test graph did not cross regions: owner=%v target=%v", beforeOwner.Region, beforeTarget.Region)
	}

	stats, err := h.GC()
	if err != nil {
		t.Fatal(err)
	}
	if stats.MovedObjects < 2 {
		t.Fatalf("moved %d objects, want at least 2", stats.MovedObjects)
	}
	newOwner, ok := h.Resolve(owner)
	if !ok || newOwner == owner {
		t.Fatalf("owner was not forwarded: old=%d new=%d ok=%v", owner, newOwner, ok)
	}
	newTarget, ok := h.Resolve(target)
	if !ok || newTarget == target {
		t.Fatalf("target was not forwarded: old=%d new=%d ok=%v", target, newTarget, ok)
	}
	ref, err := h.Reference(owner, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ref != newTarget {
		t.Fatalf("reference = %d, want forwarded target %d", ref, newTarget)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHumongousSpanIsSweptAndRootedSpanSurvives(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	dead, err := h.Allocate(600)
	if err != nil {
		t.Fatal(err)
	}
	live, err := h.Allocate(600)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(live); err != nil {
		t.Fatal(err)
	}
	info, err := h.ObjectInfo(live)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != RegionHumongousStart {
		t.Fatalf("kind = %v, want humongous start", info.Kind)
	}
	if _, err := h.GC(); err != nil {
		t.Fatal(err)
	}
	if h.IsAlive(dead) == true {
		t.Fatal("dead humongous object survived")
	}
	if !h.IsAlive(live) {
		t.Fatal("rooted humongous object was reclaimed")
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedObjectReportsEvacuationFailureButRemainsLive(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	pinned, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(pinned); err != nil {
		t.Fatal(err)
	}
	if err := h.Pin(pinned); err != nil {
		t.Fatal(err)
	}
	stats, err := h.GC()
	if !errors.Is(err, ErrEvacuationFailure) {
		t.Fatalf("GC error = %v, want evacuation failure", err)
	}
	if len(stats.FailedRegions) == 0 || !h.IsAlive(pinned) {
		t.Fatalf("pinned object was not retained: stats=%+v alive=%v", stats, h.IsAlive(pinned))
	}
	if h.State() != PhaseIdle {
		t.Fatalf("state = %s, want idle", h.State())
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}
