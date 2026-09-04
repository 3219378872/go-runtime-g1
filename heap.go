package g1gc

import (
	"fmt"
	"sync"
)

// Heap owns the managed object graph and the G1 policy state. All metadata is
// protected by mu. world permits mutators during concurrent marking and blocks
// them at the stop-the-world phases.
//
// Allocation fast-path state (all protected by mu):
//   - freeStack/inFree track exactly the set of RegionFree regions.
//   - active caches one non-full region per normal kind (Eden/Survivor/Old)
//     so repeated allocation hits O(1) without scanning h.regions.
//   - usedTotal is the sum of live object sizes (O(1) UsedBytes).
//   - freeCap is the sum of capacities of free regions (O(1) reserve check).
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

	// Marking state: see mark.go.
	marking       bool
	satb          []ObjectID
	markQueue     []ObjectID
	markActive    int
	markCond      *sync.Cond
	markCancelled bool
	markEpoch     uint32

	// Allocation fast path: see alloc.go.
	freeStack []RegionID
	inFree    []bool
	active    [6]RegionID
	usedTotal int64
	freeCap   int64
	// rsRef counts cross-region reference slots per (src,dst) pair so
	// mutator writes maintain exact remembered sets in O(1) without
	// rescanning the source region. STW paths leave it stale and
	// finishCycle rebuilds it once.
	rsRef map[uint64]int

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
		config:    cfg,
		regions:   make([]*region, count),
		objects:   make(map[ObjectID]*object),
		forward:   make(map[ObjectID]ObjectID),
		roots:     make(map[ObjectID]struct{}),
		nextID:    1,
		state:     PhaseIdle,
		freeStack: make([]RegionID, 0, count),
		inFree:    make([]bool, count),
		rsRef:     make(map[uint64]int),
	}
	for i := range h.active {
		h.active[i] = RegionID(-1)
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
		h.freeStack = append(h.freeStack, RegionID(i))
		h.inFree[i] = true
		h.freeCap += capacity
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
	h.freeStack = h.freeStack[:0]
	h.freeCap = 0
	for i, r := range h.regions {
		h.freeStack = append(h.freeStack, r.id)
		h.inFree[i] = true
		h.freeCap += r.capacity
	}
	for i := range h.active {
		h.active[i] = RegionID(-1)
	}
	h.usedTotal = 0
	for k := range h.rsRef {
		delete(h.rsRef, k)
	}
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
