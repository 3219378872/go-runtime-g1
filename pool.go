package g1gc

// freePool tracks exactly the set of RegionFree regions: a LIFO stack for
// cache locality, a membership bitmap, and the summed free capacity for O(1)
// reserve checks. All methods require the caller to hold Heap.mu (they are
// only reached through allocator or the Heap *Locked wrappers).
type freePool struct {
	stack []RegionID
	inSet []bool
	free  int64
}

func newFreePool(regionCount int) freePool {
	return freePool{
		stack: make([]RegionID, 0, regionCount),
		inSet: make([]bool, regionCount),
	}
}

func (p *freePool) push(id RegionID, capacity int64) {
	idx := int(id)
	if idx < 0 || idx >= len(p.inSet) {
		return
	}
	if p.inSet[idx] {
		return
	}
	p.inSet[idx] = true
	p.stack = append(p.stack, id)
	p.free += capacity
}

func (p *freePool) pop(regions []*region, excluded map[RegionID]bool) *region {
	for len(p.stack) > 0 {
		top := p.stack[len(p.stack)-1]
		if excluded != nil && excluded[top] {
			// Excluded top: linear probe from the top. The collection set
			// is normally tiny, so this degrades gracefully instead of
			// failing.
			found := -1
			for i := len(p.stack) - 1; i >= 0; i-- {
				if !excluded[p.stack[i]] {
					found = i
					break
				}
			}
			if found < 0 {
				return nil
			}
			top = p.stack[found]
			p.stack[found] = p.stack[len(p.stack)-1]
			p.stack = p.stack[:len(p.stack)-1]
			p.inSet[int(top)] = false
			p.free -= regions[top].capacity
			return regions[top]
		}
		p.stack = p.stack[:len(p.stack)-1]
		p.inSet[int(top)] = false
		p.free -= regions[top].capacity
		return regions[top]
	}
	return nil
}

func (p *freePool) claim(regions []*region, idx int) bool {
	if idx < 0 || idx >= len(regions) || !p.inSet[idx] {
		return false
	}
	id := RegionID(idx)
	for i, fid := range p.stack {
		if fid == id {
			p.stack[i] = p.stack[len(p.stack)-1]
			p.stack = p.stack[:len(p.stack)-1]
			break
		}
	}
	p.inSet[idx] = false
	p.free -= regions[idx].capacity
	return true
}

func (p *freePool) reset() {
	p.stack = p.stack[:0]
	for i := range p.inSet {
		p.inSet[i] = false
	}
	p.free = 0
}

// activeCache remembers one non-full region per normal kind (Eden, Survivor,
// Old) so repeated allocation hits O(1) without scanning all regions.
type activeCache [6]RegionID

func (a *activeCache) reset() {
	for i := range a {
		a[i] = RegionID(-1)
	}
}

func (a *activeCache) set(kind RegionKind, id RegionID) {
	if int(kind) >= 0 && int(kind) < len(a) {
		a[kind] = id
	}
}

func (a *activeCache) clear(id RegionID) {
	for k := range a {
		if a[k] == id {
			a[k] = RegionID(-1)
		}
	}
}

func (a *activeCache) get(kind RegionKind) (RegionID, bool) {
	if int(kind) < 0 || int(kind) >= len(a) {
		return RegionID(-1), false
	}
	id := a[kind]
	if id < 0 {
		return RegionID(-1), false
	}
	return id, true
}

// allocator owns the allocation fast path: the free pool, the active-region
// cache, and the used-bytes total behind UsedBytes. It never locks; every
// method requires Heap.mu, enforced by reaching it only through Heap or the
// *Locked wrappers in alloc.go.
type allocator struct {
	pool   freePool
	active activeCache
	used   int64
}

func newAllocator(regionCount int) allocator {
	a := allocator{pool: newFreePool(regionCount)}
	a.active.reset()
	return a
}

func (a *allocator) pushFree(r *region) {
	a.pool.push(r.id, r.capacity)
	a.active.clear(r.id)
}

func (a *allocator) popFree(regions []*region, excluded map[RegionID]bool) *region {
	return a.pool.pop(regions, excluded)
}

func (a *allocator) claimAt(regions []*region, idx int) bool {
	return a.pool.claim(regions, idx)
}

func (a *allocator) takeActive(regions []*region, kind RegionKind, size int64, excluded map[RegionID]bool) *region {
	id, ok := a.active.get(kind)
	if !ok || int(id) >= len(regions) {
		return nil
	}
	if excluded != nil && excluded[id] {
		return nil
	}
	r := regions[id]
	if r.kind != kind || r.slack() < size {
		return nil
	}
	return r
}

func (a *allocator) setActive(kind RegionKind, id RegionID) {
	a.active.set(kind, id)
}

func (a *allocator) clearActive(id RegionID) {
	a.active.clear(id)
}

func (a *allocator) addUsed(n int64) {
	a.used += n
}

func (a *allocator) subUsed(n int64) {
	a.used -= n
}

func (a *allocator) usedBytes() int64 {
	return a.used
}

func (a *allocator) freeBytes() int64 {
	return a.pool.free
}

func (a *allocator) reset() {
	a.pool.reset()
	a.active.reset()
	a.used = 0
}
