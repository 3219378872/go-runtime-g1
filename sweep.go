package g1gc

func (h *Heap) cleanupLocked(stats *Stats) {
	for _, r := range h.regions {
		switch r.kind {
		case RegionFree, RegionHumongousContinue:
			continue
		case RegionHumongousStart:
			for id := range r.objects {
				obj, ok := h.objects[id]
				if !ok || !h.mark.isMarked(obj) {
					if ok {
						stats.ReclaimedBytes += obj.size
						delete(h.objects, id)
						h.alloc.subUsed(obj.size)
					}
					stats.FreedRegions += r.span
					span := r.span
					start := int(r.id)
					for i := 0; i < span && start+i < len(h.regions); i++ {
						h.freeRegionLocked(h.regions[start+i])
					}
					break
				}
				r.lastLiveBytes = obj.size
			}
		default:
			live := int64(0)
			for _, id := range r.memberIDs() {
				obj, ok := h.objects[id]
				if !ok {
					continue
				}
				if !h.mark.isMarked(obj) {
					stats.ReclaimedBytes += obj.size
					delete(h.objects, id)
					delete(r.objects, id)
					r.used -= obj.size
					h.alloc.subUsed(obj.size)
					continue
				}
				live += obj.size
			}
			r.lastLiveBytes = live
			if r.used == 0 {
				h.freeRegionLocked(r)
				stats.FreedRegions++
			}
		}
	}
	// RSet rebuild deferred to finishCycleLocked (single rebuild per cycle).
}
