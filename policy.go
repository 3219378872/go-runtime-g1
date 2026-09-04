package g1gc

import "context"

// HeapSize returns the configured managed heap size.
func (h *Heap) HeapSize() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.config.HeapSize
}

// FreeBytes reports capacity that can currently be used by normal allocation.
// A humongous span is considered fully occupied until its start object is
// swept, including unused bytes in its last region.
func (h *Heap) FreeBytes() int64 {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.freeBytesLocked()
}

func (h *Heap) freeBytesLocked() int64 {
	var free int64
	for _, r := range h.regions {
		if r.kind == RegionFree {
			free += r.capacity
		} else if r.kind.isNormal() {
			free += r.slack()
		}
	}
	return free
}

// OccupancyPercent returns current managed-object occupancy relative to the
// configured heap size.
func (h *Heap) OccupancyPercent() float64 {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return float64(h.usedBytesLocked()) * 100 / float64(h.config.HeapSize)
}

// ShouldStartCycle implements the IHOP policy check used by a periodic GC
// trigger. Explicit GC calls always start a cycle regardless of this value.
func (h *Heap) ShouldStartCycle() bool {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.state != PhaseIdle {
		return false
	}
	return h.usedBytesLocked()*100 >= int64(h.config.InitiatingHeapOccupancy)*h.config.HeapSize
}

// MaybeCollect starts a periodic cycle when the configured IHOP threshold is
// reached. The bool reports whether a cycle was started.
func (h *Heap) MaybeCollect(ctx context.Context) (Stats, bool, error) {
	if !h.ShouldStartCycle() {
		return Stats{}, false, nil
	}
	stats, err := h.Collect(ctx, CausePeriodic)
	return stats, true, err
}
