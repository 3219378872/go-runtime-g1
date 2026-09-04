package g1gc

import (
	"context"
	"fmt"
	"sync"
)

func (h *Heap) beginMarkingLocked() {
	h.markEpoch++
	if h.markEpoch == 0 {
		// Epoch wrapped (once per 4B cycles): clear all to avoid collision.
		for _, obj := range h.objects {
			obj.markEpoch = 0
		}
		h.markEpoch = 1
	}
	h.marking = true
	h.markCancelled = false
	// Reuse queue/satb backing arrays to avoid per-cycle allocations.
	h.satb = h.satb[:0]
	h.markQueue = h.markQueue[:0]
	h.markActive = 0
	canonicalRoots := make(map[ObjectID]struct{}, len(h.roots))
	for root := range h.roots {
		root = h.resolveLocked(root)
		if _, ok := h.objects[root]; !ok {
			continue
		}
		canonicalRoots[root] = struct{}{}
		h.markObjectLocked(root)
	}
	h.roots = canonicalRoots
}

// isMarkedLocked reports whether obj is marked in the current epoch.
func (h *Heap) isMarkedLocked(obj *object) bool {
	return obj.markEpoch == h.markEpoch
}

// markObjectLocked is the shared mark primitive used by root scanning, the
// concurrent workers, the SATB drain, and insertion/allocation barriers.
// It signals (not broadcasts) only on empty→non-empty transitions to avoid
// thundering-herd wakeups on every marked object.
func (h *Heap) markObjectLocked(id ObjectID) {
	if id == NullObject {
		return
	}
	id = h.resolveLocked(id)
	obj, ok := h.objects[id]
	if !ok || obj.markEpoch == h.markEpoch {
		return
	}
	obj.markEpoch = h.markEpoch
	wasEmpty := len(h.markQueue) == 0
	h.markQueue = append(h.markQueue, id)
	if wasEmpty {
		h.markCond.Signal()
	}
}

func (h *Heap) runConcurrentMark(ctx context.Context) error {
	workerCount := h.config.GCWorkers
	if workerCount < 1 {
		workerCount = 1
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			h.markWorker(ctx)
		}()
	}

	watchDone := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.markCancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
		case <-watchDone:
		}
	}()
	workers.Wait()
	close(watchDone)
	watcher.Wait()

	h.mu.Lock()
	cancelled := h.markCancelled
	h.mu.Unlock()
	if cancelled || ctx.Err() != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}
		return ErrContextCancelled
	}
	return nil
}

func (h *Heap) markWorker(ctx context.Context) {
	// Per-worker batch buffer reused across iterations to avoid clones.
	batch := make([]ObjectID, 0, 32)
	for {
		if ctx.Err() != nil {
			h.mu.Lock()
			h.markCancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
			return
		}

		h.mu.Lock()
		for len(h.markQueue) == 0 && h.markActive > 0 && !h.markCancelled {
			h.markCond.Wait()
			if ctx.Err() != nil {
				h.markCancelled = true
				h.markCond.Broadcast()
			}
		}
		if h.markCancelled || ctx.Err() != nil {
			h.markCancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
			return
		}
		if len(h.markQueue) == 0 {
			// No queued work and no active scanner means the mark closure is
			// complete at this point. A later mutator update is handled by SATB
			// during remark.
			h.mu.Unlock()
			return
		}
		// Pop a batch under a single critical section and snapshot refs
		// inline (one lock instead of pop + snapshotRefs's extra lock).
		n := len(h.markQueue)
		if n > cap(batch) {
			n = cap(batch)
		}
		batch = batch[:0]
		for i := 0; i < n; i++ {
			last := len(h.markQueue) - 1
			id := h.markQueue[last]
			h.markQueue = h.markQueue[:last]
			batch = append(batch, id)
		}
		h.markActive++
		// Snapshot refs while still holding the lock. Batch is bounded
		// (<=32 objects) so the critical section stays short.
		var refs []ObjectID
		for _, id := range batch {
			if obj, ok := h.objects[h.resolveLocked(id)]; ok {
				refs = append(refs, obj.refs...)
			}
		}
		h.mu.Unlock()

		h.mu.Lock()
		if !h.markCancelled {
			wasEmpty := len(h.markQueue) == 0
			for _, ref := range refs {
				if ref == NullObject {
					continue
				}
				rid := h.resolveLocked(ref)
				if obj, ok := h.objects[rid]; ok && obj.markEpoch != h.markEpoch {
					obj.markEpoch = h.markEpoch
					h.markQueue = append(h.markQueue, rid)
				}
			}
			if wasEmpty && len(h.markQueue) > 0 {
				h.markCond.Signal()
			}
		}
		h.markActive--
		if len(h.markQueue) == 0 && h.markActive == 0 {
			h.markCond.Broadcast()
		}
		h.mu.Unlock()
	}
}

func (h *Heap) drainMarkQueueLocked() {
	for len(h.satb) > 0 || len(h.markQueue) > 0 {
		for len(h.satb) > 0 {
			last := len(h.satb) - 1
			id := h.satb[last]
			h.satb = h.satb[:last]
			h.markObjectLocked(id)
		}
		if len(h.markQueue) == 0 {
			continue
		}
		last := len(h.markQueue) - 1
		id := h.markQueue[last]
		h.markQueue = h.markQueue[:last]
		obj, ok := h.objects[h.resolveLocked(id)]
		if !ok {
			continue
		}
		for _, ref := range obj.refs {
			h.markObjectLocked(ref)
		}
	}
}

func (h *Heap) finishMarkingLocked() {
	h.drainMarkQueueLocked()
	h.marking = false
	h.satb = h.satb[:0]
	h.markQueue = h.markQueue[:0]
	h.markActive = 0
}

func (h *Heap) collectMarkStatsLocked(stats *Stats) {
	epoch := h.markEpoch
	for _, obj := range h.objects {
		if obj.markEpoch == epoch {
			stats.MarkedObjects++
			stats.MarkedBytes += obj.size
		}
	}
}
