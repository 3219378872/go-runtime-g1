package g1gc

import (
	"errors"
	"sort"
)

// Allocate creates an object with no reference slots. Allocation may trigger
// one automatic cycle when the heap has no immediate space.
func (h *Heap) Allocate(size int64) (ObjectID, error) {
	return h.allocateObject(size, 0, nil)
}

// Alloc is an alias for Allocate.
func (h *Heap) Alloc(size int64) (ObjectID, error) {
	return h.Allocate(size)
}

// AllocateObject creates an object with a fixed number of reference slots.
func (h *Heap) AllocateObject(size int64, slots int) (ObjectID, error) {
	return h.allocateObject(size, slots, nil)
}

// AllocObject is an alias for AllocateObject.
func (h *Heap) AllocObject(size int64, slots int) (ObjectID, error) {
	return h.AllocateObject(size, slots)
}

// AllocateWithRefs creates an object and initializes its reference slots.
func (h *Heap) AllocateWithRefs(size int64, refs []ObjectID) (ObjectID, error) {
	return h.allocateObject(size, len(refs), refs)
}

// AllocWithRefs is an alias for AllocateWithRefs.
func (h *Heap) AllocWithRefs(size int64, refs []ObjectID) (ObjectID, error) {
	return h.AllocateWithRefs(size, refs)
}

func (h *Heap) allocateObject(size int64, slots int, refs []ObjectID) (ObjectID, error) {
	if size <= 0 {
		return NullObject, ErrInvalidSize
	}
	if slots < 0 || len(refs) > 0 && len(refs) != slots {
		return NullObject, ErrInvalidSlot
	}
	if refs == nil && slots > 0 {
		refs = make([]ObjectID, slots)
	}
	if len(refs) == 0 && slots == 0 {
		refs = nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		h.world.RLock()
		h.mu.Lock()
		if err := h.checkOpenLocked(); err != nil {
			h.mu.Unlock()
			h.world.RUnlock()
			return NullObject, err
		}
		id, err := h.allocateLocked(size, refs)
		state := h.state
		h.mu.Unlock()
		h.world.RUnlock()
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrOutOfMemory) || state != PhaseIdle || attempt != 0 {
			return NullObject, err
		}
		// Allocation failure is the normal trigger for a stop-the-world G1
		// cycle. A failed evacuation is recoverable, so retry the allocation.
		if _, gcErr := h.GC(); gcErr != nil && !errors.Is(gcErr, ErrEvacuationFailure) {
			return NullObject, gcErr
		}
	}
	return NullObject, ErrOutOfMemory
}

func (h *Heap) allocateLocked(size int64, refs []ObjectID) (ObjectID, error) {
	canonicalRefs := make([]ObjectID, len(refs))
	for i, ref := range refs {
		if ref == NullObject {
			continue
		}
		ref = h.resolveLocked(ref)
		if _, ok := h.objects[ref]; !ok {
			return NullObject, ErrInvalidReference
		}
		canonicalRefs[i] = ref
	}

	id := h.nextID
	h.nextID++
	if h.nextID == NullObject {
		h.nextID++
	}
	if size <= h.config.RegionSize/2 {
		r := h.findNormalRegionLocked(size, RegionEden, nil)
		if r == nil {
			return NullObject, ErrOutOfMemory
		}
		obj := &object{
			id:     id,
			size:   size,
			refs:   canonicalRefs,
			region: r.id,
		}
		h.objects[id] = obj
		r.objects[id] = struct{}{}
		r.used += size
		h.applyAllocationBarrierLocked(obj)
		h.recordRememberedLocked(obj)
		return id, nil
	}

	start, span := h.findHumongousSpanLocked(size)
	if start < 0 {
		return NullObject, ErrOutOfMemory
	}
	obj := &object{
		id:        id,
		size:      size,
		refs:      canonicalRefs,
		region:    RegionID(start),
		humongous: true,
		span:      span,
	}
	h.objects[id] = obj
	h.regions[start].kind = RegionHumongousStart
	h.regions[start].used = size
	h.regions[start].span = span
	h.regions[start].objects[id] = struct{}{}
	for i := 1; i < span; i++ {
		r := h.regions[start+i]
		r.kind = RegionHumongousContinue
		r.used = r.capacity
		r.span = 0
	}
	h.applyAllocationBarrierLocked(obj)
	h.recordRememberedLocked(obj)
	return id, nil
}

func (h *Heap) applyAllocationBarrierLocked(obj *object) {
	if !h.marking {
		return
	}
	// A newly allocated object is treated as black. The insertion barrier also
	// marks every pre-existing object inserted into its slots.
	h.markObjectLocked(obj.id)
	for _, ref := range obj.refs {
		if ref != NullObject {
			h.markObjectLocked(ref)
		}
	}
}

func (h *Heap) findNormalRegionLocked(size int64, kind RegionKind, excluded map[RegionID]bool) *region {
	for _, r := range h.regions {
		if r.kind == kind && !excluded[r.id] && r.capacity-r.used >= size {
			return r
		}
	}
	for _, r := range h.regions {
		if r.kind == RegionFree && !excluded[r.id] && r.capacity >= size {
			r.kind = kind
			r.used = 0
			r.objects = make(map[ObjectID]struct{})
			r.rememberedFrom = make(map[RegionID]struct{})
			r.rememberedTo = make(map[RegionID]struct{})
			return r
		}
	}
	return nil
}

func (h *Heap) findHumongousSpanLocked(size int64) (int, int) {
	for start := 0; start < len(h.regions); start++ {
		if h.regions[start].kind != RegionFree {
			continue
		}
		var capacity int64
		for end := start; end < len(h.regions); end++ {
			if h.regions[end].kind != RegionFree {
				break
			}
			capacity += h.regions[end].capacity
			if capacity >= size {
				return start, end - start + 1
			}
		}
	}
	return -1, 0
}

func (h *Heap) releaseHumongousLocked(obj *object) {
	start := int(obj.region)
	span := obj.span
	if start < 0 || start+span > len(h.regions) {
		return
	}
	for i := 0; i < span; i++ {
		h.regions[start+i].reset()
	}
}

func (h *Heap) deleteObjectLocked(id ObjectID) {
	obj, ok := h.objects[id]
	if !ok {
		return
	}
	if obj.humongous {
		delete(h.objects, id)
		h.releaseHumongousLocked(obj)
		return
	}
	if obj.region >= 0 && int(obj.region) < len(h.regions) {
		r := h.regions[obj.region]
		delete(r.objects, id)
		r.used -= obj.size
		if r.used < 0 {
			r.used = 0
		}
	}
	delete(h.objects, id)
}

// AddRoot adds a handle to the managed root set.
func (h *Heap) AddRoot(id ObjectID) error {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.checkOpenLocked(); err != nil {
		return err
	}
	if id == NullObject {
		return ErrInvalidReference
	}
	id = h.resolveLocked(id)
	if _, ok := h.objects[id]; !ok {
		return ErrInvalidObject
	}
	h.roots[id] = struct{}{}
	if h.marking {
		h.markObjectLocked(id)
	}
	return nil
}

// RemoveRoot removes a handle from the managed root set. Removing an absent
// root is intentionally idempotent.
func (h *Heap) RemoveRoot(id ObjectID) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.roots, h.resolveLocked(id))
	delete(h.roots, id)
}

// Roots returns the current canonical root handles.
func (h *Heap) Roots() []ObjectID {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	roots := make([]ObjectID, 0, len(h.roots))
	for id := range h.roots {
		roots = append(roots, h.resolveLocked(id))
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	return roots
}

// SetReference stores a managed reference and applies both the SATB
// pre-write barrier and the remembered-set/insertion barriers.
func (h *Heap) SetReference(owner ObjectID, slot int, target ObjectID) error {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.checkOpenLocked(); err != nil {
		return err
	}
	obj, err := h.objectLocked(owner)
	if err != nil {
		return err
	}
	if slot < 0 || slot >= len(obj.refs) {
		return ErrInvalidSlot
	}
	if target != NullObject {
		target = h.resolveLocked(target)
		if _, ok := h.objects[target]; !ok {
			return ErrInvalidReference
		}
	}
	old := obj.refs[slot]
	if h.marking && old != NullObject {
		// SATB records the value that was visible before the mutation.
		h.satb = append(h.satb, old)
	}
	obj.refs[slot] = target
	if h.marking && target != NullObject {
		// The insertion barrier prevents a black object from pointing at white.
		h.markObjectLocked(target)
	}
	h.recordRememberedLocked(obj)
	return nil
}

// SetRef is an alias for SetReference.
func (h *Heap) SetRef(owner ObjectID, slot int, target ObjectID) error {
	return h.SetReference(owner, slot, target)
}

// ClearReference removes a reference from a slot.
func (h *Heap) ClearReference(owner ObjectID, slot int) error {
	return h.SetReference(owner, slot, NullObject)
}

// Reference returns one canonical reference from an object.
func (h *Heap) Reference(owner ObjectID, slot int) (ObjectID, error) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, err := h.objectLocked(owner)
	if err != nil {
		return NullObject, err
	}
	if slot < 0 || slot >= len(obj.refs) {
		return NullObject, ErrInvalidSlot
	}
	return h.resolveLocked(obj.refs[slot]), nil
}

// GetReference is an alias for Reference.
func (h *Heap) GetReference(owner ObjectID, slot int) (ObjectID, error) {
	return h.Reference(owner, slot)
}

// References returns a copy of all canonical references from an object.
func (h *Heap) References(id ObjectID) ([]ObjectID, error) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, err := h.objectLocked(id)
	if err != nil {
		return nil, err
	}
	refs := cloneIDs(obj.refs)
	for i, ref := range refs {
		refs[i] = h.resolveLocked(ref)
	}
	return refs, nil
}

// Resolve returns the current object for an old handle after evacuation.
func (h *Heap) Resolve(id ObjectID) (ObjectID, bool) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == NullObject {
		return NullObject, false
	}
	id = h.resolveLocked(id)
	if _, ok := h.objects[id]; !ok {
		return NullObject, false
	}
	return id, true
}

// IsAlive reports whether a handle resolves to a live object.
func (h *Heap) IsAlive(id ObjectID) bool {
	_, ok := h.Resolve(id)
	return ok
}

// ObjectInfo returns metadata for a live object.
func (h *Heap) ObjectInfo(id ObjectID) (ObjectInfo, error) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, err := h.objectLocked(id)
	if err != nil {
		return ObjectInfo{}, err
	}
	r := h.regions[obj.region]
	return ObjectInfo{
		ID:         obj.id,
		Size:       obj.size,
		Region:     obj.region,
		Kind:       r.kind,
		Age:        obj.age,
		Pinned:     obj.pinned,
		References: len(obj.refs),
	}, nil
}

// Pin prevents evacuation of an object until Unpin is called. This models
// native handles that cannot be relocated during a pause.
func (h *Heap) Pin(id ObjectID) error {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, err := h.objectLocked(id)
	if err != nil {
		return err
	}
	obj.pinned = true
	return nil
}

// Unpin permits future evacuation of an object.
func (h *Heap) Unpin(id ObjectID) error {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	obj, err := h.objectLocked(id)
	if err != nil {
		return err
	}
	obj.pinned = false
	return nil
}

func (h *Heap) recordRememberedLocked(source *object) {
	// A source object may have several cross-region slots. Recomputing the
	// source's entries keeps a write to one slot from erasing another slot's
	// remembered-set edge.
	if source.region < 0 || int(source.region) >= len(h.regions) {
		return
	}
	sourceRegion := h.regions[source.region]
	for target := range sourceRegion.rememberedTo {
		delete(h.regions[target].rememberedFrom, source.region)
	}
	sourceRegion.rememberedTo = make(map[RegionID]struct{})
	for id := range sourceRegion.objects {
		obj, ok := h.objects[id]
		if !ok {
			continue
		}
		for _, ref := range obj.refs {
			if ref == NullObject {
				continue
			}
			targetObj, ok := h.objects[h.resolveLocked(ref)]
			if ok && targetObj.region != source.region {
				h.regions[targetObj.region].rememberedFrom[source.region] = struct{}{}
				sourceRegion.rememberedTo[targetObj.region] = struct{}{}
			}
		}
	}
}

func (h *Heap) rebuildRememberedSetsLocked() {
	for _, r := range h.regions {
		r.rememberedFrom = make(map[RegionID]struct{})
		r.rememberedTo = make(map[RegionID]struct{})
	}
	for _, obj := range h.objects {
		for _, ref := range obj.refs {
			if ref == NullObject {
				continue
			}
			target, ok := h.objects[h.resolveLocked(ref)]
			if !ok || target.region == obj.region {
				continue
			}
			h.regions[target.region].rememberedFrom[obj.region] = struct{}{}
			h.regions[obj.region].rememberedTo[target.region] = struct{}{}
		}
	}
}

// RegionSnapshot returns a consistent snapshot of all heap regions.
func (h *Heap) RegionSnapshot() []RegionInfo {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RegionInfo, 0, len(h.regions))
	for _, r := range h.regions {
		remembered := make([]RegionID, 0, len(r.rememberedFrom))
		for source := range r.rememberedFrom {
			remembered = append(remembered, source)
		}
		sort.Slice(remembered, func(i, j int) bool { return remembered[i] < remembered[j] })
		live := int64(0)
		for id := range r.objects {
			if obj, ok := h.objects[id]; ok {
				live += obj.size
			}
		}
		out = append(out, RegionInfo{
			ID:             r.id,
			Kind:           r.kind,
			Capacity:       r.capacity,
			Used:           r.used,
			LiveBytes:      live,
			ObjectCount:    len(r.objects),
			RememberedFrom: remembered,
			Span:           r.span,
		})
	}
	return out
}

// RememberedSet returns source regions recorded for a target region.
func (h *Heap) RememberedSet(id RegionID) ([]RegionID, error) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	r, err := h.regionLocked(id)
	if err != nil {
		return nil, err
	}
	ids := make([]RegionID, 0, len(r.rememberedFrom))
	for source := range r.rememberedFrom {
		ids = append(ids, source)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// RegionCount returns the fixed number of regions in the heap.
func (h *Heap) RegionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.regions)
}

// ObjectCount returns the number of current live objects.
func (h *Heap) ObjectCount() int {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.objects)
}
