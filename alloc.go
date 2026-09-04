package g1gc

import "errors"

func (h *Heap) usedBytesLocked() int64 {
	return h.alloc.usedBytes()
}

// freeCapacityLocked reports the sum of capacities of free regions in O(1).
func (h *Heap) freeCapacityLocked() int64 {
	return h.alloc.freeBytes()
}

// pushFreeLocked returns a region to the free pool. Caller must have set
// r.kind to RegionFree already.
func (h *Heap) pushFreeLocked(r *region) {
	h.alloc.pushFree(r)
}

// popFreeLocked removes one free region that is not excluded, preferring the
// top of the stack for cache locality. Returns nil when none is available.
func (h *Heap) popFreeLocked(excluded map[RegionID]bool) *region {
	return h.alloc.popFree(h.regions, excluded)
}

func (h *Heap) clearActiveLocked(id RegionID) {
	h.alloc.clearActive(id)
}

// claimFreeAtLocked removes a specific free region from the free set.
// Returns false when the region is not free.
func (h *Heap) claimFreeAtLocked(idx int) bool {
	return h.alloc.claimAt(h.regions, idx)
}

// takeActiveLocked returns the cached active region for kind when it has room.
func (h *Heap) takeActiveLocked(kind RegionKind, size int64, excluded map[RegionID]bool) *region {
	return h.alloc.takeActive(h.regions, kind, size, excluded)
}

func (h *Heap) setActiveLocked(kind RegionKind, id RegionID) {
	h.alloc.setActive(kind, id)
}

// Allocate creates an object with no reference slots. Allocation may trigger
// one automatic cycle when the heap has no immediate space.
func (h *Heap) Allocate(size int64) (ObjectID, error) {
	return h.allocateObject(size, 0, nil)
}

// AllocateObject creates an object with a fixed number of reference slots.
func (h *Heap) AllocateObject(size int64, slots int) (ObjectID, error) {
	return h.allocateObject(size, slots, nil)
}

// AllocateWithRefs creates an object and initializes its reference slots.
func (h *Heap) AllocateWithRefs(size int64, refs []ObjectID) (ObjectID, error) {
	return h.allocateObject(size, len(refs), refs)
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
		h.alloc.addUsed(size)
		h.applyAllocationBarrierLocked(obj)
		h.recordAllocRememberedLocked(obj)
		return id, nil
	}

	start, span := h.findHumongousSpanLocked(size)
	if start < 0 {
		return NullObject, ErrOutOfMemory
	}
	for i := 0; i < span; i++ {
		h.claimFreeAtLocked(start + i)
		h.clearActiveLocked(RegionID(start + i))
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
	h.alloc.addUsed(size)
	h.applyAllocationBarrierLocked(obj)
	h.recordAllocRememberedLocked(obj)
	return id, nil
}

func (h *Heap) applyAllocationBarrierLocked(obj *object) {
	if !h.mark.marking {
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
	// Fast path: cached active region for this kind.
	if r := h.takeActiveLocked(kind, size, excluded); r != nil {
		return r
	}
	// Slow path: scan only regions of the requested kind (active missed
	// because it is full, excluded, or unset). This is rare; the common
	// case never reaches here.
	for _, r := range h.regions {
		if r.kind == kind && r.slack() >= size {
			if excluded != nil && excluded[r.id] {
				continue
			}
			h.setActiveLocked(kind, r.id)
			return r
		}
	}
	// No room: pop a free region in O(1) and convert it. Reuse maps to
	// avoid STW-time allocations.
	r := h.popFreeLocked(excluded)
	if r == nil || r.capacity < size {
		if r != nil {
			// Too small: return it and fail. Region sizes are uniform
			// except possibly the last one, so this is extremely rare.
			h.pushFreeLocked(r)
		}
		return nil
	}
	r.kind = kind
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
	h.setActiveLocked(kind, r.id)
	return r
}

// freeRegionLocked resets r and returns it to the free stack, reusing maps.
func (h *Heap) freeRegionLocked(r *region) {
	r.reset()
	h.pushFreeLocked(r)
}

func (h *Heap) findHumongousSpanLocked(size int64) (int, int) {
	// Single O(R) pass tracking the current free run instead of the old
	// O(R^2) nested scan. Respects the free pool so it stays consistent with
	// the free stack.
	runStart := -1
	var runCap int64
	for i, r := range h.regions {
		if r.kind != RegionFree {
			runStart = -1
			runCap = 0
			continue
		}
		if runStart < 0 {
			runStart = i
			runCap = 0
		}
		runCap += r.capacity
		if runCap >= size {
			return runStart, i - runStart + 1
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
	h.alloc.subUsed(obj.size)
	for i := 0; i < span; i++ {
		h.freeRegionLocked(h.regions[start+i])
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
	h.alloc.subUsed(obj.size)
	delete(h.objects, id)
}
