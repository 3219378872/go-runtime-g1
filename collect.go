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
				if !ok || obj.markEpoch != h.markEpoch {
					if ok {
						stats.ReclaimedBytes += obj.size
						delete(h.objects, id)
						h.usedTotal -= obj.size
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
			for _, id := range r.objectIDsUnsorted() {
				obj, ok := h.objects[id]
				if !ok {
					continue
				}
				if obj.markEpoch != h.markEpoch {
					stats.ReclaimedBytes += obj.size
					delete(h.objects, id)
					delete(r.objects, id)
					r.used -= obj.size
					h.usedTotal -= obj.size
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
	// young is already in region-id order because h.regions is ordered;
	// the old explicit sort was pure overhead.
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
	// Fast path O(1): active region has room → no reserve check needed.
	// Slow path (active miss, rare): scan same-kind for slack; only when
	// no same-kind room exists do we consult the O(1) freeCap reserve.
	// cset never contains free regions, so freeCap needs no exclusion.
	if h.takeActiveLocked(targetKind, src.size, excluded) == nil {
		hasRoom := false
		for _, r := range h.regions {
			if r.kind == targetKind && r.capacity-r.used >= src.size {
				if excluded != nil && excluded[r.id] {
					continue
				}
				hasRoom = true
				break
			}
		}
		if !hasRoom {
			reserve := h.config.HeapSize * int64(h.config.EvacuationReservePercent) / 100
			if h.freeCap-reserve < src.size {
				return nil, false
			}
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
	h.usedTotal += copyObj.size
	return copyObj, true
}

func (h *Heap) evacuateLocked(stats *Stats) error {
	cset := h.selectCollectionSetLocked(stats)
	if len(cset) == 0 {
		return nil
	}
	failed := make(map[RegionID]bool)
	destSet := make(map[RegionID]bool)
	for _, r := range h.regions {
		if !cset[r.id] {
			continue
		}
		for _, id := range r.objectIDsUnsorted() {
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
			destSet[copyObj.region] = true
		}
	}
	if stats.MovedObjects == 0 {
		// Nothing moved: no reference can be stale. Still rebuild roots
		// canonically (cheap) and skip the rewrite scan entirely.
		newRoots := make(map[ObjectID]struct{}, len(h.roots))
		for root := range h.roots {
			root = h.resolveLocked(root)
			if _, ok := h.objects[root]; ok {
				newRoots[root] = struct{}{}
			}
		}
		h.roots = newRoots
	} else {
		h.rewriteForwardedRefsLocked(cset, destSet)
	}

	for _, r := range h.regions {
		if !cset[r.id] {
			continue
		}
		for _, id := range r.objectIDsUnsorted() {
			obj, ok := h.objects[id]
			if !ok {
				continue
			}
			if _, moved := h.forward[id]; moved {
				delete(h.objects, id)
				delete(r.objects, id)
				r.used -= obj.size
				h.usedTotal -= obj.size
			}
		}
		if failed[r.id] {
			// A young region that cannot be fully evacuated is retained as old;
			// the next cycle can retry after reserve space becomes available.
			if r.kind == RegionEden || r.kind == RegionSurvivor {
				h.clearActiveLocked(r.id)
				r.kind = RegionOld
				for id := range r.objects {
					h.objects[id].age = h.config.MaxTenuringAge
				}
			}
			r.lastLiveBytes = r.used
			continue
		}
		h.freeRegionLocked(r)
		stats.FreedRegions++
	}

	for id := range failed {
		stats.FailedRegions = append(stats.FailedRegions, id)
	}
	sort.Slice(stats.FailedRegions, func(i, j int) bool { return stats.FailedRegions[i] < stats.FailedRegions[j] })
	// RSet rebuild deferred to finishCycleLocked (single rebuild per cycle).
	if len(failed) > 0 {
		return ErrEvacuationFailure
	}
	return nil
}

// rewriteForwardedRefsLocked canonicalizes every reference that may point
// to a moved object. Roots are always fully rescanned (small set). Heap
// objects are rewritten only in affected regions:
//
//	cset (sources, incl. failed leftovers) + destSet (copy targets) +
//	RSet-sources pointing into cset.
//
// Safety: the RSet at this point is a stale superset for surviving regions
// (cleanup deletions are deferred to finishCycle; freed regions own no live
// refs), so the filter can only include extra regions, never miss a true
// edge. Copies' refs are covered via destSet. When the affected set covers
// most of the heap, a full scan is cheaper than set overhead and is used.
func (h *Heap) rewriteForwardedRefsLocked(cset, destSet map[RegionID]bool) {
	newRoots := make(map[ObjectID]struct{}, len(h.roots))
	for root := range h.roots {
		root = h.resolveLocked(root)
		if _, ok := h.objects[root]; ok {
			newRoots[root] = struct{}{}
		}
	}
	h.roots = newRoots

	affected := make(map[RegionID]bool, len(cset)+len(destSet)+8)
	for id := range cset {
		affected[id] = true
	}
	for id := range destSet {
		affected[id] = true
	}
	for id := range cset {
		if int(id) < 0 || int(id) >= len(h.regions) {
			continue
		}
		for src := range h.regions[id].rememberedFrom {
			affected[src] = true
		}
	}
	// Cost model: filtered rewrite touches sum(len(objects)) over affected
	// regions plus one map lookup per slot. Fall back to the full scan when
	// the filter retains most of the heap.
	affectedObjects := 0
	for id := range affected {
		if int(id) >= 0 && int(id) < len(h.regions) {
			affectedObjects += len(h.regions[id].objects)
		}
	}
	if affectedObjects*2 >= len(h.objects) {
		for _, obj := range h.objects {
			for i, ref := range obj.refs {
				if ref != NullObject {
					if resolved := h.resolveLocked(ref); resolved != ref {
						obj.refs[i] = resolved
					}
				}
			}
		}
		return
	}
	for id := range affected {
		if int(id) < 0 || int(id) >= len(h.regions) {
			continue
		}
		r := h.regions[id]
		for oid := range r.objects {
			obj, ok := h.objects[oid]
			if !ok {
				continue
			}
			for i, ref := range obj.refs {
				if ref != NullObject {
					if resolved := h.resolveLocked(ref); resolved != ref {
						obj.refs[i] = resolved
					}
				}
			}
		}
	}
}

func (h *Heap) finishCycleLocked(stats *Stats) {
	// No per-object mark clearing: the epoch is bumped at the next
	// beginMarkingLocked, invalidating all marks in O(1).
	h.marking = false
	h.markCancelled = false
	h.satb = h.satb[:0]
	h.markQueue = h.markQueue[:0]
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
