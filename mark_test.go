package g1gc

import (
	"runtime"
	"testing"
	"time"
)

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
