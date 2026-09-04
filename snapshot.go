package g1gc

import "sort"

func (h *Heap) UsedBytes() int64 {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.usedBytesLocked()
}

// RegionSnapshot returns a consistent snapshot of all heap regions.
func (h *Heap) RegionSnapshot() []RegionInfo {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RegionInfo, 0, len(h.regions))
	for _, r := range h.regions {
		remembered := make([]RegionID, 0, len(r.rememberedFrom))
		for source := range r.rememberedFrom {
			remembered = append(remembered, source)
		}
		sort.Slice(remembered, func(i, j int) bool { return remembered[i] < remembered[j] })
		// r.used is maintained incrementally and equals the sum of member
		// object sizes; the old per-object summation loop was O(N).
		// Humongous continuations carry reserve accounting in used but own
		// no objects, so their live bytes stay 0 as before.
		live := r.used
		if r.kind == RegionHumongousContinue {
			live = 0
		}
		out = append(out, RegionInfo{
			ID:             r.id,
			Kind:           r.kind,
			Capacity:       r.capacity,
			Used:           r.used,
			LiveBytes:      live,
			ObjectCount:    len(r.objects),
			RememberedFrom: remembered,
			Span:           r.span,
		})
	}
	return out
}

// RememberedSet returns source regions recorded for a target region.
func (h *Heap) RememberedSet(id RegionID) ([]RegionID, error) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	r, err := h.regionLocked(id)
	if err != nil {
		return nil, err
	}
	ids := make([]RegionID, 0, len(r.rememberedFrom))
	for source := range r.rememberedFrom {
		ids = append(ids, source)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// RegionCount returns the fixed number of regions in the heap.
func (h *Heap) RegionCount() int {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.regions)
}

// ObjectCount returns the number of current live objects.
func (h *Heap) ObjectCount() int {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.objects)
}
