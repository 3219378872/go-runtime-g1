package g1gc

func rsKey(src, dst RegionID) uint64 {
	return uint64(uint32(src))<<32 | uint64(uint32(dst))
}

// rsetIndex counts cross-region reference slots per (src, dst) pair so
// mutator writes maintain exact remembered sets in O(1) without rescanning
// the source region. It is pure bookkeeping over region IDs: the Heap
// methods below translate first/zero transitions into rememberedFrom/To
// updates. STW paths leave it stale and finishCycle rebuilds it once.
type rsetIndex struct {
	counts map[uint64]int
}

func newRSetIndex() rsetIndex {
	return rsetIndex{counts: make(map[uint64]int)}
}

// add records one slot edge and reports whether the pair is new.
func (x *rsetIndex) add(src, dst RegionID) bool {
	k := rsKey(src, dst)
	if x.counts[k] == 0 {
		x.counts[k] = 1
		return true
	}
	x.counts[k]++
	return false
}

// remove withdraws one slot edge and reports whether the pair is gone.
func (x *rsetIndex) remove(src, dst RegionID) bool {
	k := rsKey(src, dst)
	n, ok := x.counts[k]
	if !ok || n <= 0 {
		return false
	}
	if n == 1 {
		delete(x.counts, k)
		return true
	}
	x.counts[k] = n - 1
	return false
}

func (x *rsetIndex) clear() {
	clear(x.counts)
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
	if h.rset.add(source.region, target.region) {
		h.regions[target.region].rememberedFrom[source.region] = struct{}{}
		h.regions[source.region].rememberedTo[target.region] = struct{}{}
	}
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
	if h.rset.remove(source.region, target.region) {
		if int(target.region) < len(h.regions) && target.region >= 0 {
			delete(h.regions[target.region].rememberedFrom, source.region)
			// Only drop the forward edge when no other dst from src remains.
			// Since counts are per-pair, removal here means the pair is gone.
			delete(h.regions[source.region].rememberedTo, target.region)
		}
	}
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
			clear(r.rememberedFrom)
		}
		if r.rememberedTo == nil {
			r.rememberedTo = make(map[RegionID]struct{})
		} else {
			clear(r.rememberedTo)
		}
	}
	h.rset.clear()
	for _, obj := range h.objects {
		for _, ref := range obj.refs {
			h.rsAddEdgeForSlotLocked(obj, ref)
		}
	}
}
