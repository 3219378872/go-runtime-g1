package g1gc

type object struct {
	id          ObjectID
	size        int64
	refs        []ObjectID
	region      RegionID
	age         uint8
	markEpoch   uint32
	pinned      bool
	forwardedTo ObjectID
	humongous   bool
	span        int
}

// ObjectInfo is a read-only description of a currently live object.
type ObjectInfo struct {
	ID         ObjectID
	Size       int64
	Region     RegionID
	Kind       RegionKind
	Age        uint8
	Pinned     bool
	References int
}

func cloneIDs(ids []ObjectID) []ObjectID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]ObjectID, len(ids))
	copy(out, ids)
	return out
}
