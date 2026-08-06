package g1gc

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		HeapSize:                 16 * 1024,
		RegionSize:               1024,
		GCWorkers:                2,
		MaxTenuringAge:           3,
		MixedGCCount:             8,
		EvacuationReservePercent: 10,
	}
}

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

func TestRememberedSetRetainsAllCrossRegionEdges(t *testing.T) {
	h, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	source, err := h.AllocateObject(128, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Allocate(400); err != nil {
		t.Fatal(err)
	}
	targetA, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	targetB, err := h.Allocate(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(source, 0, targetA); err != nil {
		t.Fatal(err)
	}
	if err := h.SetReference(source, 1, targetB); err != nil {
		t.Fatal(err)
	}
	sourceRegion, err := h.ObjectInfo(source)
	if err != nil {
		t.Fatal(err)
	}
	targetRegion, err := h.ObjectInfo(targetA)
	if err != nil {
		t.Fatal(err)
	}
	remembered, err := h.RememberedSet(targetRegion.Region)
	if err != nil {
		t.Fatal(err)
	}
	if len(remembered) != 1 || remembered[0] != sourceRegion.Region {
		t.Fatalf("remembered set = %v, want source region %d", remembered, sourceRegion.Region)
	}
	if err := h.ClearReference(source, 0); err != nil {
		t.Fatal(err)
	}
	remembered, err = h.RememberedSet(targetRegion.Region)
	if err != nil {
		t.Fatal(err)
	}
	if len(remembered) != 1 || remembered[0] != sourceRegion.Region {
		t.Fatalf("second edge was erased: remembered set = %v", remembered)
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

func TestSATBPreservesPreMutationValueForOneCycle(t *testing.T) {
	h, err := New(Config{
		HeapSize:                 16 * 1024 * 1024,
		RegionSize:               1024,
		GCWorkers:                1,
		MaxTenuringAge:           3,
		EvacuationReservePercent: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	root, err := h.AllocateObject(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	chain := NullObject
	for i := 0; i < 16000; i++ {
		node, allocErr := h.AllocateObject(64, 1)
		if allocErr != nil {
			t.Fatal(allocErr)
		}
		if chain != NullObject {
			if setErr := h.SetReference(node, 0, chain); setErr != nil {
				t.Fatal(setErr)
			}
		}
		chain = node
	}
	if err := h.SetReference(root, 0, chain); err != nil {
		t.Fatal(err)
	}
	if err := h.AddRoot(root); err != nil {
		t.Fatal(err)
	}
	oldTarget := chain

	type result struct {
		stats Stats
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		stats, collectErr := h.GC()
		resultCh <- result{stats: stats, err: collectErr}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for h.State() != PhaseConcurrentMark {
		if time.Now().After(deadline) {
			t.Fatal("collection never entered concurrent-mark")
		}
		runtime.Gosched()
	}
	if err := h.SetReference(root, 0, NullObject); err != nil {
		t.Fatal(err)
	}
	resultValue := <-resultCh
	if resultValue.err != nil {
		t.Fatal(resultValue.err)
	}
	if !h.IsAlive(oldTarget) {
		t.Fatal("SATB did not preserve the pre-mutation target")
	}
	if ref, err := h.Reference(root, 0); err != nil || ref != NullObject {
		t.Fatalf("root reference after mutation = %d, err=%v", ref, err)
	}
	if _, err := h.GC(); err != nil {
		t.Fatal(err)
	}
	if h.IsAlive(oldTarget) {
		t.Fatal("pre-mutation target survived a second cycle without a root")
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
