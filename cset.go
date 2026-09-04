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

func (h *Heap) selectCollectionSetLocked(stats *Stats) map[RegionID]bool {
	var young []collectionCandidate
	var old []collectionCandidate
	for _, r := range h.regions {
		if !r.kind.isNormal() {
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
