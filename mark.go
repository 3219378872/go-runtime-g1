package g1gc

import (
	"context"
	"fmt"
	"sync"
)

func (h *Heap) beginMarkingLocked() {
	h.mark.begin(h.objects)
	h.canonicalizeRootsLocked()
	for root := range h.roots {
		h.markObjectLocked(root)
	}
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
	if !ok || !h.mark.mark(obj) {
		return
	}
	if h.mark.push(id) {
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
			h.mark.cancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
		case <-watchDone:
		}
	}()
	workers.Wait()
	close(watchDone)
	watcher.Wait()

	h.mu.Lock()
	cancelled := h.mark.cancelled
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
			h.mark.cancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
			return
		}

		h.mu.Lock()
		for len(h.mark.queue) == 0 && h.mark.active > 0 && !h.mark.cancelled {
			h.markCond.Wait()
			if ctx.Err() != nil {
				h.mark.cancelled = true
				h.markCond.Broadcast()
			}
		}
		if h.mark.cancelled || ctx.Err() != nil {
			h.mark.cancelled = true
			h.markCond.Broadcast()
			h.mu.Unlock()
			return
		}
		if len(h.mark.queue) == 0 {
			// No queued work and no active scanner means the mark closure is
			// complete at this point. A later mutator update is handled by SATB
			// during remark.
			h.mu.Unlock()
			return
		}
		// Pop a batch under a single critical section and snapshot refs
		// inline (one lock instead of pop + snapshotRefs's extra lock).
		batch = h.mark.popBatch(batch)
		h.mark.active++
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
		if !h.mark.cancelled {
			wasEmpty := len(h.mark.queue) == 0
			for _, ref := range refs {
				if ref == NullObject {
					continue
				}
				rid := h.resolveLocked(ref)
				if obj, ok := h.objects[rid]; ok && h.mark.mark(obj) {
					h.mark.queue = append(h.mark.queue, rid)
				}
			}
			if wasEmpty && len(h.mark.queue) > 0 {
				h.markCond.Signal()
			}
		}
		h.mark.active--
		if len(h.mark.queue) == 0 && h.mark.active == 0 {
			h.markCond.Broadcast()
		}
		h.mu.Unlock()
	}
}

func (h *Heap) drainMarkQueueLocked() {
	for len(h.mark.satb) > 0 || len(h.mark.queue) > 0 {
		for len(h.mark.satb) > 0 {
			last := len(h.mark.satb) - 1
			id := h.mark.satb[last]
			h.mark.satb = h.mark.satb[:last]
			h.markObjectLocked(id)
		}
		if len(h.mark.queue) == 0 {
			continue
		}
		last := len(h.mark.queue) - 1
		id := h.mark.queue[last]
		h.mark.queue = h.mark.queue[:last]
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
	h.mark.finish()
}

func (h *Heap) collectMarkStatsLocked(stats *Stats) {
	epoch := h.mark.epoch
	for _, obj := range h.objects {
		if obj.markEpoch == epoch {
			stats.MarkedObjects++
			stats.MarkedBytes += obj.size
		}
	}
}
