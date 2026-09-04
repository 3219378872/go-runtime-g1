package g1gc

func rsKey(src, dst RegionID) uint64 {
	return uint64(uint32(src))<<32 | uint64(uint32(dst))
}

// rsAddEdgeForSlotLocked adds one slot's cross-region edge in O(1).
func (h *Heap) rsAddEdgeForSlotLocked(source *object, ref ObjectID) {
	if ref == NullObject || source == nil {
		return
	}
	if source.region < 0 || int(source.region) >= len(h.regions) {
		return
	}
	target, ok := h.objects[h.resolveLocked(ref)]
	if !ok || target.region == source.region {
		return
	}
	if target.region < 0 || int(target.region) >= len(h.regions) {
		return
	}
	k := rsKey(source.region, target.region)
	if h.rsRef[k] == 0 {
		h.regions[target.region].rememberedFrom[source.region] = struct{}{}
		h.regions[source.region].rememberedTo[target.region] = struct{}{}
	}
	h.rsRef[k]++
}

// rsRemoveEdgeForSlotLocked withdraws one slot's edge in O(1).
func (h *Heap) rsRemoveEdgeForSlotLocked(source *object, ref ObjectID) {
	if ref == NullObject || source == nil {
		return
	}
	if source.region < 0 || int(source.region) >= len(h.regions) {
		return
	}
	target, ok := h.objects[h.resolveLocked(ref)]
	if !ok || target.region == source.region {
		return
	}
	k := rsKey(source.region, target.region)
	n, ok := h.rsRef[k]
	if !ok || n <= 0 {
		return
	}
	if n == 1 {
		delete(h.rsRef, k)
		if int(target.region) < len(h.regions) && target.region >= 0 {
			delete(h.regions[target.region].rememberedFrom, source.region)
			// Only drop the forward edge when no other dst from src remains.
			// Since counts are per-pair, zero here means the pair is gone.
			delete(h.regions[source.region].rememberedTo, target.region)
		}
		return
	}
	h.rsRef[k] = n - 1
}

// recordAllocRememberedLocked indexes all refs of a freshly allocated object.
// Cost is O(slots), not O(region size).
func (h *Heap) recordAllocRememberedLocked(obj *object) {
	for _, ref := range obj.refs {
		h.rsAddEdgeForSlotLocked(obj, ref)
	}
}

func (h *Heap) rebuildRememberedSetsLocked() {
	for _, r := range h.regions {
		if r.rememberedFrom == nil {
			r.rememberedFrom = make(map[RegionID]struct{})
		} else {
			for s := range r.rememberedFrom {
				delete(r.rememberedFrom, s)
			}
		}
		if r.rememberedTo == nil {
			r.rememberedTo = make(map[RegionID]struct{})
		} else {
			for t := range r.rememberedTo {
				delete(r.rememberedTo, t)
			}
		}
	}
	for k := range h.rsRef {
		delete(h.rsRef, k)
	}
	for _, obj := range h.objects {
		for _, ref := range obj.refs {
			h.rsAddEdgeForSlotLocked(obj, ref)
		}
	}
}
