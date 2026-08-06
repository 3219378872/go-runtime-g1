package g1gc

import "sort"

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

func (r *region) objectIDs() []ObjectID {
	ids := make([]ObjectID, 0, len(r.objects))
	for id := range r.objects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (r *region) reset() {
	r.kind = RegionFree
	r.used = 0
	r.objects = make(map[ObjectID]struct{})
	r.rememberedFrom = make(map[RegionID]struct{})
	r.rememberedTo = make(map[RegionID]struct{})
	r.span = 0
	r.lastLiveBytes = 0
}
