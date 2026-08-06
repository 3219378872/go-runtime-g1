package g1gc

import (
	"context"
	"fmt"
	"sync"
)

func (h *Heap) beginMarkingLocked() {
	h.marking = true
	h.markCancelled = false
	h.satb = nil
	h.markQueue = nil
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

// markObjectLocked is the shared mark primitive used by root scanning, the
// concurrent workers, the SATB drain, and insertion/allocation barriers.
func (h *Heap) markObjectLocked(id ObjectID) {
	if id == NullObject {
		return
	}
	id = h.resolveLocked(id)
	obj, ok := h.objects[id]
	if !ok || obj.marked {
		return
	}
	obj.marked = true
	h.markQueue = append(h.markQueue, id)
	h.markCond.Broadcast()
}

func (h *Heap) snapshotRefs(id ObjectID) []ObjectID {
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, ok := h.objects[h.resolveLocked(id)]
	if !ok {
		return nil
	}
	return cloneIDs(obj.refs)
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
		last := len(h.markQueue) - 1
		id := h.markQueue[last]
		h.markQueue = h.markQueue[:last]
		h.markActive++
		h.mu.Unlock()

		refs := h.snapshotRefs(id)

		h.mu.Lock()
		if !h.markCancelled {
			for _, ref := range refs {
				h.markObjectLocked(ref)
			}
		}
		h.markActive--
		h.markCond.Broadcast()
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
	h.satb = nil
	h.markQueue = nil
	h.markActive = 0
}

func (h *Heap) collectMarkStatsLocked(stats *Stats) {
	for _, obj := range h.objects {
		if obj.marked {
			stats.MarkedObjects++
			stats.MarkedBytes += obj.size
		}
	}
}

func (h *Heap) abortCycle() {
	h.world.Lock()
	h.mu.Lock()
	for _, obj := range h.objects {
		obj.marked = false
	}
	h.marking = false
	h.markCancelled = true
	h.satb = nil
	h.markQueue = nil
	h.markActive = 0
	h.state = PhaseIdle
	h.markCond.Broadcast()
	h.mu.Unlock()
	h.world.Unlock()
}
