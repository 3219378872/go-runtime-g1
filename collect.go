package g1gc

import (
	"sort"
	"time"
)

type collectionCandidate struct {
	id       RegionID
	kind     RegionKind
	live     int64
	garbage  int64
	objects  int
	estimate time.Duration
}

func (h *Heap) cleanupLocked(stats *Stats) {
	for _, r := range h.regions {
		switch r.kind {
		case RegionFree, RegionHumongousContinue:
			continue
		case RegionHumongousStart:
			for id := range r.objects {
				obj, ok := h.objects[id]
				if !ok || !obj.marked {
					if ok {
						stats.ReclaimedBytes += obj.size
						delete(h.objects, id)
					}
					stats.FreedRegions += r.span
					r.reset()
					break
				}
				r.lastLiveBytes = obj.size
			}
		default:
			live := int64(0)
			for _, id := range r.objectIDs() {
				obj, ok := h.objects[id]
				if !ok {
					continue
				}
				if !obj.marked {
					stats.ReclaimedBytes += obj.size
					delete(h.objects, id)
					delete(r.objects, id)
					r.used -= obj.size
					continue
				}
				live += obj.size
			}
			r.lastLiveBytes = live
			if r.used == 0 {
				r.reset()
				stats.FreedRegions++
			}
		}
	}
	h.rebuildRememberedSetsLocked()
}

func (h *Heap) selectCollectionSetLocked(stats *Stats) map[RegionID]bool {
	var young []collectionCandidate
	var old []collectionCandidate
	for _, r := range h.regions {
		if r.kind != RegionEden && r.kind != RegionSurvivor && r.kind != RegionOld {
			continue
		}
		if r.used == 0 {
			continue
		}
		candidate := collectionCandidate{
			id:       r.id,
			kind:     r.kind,
			live:     r.lastLiveBytes,
			garbage:  r.capacity - r.lastLiveBytes,
			objects:  len(r.objects),
			estimate: h.pauseEstimate(r.lastLiveBytes, len(r.objects)),
		}
		if r.kind == RegionOld {
			old = append(old, candidate)
		} else {
			young = append(young, candidate)
		}
	}
	sort.Slice(young, func(i, j int) bool { return young[i].id < young[j].id })
	sort.Slice(old, func(i, j int) bool {
		if old[i].garbage == old[j].garbage {
			return old[i].id < old[j].id
		}
		return old[i].garbage > old[j].garbage
	})

	cset := make(map[RegionID]bool)
	budget := h.config.MaxPause
	var estimated time.Duration
	add := func(candidate collectionCandidate) {
		if budget > 0 && len(cset) > 0 && estimated+candidate.estimate > budget {
			stats.SkippedRegions = append(stats.SkippedRegions, candidate.id)
			return
		}
		cset[candidate.id] = true
		estimated += candidate.estimate
		stats.SelectedRegions = append(stats.SelectedRegions, candidate.id)
	}
	for _, candidate := range young {
		add(candidate)
	}
	oldAdded := 0
	for _, candidate := range old {
		if oldAdded >= h.config.MixedGCCount {
			stats.SkippedRegions = append(stats.SkippedRegions, candidate.id)
			continue
		}
		before := len(cset)
		add(candidate)
		if len(cset) != before {
			oldAdded++
		}
	}
	return cset
}

func (h *Heap) pauseEstimate(live int64, objects int) time.Duration {
	// The runtime has no platform-specific pause model. This deterministic
	// estimate gives MaxPause a useful policy meaning without confusing it with
	// wall-clock timing.
	estimate := time.Duration(objects) * time.Microsecond
	estimate += time.Duration(live/h.config.RegionSize) * time.Millisecond
	if estimate == 0 && objects > 0 {
		estimate = time.Microsecond
	}
	return estimate
}

func (h *Heap) allocateEvacuationCopyLocked(src *object, targetKind RegionKind, excluded map[RegionID]bool) (*object, bool) {
	hasExistingToSpace := false
	for _, r := range h.regions {
		if r.kind == targetKind && !excluded[r.id] && r.capacity-r.used >= src.size {
			hasExistingToSpace = true
			break
		}
	}
	if !hasExistingToSpace {
		free := int64(0)
		for _, r := range h.regions {
			if r.kind == RegionFree && !excluded[r.id] {
				free += r.capacity
			}
		}
		reserve := h.config.HeapSize * int64(h.config.EvacuationReservePercent) / 100
		if free-reserve < src.size {
			return nil, false
		}
	}
	r := h.findNormalRegionLocked(src.size, targetKind, excluded)
	if r == nil {
		return nil, false
	}
	id := h.nextID
	h.nextID++
	if h.nextID == NullObject {
		h.nextID++
	}
	age := src.age
	if src.humongous || src.region >= 0 && (h.regions[src.region].kind == RegionEden || h.regions[src.region].kind == RegionSurvivor) {
		if age < 255 {
			age++
		}
	}
	copyObj := &object{
		id:     id,
		size:   src.size,
		refs:   cloneIDs(src.refs),
		region: r.id,
		age:    age,
	}
	h.objects[id] = copyObj
	r.objects[id] = struct{}{}
	r.used += copyObj.size
	return copyObj, true
}

func (h *Heap) evacuateLocked(stats *Stats) error {
	cset := h.selectCollectionSetLocked(stats)
	if len(cset) == 0 {
		return nil
	}
	failed := make(map[RegionID]bool)
	for _, r := range h.regions {
		if !cset[r.id] {
			continue
		}
		for _, id := range r.objectIDs() {
			src, ok := h.objects[id]
			if !ok {
				continue
			}
			if src.pinned {
				failed[r.id] = true
				continue
			}
			targetKind := RegionOld
			if r.kind == RegionEden || r.kind == RegionSurvivor {
				nextAge := src.age
				if nextAge < 255 {
					nextAge++
				}
				if nextAge < h.config.MaxTenuringAge {
					targetKind = RegionSurvivor
				}
			}
			copyObj, copied := h.allocateEvacuationCopyLocked(src, targetKind, cset)
			if !copied && targetKind == RegionSurvivor {
				copyObj, copied = h.allocateEvacuationCopyLocked(src, RegionOld, cset)
			}
			if !copied {
				failed[r.id] = true
				continue
			}
			h.forward[id] = copyObj.id
			src.forwardedTo = copyObj.id
			stats.MovedObjects++
			stats.EvacuatedBytes += src.size
		}
	}

	// Update every root and every object reference before deleting source
	// objects. This also handles references from a failed source region.
	newRoots := make(map[ObjectID]struct{}, len(h.roots))
	for root := range h.roots {
		root = h.resolveLocked(root)
		if _, ok := h.objects[root]; ok {
			newRoots[root] = struct{}{}
		}
	}
	h.roots = newRoots
	for _, obj := range h.objects {
		for i, ref := range obj.refs {
			if ref != NullObject {
				obj.refs[i] = h.resolveLocked(ref)
			}
		}
	}

	for _, r := range h.regions {
		if !cset[r.id] {
			continue
		}
		for _, id := range r.objectIDs() {
			obj, ok := h.objects[id]
			if !ok {
				continue
			}
			if _, moved := h.forward[id]; moved {
				delete(h.objects, id)
				delete(r.objects, id)
				r.used -= obj.size
			}
		}
		if failed[r.id] {
			// A young region that cannot be fully evacuated is retained as old;
			// the next cycle can retry after reserve space becomes available.
			if r.kind == RegionEden || r.kind == RegionSurvivor {
				r.kind = RegionOld
				for id := range r.objects {
					h.objects[id].age = h.config.MaxTenuringAge
				}
			}
			r.lastLiveBytes = r.used
			continue
		}
		r.reset()
		stats.FreedRegions++
	}

	for id := range failed {
		stats.FailedRegions = append(stats.FailedRegions, id)
	}
	sort.Slice(stats.FailedRegions, func(i, j int) bool { return stats.FailedRegions[i] < stats.FailedRegions[j] })
	h.rebuildRememberedSetsLocked()
	if len(failed) > 0 {
		return ErrEvacuationFailure
	}
	return nil
}

func (h *Heap) finishCycleLocked(stats *Stats) {
	for _, obj := range h.objects {
		obj.marked = false
	}
	h.marking = false
	h.markCancelled = false
	h.satb = nil
	h.markQueue = nil
	h.markActive = 0
	h.state = PhaseIdle
	for _, r := range h.regions {
		if r.kind != RegionFree {
			r.lastLiveBytes = r.used
		}
	}
	h.rebuildRememberedSetsLocked()
	stats.AfterUsedBytes = h.usedBytesLocked()
}
