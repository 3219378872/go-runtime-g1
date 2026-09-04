package g1gc

import "context"

// HeapSize returns the configured managed heap size.
func (h *Heap) HeapSize() int64 {
	var size int64
	h.withReader(func() { size = h.config.HeapSize })
	return size
}

// FreeBytes reports capacity that can currently be used by normal allocation.
// A humongous span is considered fully occupied until its start object is
// swept, including unused bytes in its last region.
func (h *Heap) FreeBytes() int64 {
	var free int64
	h.withReader(func() { free = h.freeBytesLocked() })
	return free
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
	var pct float64
	h.withReader(func() { pct = float64(h.usedBytesLocked()) * 100 / float64(h.config.HeapSize) })
	return pct
}

// ShouldStartCycle implements the IHOP policy check used by a periodic GC
// trigger. Explicit GC calls always start a cycle regardless of this value.
func (h *Heap) ShouldStartCycle() bool {
	var start bool
	h.withReader(func() {
		if h.closed || h.state != PhaseIdle {
			return
		}
		start = h.usedBytesLocked()*100 >= int64(h.config.InitiatingHeapOccupancy)*h.config.HeapSize
	})
	return start
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
