package g1gc

import "sort"

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
	if h.mark.marking {
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

// canonicalizeRootsLocked resolves every root through the forwarding table
// and drops handles to dead objects. Callers must hold mu.
func (h *Heap) canonicalizeRootsLocked() {
	newRoots := make(map[ObjectID]struct{}, len(h.roots))
	for root := range h.roots {
		root = h.resolveLocked(root)
		if _, ok := h.objects[root]; ok {
			newRoots[root] = struct{}{}
		}
	}
	h.roots = newRoots
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
	if h.mark.marking && old != NullObject {
		// SATB records the value that was visible before the mutation.
		h.mark.recordSATB(old)
	}
	// Incremental RSet: withdraw the old edge and add the new one, each
	// O(1) via refcounts. This replaces the old full-region rescan.
	h.rsRemoveEdgeForSlotLocked(obj, old)
	obj.refs[slot] = target
	if h.mark.marking && target != NullObject {
		// The insertion barrier prevents a black object from pointing at white.
		h.markObjectLocked(target)
	}
	h.rsAddEdgeForSlotLocked(obj, target)
	return nil
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
