package g1gc

import "fmt"

// Validate checks the invariants that make a collection cycle safe. It is
// intentionally available to integration tests and diagnostics; production
// callers do not need to run it on every mutator operation.
func (h *Heap) Validate() error {
	return h.withReaderErr(func() error {
		if err := h.checkOpenLocked(); err != nil {
			return err
		}
		return h.validateLocked()
	})
}

func (h *Heap) validateLocked() error {
	seenSpan := make(map[RegionID]bool)
	for _, r := range h.regions {
		switch r.kind {
		case RegionFree:
			if r.used != 0 || len(r.objects) != 0 || r.span != 0 {
				return fmt.Errorf("g1gc: free region %d is not empty", r.id)
			}
		case RegionHumongousContinue:
			if r.used != r.capacity || len(r.objects) != 0 || r.span != 0 {
				return fmt.Errorf("g1gc: invalid humongous continuation %d", r.id)
			}
		default:
			if r.used < 0 || r.used > r.capacity {
				return fmt.Errorf("g1gc: region %d used=%d capacity=%d", r.id, r.used, r.capacity)
			}
		}
		if r.kind == RegionHumongousStart {
			if r.span <= 0 || int(r.id)+r.span > len(h.regions) {
				return fmt.Errorf("g1gc: invalid humongous span at region %d", r.id)
			}
			for i := 1; i < r.span; i++ {
				continuation := h.regions[int(r.id)+i]
				if continuation.kind != RegionHumongousContinue || seenSpan[continuation.id] {
					return fmt.Errorf("g1gc: invalid continuation for region %d", r.id)
				}
				seenSpan[continuation.id] = true
			}
		}
	}

	var used int64
	for id, obj := range h.objects {
		if id == NullObject || obj.id != id {
			return fmt.Errorf("g1gc: object identity mismatch for %d", id)
		}
		if obj.region < 0 || int(obj.region) >= len(h.regions) {
			return fmt.Errorf("g1gc: object %d has invalid region %d", id, obj.region)
		}
		r := h.regions[obj.region]
		if _, ok := r.objects[id]; !ok {
			return fmt.Errorf("g1gc: object %d missing from region %d", id, obj.region)
		}
		if obj.humongous {
			if r.kind != RegionHumongousStart || obj.span != r.span || obj.span <= 0 {
				return fmt.Errorf("g1gc: object %d has invalid humongous metadata", id)
			}
		} else if r.kind == RegionHumongousStart || r.kind == RegionHumongousContinue || obj.size > h.config.RegionSize/2 {
			return fmt.Errorf("g1gc: normal object %d has invalid region or size", id)
		}
		used += obj.size
		for _, ref := range obj.refs {
			if ref == NullObject {
				continue
			}
			resolved := h.resolveLocked(ref)
			if resolved == NullObject {
				return fmt.Errorf("g1gc: reference from %d has a forwarding cycle", id)
			}
			if _, ok := h.objects[resolved]; !ok {
				return fmt.Errorf("g1gc: reference from %d points to dead object %d", id, ref)
			}
		}
	}
	if used != h.usedBytesLocked() {
		return fmt.Errorf("g1gc: used-byte accounting mismatch: objects=%d heap=%d", used, h.usedBytesLocked())
	}
	for root := range h.roots {
		resolved := h.resolveLocked(root)
		if resolved != root {
			return fmt.Errorf("g1gc: root %d is not canonical", root)
		}
		if _, ok := h.objects[root]; !ok {
			return fmt.Errorf("g1gc: root %d is dead", root)
		}
	}

	expected := make(map[RegionID]map[RegionID]bool)
	for _, obj := range h.objects {
		for _, ref := range obj.refs {
			if ref == NullObject {
				continue
			}
			target, ok := h.objects[h.resolveLocked(ref)]
			if !ok || target.region == obj.region {
				continue
			}
			if expected[target.region] == nil {
				expected[target.region] = make(map[RegionID]bool)
			}
			expected[target.region][obj.region] = true
		}
	}
	for _, r := range h.regions {
		actual := make(map[RegionID]bool, len(r.rememberedFrom))
		for source := range r.rememberedFrom {
			actual[source] = true
		}
		want := expected[r.id]
		if len(actual) != len(want) {
			return fmt.Errorf("g1gc: remembered set mismatch for region %d", r.id)
		}
		for source := range want {
			if !actual[source] {
				return fmt.Errorf("g1gc: remembered set missing %d -> %d", source, r.id)
			}
		}
	}
	for _, r := range h.regions {
		for target := range r.rememberedTo {
			if target < 0 || int(target) >= len(h.regions) {
				return fmt.Errorf("g1gc: source region %d points to invalid target %d", r.id, target)
			}
			if _, ok := h.regions[target].rememberedFrom[r.id]; !ok {
				return fmt.Errorf("g1gc: reverse remembered edge missing %d -> %d", r.id, target)
			}
		}
	}
	return nil
}
