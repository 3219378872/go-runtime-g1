package g1gc

// RegionKind identifies the role of a heap region in the current collection
// cycle.
type RegionKind uint8

const (
	RegionFree RegionKind = iota
	RegionEden
	RegionSurvivor
	RegionOld
	RegionHumongousStart
	RegionHumongousContinue
)

func (k RegionKind) String() string {
	switch k {
	case RegionFree:
		return "free"
	case RegionEden:
		return "eden"
	case RegionSurvivor:
		return "survivor"
	case RegionOld:
		return "old"
	case RegionHumongousStart:
		return "humongous-start"
	case RegionHumongousContinue:
		return "humongous-continue"
	default:
		return "unknown"
	}
}

type region struct {
	id             RegionID
	kind           RegionKind
	capacity       int64
	used           int64
	objects        map[ObjectID]struct{}
	rememberedFrom map[RegionID]struct{}
	rememberedTo   map[RegionID]struct{}
	span           int
	lastLiveBytes  int64
}

// reset returns a region to the free state, clearing maps in place so
// STW paths do not churn the Go GC with tiny map allocations.
func (r *region) reset() {
	r.kind = RegionFree
	r.used = 0
	if r.objects == nil {
		r.objects = make(map[ObjectID]struct{})
	} else {
		for id := range r.objects {
			delete(r.objects, id)
		}
	}
	if r.rememberedFrom == nil {
		r.rememberedFrom = make(map[RegionID]struct{})
	} else {
		for id := range r.rememberedFrom {
			delete(r.rememberedFrom, id)
		}
	}
	if r.rememberedTo == nil {
		r.rememberedTo = make(map[RegionID]struct{})
	} else {
		for id := range r.rememberedTo {
			delete(r.rememberedTo, id)
		}
	}
	r.span = 0
	r.lastLiveBytes = 0
}

// memberIDs returns member IDs without sorting. Use this on hot paths;
// sort only at API/test boundaries that require determinism.
// isNormal reports whether the region holds ordinary (non-humongous,
// non-free) objects and therefore participates in collection sets and free
// accounting.
func (k RegionKind) isNormal() bool {
	return k == RegionEden || k == RegionSurvivor || k == RegionOld
}

// slack reports the remaining capacity available to normal allocation.
func (r *region) slack() int64 {
	return r.capacity - r.used
}

func (r *region) memberIDs() []ObjectID {
	ids := make([]ObjectID, 0, len(r.objects))
	for id := range r.objects {
		ids = append(ids, id)
	}
	return ids
}

// RegionInfo is a read-only region snapshot. RememberedFrom lists source
// regions that currently contain references into this region.
type RegionInfo struct {
	ID             RegionID
	Kind           RegionKind
	Capacity       int64
	Used           int64
	LiveBytes      int64
	ObjectCount    int
	RememberedFrom []RegionID
	Span           int
}
