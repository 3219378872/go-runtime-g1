package g1gc

import (
	"fmt"
	"sync"
)

// Heap owns the managed object graph and the G1 policy state. All metadata is
// protected by mu. world permits mutators during concurrent marking and blocks
// them at the stop-the-world phases.
//
// Allocation fast-path state (all protected by mu, see pool.go): the free
// pool tracks exactly the set of RegionFree regions, the active cache keeps
// one non-full region per normal kind so repeated allocation hits O(1)
// without scanning h.regions, and used is the sum of live object sizes for
// O(1) UsedBytes.
type Heap struct {
	mu      sync.Mutex
	world   sync.RWMutex
	cycleMu sync.Mutex
	closed  bool

	// Graph state: see refs.go.
	config  Config
	regions []*region
	objects map[ObjectID]*object
	forward map[ObjectID]ObjectID
	roots   map[ObjectID]struct{}
	nextID  ObjectID
	cycle   uint64
	state   Phase

	// Marking state machine: see marker.go. markCond stays here because it
	// is bound to mu; the marker itself never locks.
	mark     marker
	markCond *sync.Cond

	// Allocation fast path: see alloc.go and pool.go.
	alloc allocator
	// Remembered-set edge counts: see rset.go. STW paths leave it stale and
	// finishCycle rebuilds it once.
	rset rsetIndex

	lastStats Stats
}

// New creates an empty managed heap.
func New(cfg Config) (*Heap, error) {
	if cfg.RegionSize == 0 && cfg.HeapSize > 0 {
		cfg.RegionSize = DefaultConfig(cfg.HeapSize).RegionSize
	}
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return nil, err
	}
	count := int((cfg.HeapSize-1)/cfg.RegionSize + 1)
	if count <= 0 {
		return nil, ErrInvalidConfig
	}
	h := &Heap{
		config:  cfg,
		regions: make([]*region, count),
		objects: make(map[ObjectID]*object),
		forward: make(map[ObjectID]ObjectID),
		roots:   make(map[ObjectID]struct{}),
		nextID:  1,
		state:   PhaseIdle,
		alloc:   newAllocator(count),
		rset:    newRSetIndex(),
	}
	for i := range h.regions {
		capacity := cfg.RegionSize
		if remaining := cfg.HeapSize - int64(i)*cfg.RegionSize; remaining < capacity {
			capacity = remaining
		}
		h.regions[i] = &region{
			id:             RegionID(i),
			kind:           RegionFree,
			capacity:       capacity,
			objects:        make(map[ObjectID]struct{}),
			rememberedFrom: make(map[RegionID]struct{}),
			rememberedTo:   make(map[RegionID]struct{}),
		}
		h.alloc.pushFree(h.regions[i])
	}
	h.markCond = sync.NewCond(&h.mu)
	return h, nil
}

// Close releases the logical heap. Existing handles become invalid.
func (h *Heap) Close() {
	h.cycleMu.Lock()
	defer h.cycleMu.Unlock()
	h.world.Lock()
	h.mu.Lock()
	h.closed = true
	h.objects = make(map[ObjectID]*object)
	h.forward = make(map[ObjectID]ObjectID)
	h.roots = make(map[ObjectID]struct{})
	for _, r := range h.regions {
		r.reset()
	}
	h.alloc.reset()
	for _, r := range h.regions {
		h.alloc.pushFree(r)
	}
	h.rset.clear()
	h.mu.Unlock()
	h.world.Unlock()
}

func (h *Heap) checkOpenLocked() error {
	if h.closed {
		return ErrAlreadyClosed
	}
	return nil
}

func (h *Heap) resolveLocked(id ObjectID) ObjectID {
	if id == NullObject {
		return NullObject
	}
	// Fast path: no forwarding.
	if _, ok := h.forward[id]; !ok {
		return id
	}
	// Slow path: walk the chain with path compression so repeated
	// resolves of old handles stay O(1) amortized across cycles.
	// Bounded to avoid hanging on a corrupted cycle.
	const maxChain = 64
	orig := id
	var path []ObjectID
	for steps := 0; steps < maxChain; steps++ {
		next, ok := h.forward[id]
		if !ok || next == NullObject {
			for _, p := range path {
				h.forward[p] = id
			}
			return id
		}
		if next == id {
			return NullObject
		}
		path = append(path, id)
		id = next
		// Fast exit: tail has no further forwarding.
		if _, ok := h.forward[id]; !ok {
			for _, p := range path {
				h.forward[p] = id
			}
			return id
		}
	}
	_ = orig
	return NullObject
}

func (h *Heap) objectLocked(id ObjectID) (*object, error) {
	id = h.resolveLocked(id)
	obj, ok := h.objects[id]
	if !ok {
		return nil, ErrInvalidObject
	}
	return obj, nil
}

func (h *Heap) regionLocked(id RegionID) (*region, error) {
	if id < 0 || int(id) >= len(h.regions) {
		return nil, ErrInvalidRegion
	}
	return h.regions[id], nil
}

func (h *Heap) State() Phase {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *Heap) Config() Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.config
}

func (h *Heap) LastStats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneStats(h.lastStats)
}

func (h *Heap) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("g1 heap: regions=%d objects=%d used=%d phase=%s", len(h.regions), len(h.objects), h.usedBytesLocked(), h.state)
}
