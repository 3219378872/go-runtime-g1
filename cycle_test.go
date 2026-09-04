package g1gc

import (
	"context"
	"errors"
	"testing"
)

func TestG1CycleKeepsReachableGraphAndReclaimsGarbage(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	root, err := h.AllocateObject(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.AllocateObject(128, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(root, 0, child); err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(root); err != nil {
		t.Fatal(err)
	}
	garbage := make([]ObjectID, 0, 20)
	for i := 0; i < 20; i++ {
		id, allocErr := h.Allocate(200)
		if allocErr != nil {
			t.Fatal(allocErr)
		}
		garbage = append(garbage, id)
	}

	stats, err := h.Collect(context.Background(), CauseExplicit)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Completed {
		t.Fatal("collection did not complete")
	}
	wantPhases := []Phase{PhaseInitialMark, PhaseConcurrentMark, PhaseRemark, PhaseCleanup, PhaseEvacuation}
	if len(stats.Phases) != len(wantPhases) {
		t.Fatalf("phases = %v, want %v", stats.Phases, wantPhases)
	}
	for i := range wantPhases {
		if stats.Phases[i] != wantPhases[i] {
			t.Fatalf("phases = %v, want %v", stats.Phases, wantPhases)
		}
	}
	if !h.IsAlive(root) || !h.IsAlive(child) {
		t.Fatalf("reachable graph was reclaimed: root=%v child=%v", h.IsAlive(root), h.IsAlive(child))
	}
	resolvedRoot, ok := h.Resolve(root)
	if !ok {
		t.Fatal("root handle did not resolve")
	}
	resolvedChild, err := h.Reference(resolvedRoot, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsAlive(resolvedChild) {
		t.Fatal("rewritten child reference is not live")
	}
	for _, id := range garbage {
		if h.IsAlive(id) {
			t.Fatalf("unreachable object %d survived", id)
		}
	}
	if stats.ReclaimedBytes == 0 {
		t.Fatal("cleanup reclaimed no bytes")
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledCycleRestoresIdleState(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	root, err := h.AllocateObject(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(root, 0, child); err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.Collect(ctx, CauseExplicit)
	if !errors.Is(err, ErrContextCancelled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if h.State() != PhaseIdle || !h.IsAlive(root) || !h.IsAlive(child) {
		t.Fatalf("cancelled cycle corrupted heap: state=%s root=%v child=%v", h.State(), h.IsAlive(root), h.IsAlive(child))
	}
}

func TestAllocationFailureTriggersAutomaticCollection(t *testing.T) {
	h, err := New(Config{
		HeapSize:                 4 * 1024,
		RegionSize:               1024,
		GCWorkers:                2,
		MaxTenuringAge:           2,
		EvacuationReservePercent: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	root, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := h.Allocate(200); err != nil {
			t.Fatalf("allocation %d failed after automatic GC: %v", i, err)
		}
	}
	if !h.IsAlive(root) {
		t.Fatal("automatic collection reclaimed root")
	}
	if h.LastStats().Cycle == 0 {
		t.Fatal("automatic collection did not publish stats")
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}
