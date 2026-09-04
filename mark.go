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
			h.mark.cancel()
			h.markCond.Broadcast()
			h.mu.Unlock()
		case <-watchDone:
		}
	}()
	workers.Wait()
	close(watchDone)
	watcher.Wait()

	h.mu.Lock()
	cancelled := h.mark.isCancelled()
	h.mu.Unlock()
	if cancelled || ctx.Err() != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %s", ErrContextCancelled, err)
		}
		return ErrContextCancelled
	}
	return nil
}

func (h *Heap) markWorker(ctx context.Context) {
	// Per-worker batch buffer reused across iterations to avoid clones.
	batch := make([]ObjectID, 0, 32)
	for {
		if h.cancelMarkIfDone(ctx) {
			return
		}
		refs, ok := h.takeMarkBatch(ctx, batch)
		if !ok {
			return
		}
		h.publishMarkRefs(refs)
	}
}

// cancelMarkIfDone flags global cancellation when ctx is done and reports
// whether the caller must stop.
func (h *Heap) cancelMarkIfDone(ctx context.Context) bool {
	if ctx.Err() == nil {
		return false
	}
	h.mu.Lock()
	h.mark.cancel()
	h.markCond.Broadcast()
	h.mu.Unlock()
	return true
}

// takeMarkBatch waits for queued work, pops one batch with its references
// snapshotted, and records the in-flight scanner. It reports false when the
// mark closure is complete or cancelled, in which case the caller returns.
// A later mutator update after a complete closure is handled by SATB during
// remark.
func (h *Heap) takeMarkBatch(ctx context.Context, batch []ObjectID) ([]ObjectID, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for !h.mark.hasQueuedWork() && !h.mark.isQuiescent() && !h.mark.isCancelled() {
		h.markCond.Wait()
		if ctx.Err() != nil {
			h.mark.cancel()
			h.markCond.Broadcast()
		}
	}
	if h.mark.isCancelled() || ctx.Err() != nil {
		h.mark.cancel()
		h.markCond.Broadcast()
		return nil, false
	}
	if !h.mark.hasQueuedWork() {
		return nil, false
	}
	// Pop a batch and snapshot refs under a single critical section.
	// Batch is bounded (<=32 objects) so the section stays short.
	batch = h.mark.popBatch(batch)
	h.mark.trackStart()
	var refs []ObjectID
	for _, id := range batch {
		if obj, ok := h.objects[h.resolveLocked(id)]; ok {
			refs = append(refs, obj.refs...)
		}
	}
	return refs, true
}

// publishMarkRefs enqueues newly discovered references and retires one
// in-flight scanner, waking waiters when the closure completes.
func (h *Heap) publishMarkRefs(refs []ObjectID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.mark.isCancelled() {
		needSignal := false
		for _, ref := range refs {
			if ref == NullObject {
				continue
			}
			rid := h.resolveLocked(ref)
			if obj, ok := h.objects[rid]; ok && h.mark.mark(obj) {
				if h.mark.push(rid) {
					needSignal = true
				}
			}
		}
		if needSignal {
			h.markCond.Signal()
		}
	}
	if h.mark.trackDone() {
		h.markCond.Broadcast()
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
