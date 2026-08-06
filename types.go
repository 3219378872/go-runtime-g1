package g1gc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ObjectID is a stable handle into the managed object heap. A handle remains
// usable after evacuation; Resolve follows the forwarding chain to the new
// object.
type ObjectID uint64

const NullObject ObjectID = 0

// RegionID is the zero-based identifier of a heap region.
type RegionID int

// Phase is the externally observable G1 cycle phase.
type Phase uint8

const (
	PhaseIdle Phase = iota
	PhaseInitialMark
	PhaseConcurrentMark
	PhaseRemark
	PhaseCleanup
	PhaseEvacuation
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseInitialMark:
		return "initial-mark"
	case PhaseConcurrentMark:
		return "concurrent-mark"
	case PhaseRemark:
		return "remark"
	case PhaseCleanup:
		return "cleanup"
	case PhaseEvacuation:
		return "evacuation"
	default:
		return "unknown"
	}
}

// Cause describes why a collection was requested.
type Cause uint8

const (
	CauseExplicit Cause = iota
	CauseAllocationFailure
	CausePeriodic
)

func (c Cause) String() string {
	switch c {
	case CauseExplicit:
		return "explicit"
	case CauseAllocationFailure:
		return "allocation-failure"
	case CausePeriodic:
		return "periodic"
	default:
		return "unknown"
	}
}

var (
	ErrInvalidConfig     = errors.New("g1gc: invalid configuration")
	ErrInvalidSize       = errors.New("g1gc: object size must be positive")
	ErrInvalidObject     = errors.New("g1gc: object does not exist")
	ErrInvalidReference  = errors.New("g1gc: reference target does not exist")
	ErrInvalidSlot       = errors.New("g1gc: reference slot is out of range")
	ErrOutOfMemory       = errors.New("g1gc: managed heap is out of memory")
	ErrEvacuationFailure = errors.New("g1gc: evacuation failed for at least one region")
	ErrCycleInProgress   = errors.New("g1gc: collection cycle already in progress")
	ErrContextCancelled  = errors.New("g1gc: collection cancelled")
	ErrInvalidRegion     = errors.New("g1gc: region does not exist")
	ErrAlreadyClosed     = errors.New("g1gc: heap is closed")
)

// Config controls the managed heap and the G1 policy. Zero values for policy
// fields are filled with defaults by New.
type Config struct {
	HeapSize                 int64
	RegionSize               int64
	GCWorkers                int
	MaxPause                 time.Duration
	MaxTenuringAge           uint8
	MixedGCCount             int
	InitiatingHeapOccupancy  int
	EvacuationReservePercent int
}

// DefaultConfig returns a practical configuration for a managed heap of the
// supplied size. RegionSize is chosen close to the G1 target of at most 2048
// regions and rounded to a power of two.
func DefaultConfig(heapSize int64) Config {
	regionSize := int64(1 << 20)
	if heapSize > 0 {
		for regionSize > 1 && heapSize/regionSize < 1024 {
			regionSize >>= 1
		}
		for regionSize < 32<<10 && regionSize < heapSize {
			regionSize <<= 1
		}
		if regionSize > heapSize {
			regionSize = heapSize
		}
	}
	return Config{
		HeapSize:                 heapSize,
		RegionSize:               regionSize,
		GCWorkers:                1,
		MaxTenuringAge:           3,
		MixedGCCount:             8,
		InitiatingHeapOccupancy:  45,
		EvacuationReservePercent: 10,
	}
}

func (c Config) normalized() (Config, error) {
	if c.HeapSize <= 0 || c.RegionSize <= 0 || c.RegionSize > c.HeapSize {
		return Config{}, ErrInvalidConfig
	}
	if c.MaxPause < 0 {
		return Config{}, ErrInvalidConfig
	}
	if c.GCWorkers <= 0 {
		c.GCWorkers = 1
	}
	if c.MaxTenuringAge == 0 {
		c.MaxTenuringAge = 3
	}
	if c.MixedGCCount <= 0 {
		c.MixedGCCount = 8
	}
	if c.InitiatingHeapOccupancy <= 0 {
		c.InitiatingHeapOccupancy = 45
	}
	if c.InitiatingHeapOccupancy > 100 {
		return Config{}, ErrInvalidConfig
	}
	if c.EvacuationReservePercent < 0 || c.EvacuationReservePercent >= 100 {
		return Config{}, ErrInvalidConfig
	}
	return c, nil
}

type object struct {
	id          ObjectID
	size        int64
	refs        []ObjectID
	region      RegionID
	age         uint8
	marked      bool
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

// Stats describes one complete G1 cycle.
type Stats struct {
	Cycle                  uint64
	Cause                  Cause
	Completed              bool
	Phases                 []Phase
	PhaseDurations         map[Phase]time.Duration
	PauseDuration          time.Duration
	ConcurrentMarkDuration time.Duration
	BeforeUsedBytes        int64
	AfterUsedBytes         int64
	MarkedObjects          int
	MarkedBytes            int64
	ReclaimedBytes         int64
	MovedObjects           int
	EvacuatedBytes         int64
	FreedRegions           int
	SelectedRegions        []RegionID
	FailedRegions          []RegionID
	SkippedRegions         []RegionID
}

// Heap owns the managed object graph and the G1 policy state. All metadata is
// protected by mu. world permits mutators during concurrent marking and blocks
// them at the stop-the-world phases.
type Heap struct {
	mu      sync.Mutex
	world   sync.RWMutex
	cycleMu sync.Mutex
	closed  bool

	config  Config
	regions []*region
	objects map[ObjectID]*object
	forward map[ObjectID]ObjectID
	roots   map[ObjectID]struct{}
	nextID  ObjectID
	cycle   uint64
	state   Phase

	marking       bool
	satb          []ObjectID
	markQueue     []ObjectID
	markActive    int
	markCond      *sync.Cond
	markCancelled bool

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
	}
	h.markCond = sync.NewCond(&h.mu)
	return h, nil
}

// NewHeap is an explicit alias for New for callers that prefer constructor
// names which distinguish the managed heap from Go's process heap.
func NewHeap(cfg Config) (*Heap, error) {
	return New(cfg)
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
	h.mu.Unlock()
	h.world.Unlock()
}

func (h *Heap) checkOpenLocked() error {
	if h.closed {
		return ErrAlreadyClosed
	}
	return nil
}

func cloneIDs(ids []ObjectID) []ObjectID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]ObjectID, len(ids))
	copy(out, ids)
	return out
}

func clonePhaseDurations(in map[Phase]time.Duration) map[Phase]time.Duration {
	out := make(map[Phase]time.Duration, len(in))
	for phase, duration := range in {
		out[phase] = duration
	}
	return out
}

func cloneStats(in Stats) Stats {
	in.Phases = clonePhases(in.Phases)
	in.PhaseDurations = clonePhaseDurations(in.PhaseDurations)
	in.SelectedRegions = cloneRegionIDs(in.SelectedRegions)
	in.FailedRegions = cloneRegionIDs(in.FailedRegions)
	in.SkippedRegions = cloneRegionIDs(in.SkippedRegions)
	return in
}

func clonePhases(in []Phase) []Phase {
	if len(in) == 0 {
		return nil
	}
	out := make([]Phase, len(in))
	copy(out, in)
	return out
}

func cloneRegionIDs(in []RegionID) []RegionID {
	if len(in) == 0 {
		return nil
	}
	out := make([]RegionID, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (h *Heap) resolveLocked(id ObjectID) ObjectID {
	if id == NullObject {
		return NullObject
	}
	// Forwarding chains are normally one or two links. The bounded walk avoids
	// allocating a temporary set for every reference while still treating a
	// corrupted cycle as invalid.
	for steps := 0; ; steps++ {
		next, ok := h.forward[id]
		if !ok || next == NullObject {
			return id
		}
		if steps > len(h.forward) {
			return NullObject
		}
		id = next
	}
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

func (h *Heap) phaseLocked(phase Phase) {
	h.state = phase
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

func (h *Heap) UsedBytes() int64 {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.usedBytesLocked()
}

func (h *Heap) usedBytesLocked() int64 {
	var total int64
	for _, obj := range h.objects {
		total += obj.size
	}
	return total
}

func (h *Heap) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("g1 heap: regions=%d objects=%d used=%d phase=%s", len(h.regions), len(h.objects), h.usedBytesLocked(), h.state)
}

// Collect performs one full G1-style cycle. Mutators are allowed during the
// concurrent-mark phase and are stopped for the other phases.
func (h *Heap) Collect(ctx context.Context, cause Cause) (Stats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.cycleMu.Lock()
	defer h.cycleMu.Unlock()

	if err := ctx.Err(); err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	if err := h.checkOpenLocked(); err != nil {
		h.mu.Unlock()
		h.world.Unlock()
		return Stats{}, err
	}
	if h.state != PhaseIdle {
		h.mu.Unlock()
		h.world.Unlock()
		return Stats{}, ErrCycleInProgress
	}
	h.cycle++
	stats := Stats{
		Cycle:           h.cycle,
		Cause:           cause,
		BeforeUsedBytes: h.usedBytesLocked(),
		PhaseDurations:  make(map[Phase]time.Duration),
	}
	h.phaseLocked(PhaseInitialMark)
	initialStart := time.Now()
	h.beginMarkingLocked()
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseInitialMark)
	stats.PhaseDurations[PhaseInitialMark] = time.Since(initialStart)

	h.mu.Lock()
	h.phaseLocked(PhaseConcurrentMark)
	h.mu.Unlock()
	concurrentStart := time.Now()
	err := h.runConcurrentMark(ctx)
	concurrentDuration := time.Since(concurrentStart)
	stats.Phases = append(stats.Phases, PhaseConcurrentMark)
	stats.PhaseDurations[PhaseConcurrentMark] = concurrentDuration
	stats.ConcurrentMarkDuration = concurrentDuration
	if err != nil {
		h.abortCycle()
		return stats, err
	}

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Remark, cleanup, and evacuation are stop-the-world phases. The shared
	// mutex makes every public mutator wait while these snapshots change.
	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseRemark)
	remarkStart := time.Now()
	h.finishMarkingLocked()
	h.collectMarkStatsLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseRemark)
	stats.PhaseDurations[PhaseRemark] = time.Since(remarkStart)

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseCleanup)
	cleanupStart := time.Now()
	h.cleanupLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseCleanup)
	stats.PhaseDurations[PhaseCleanup] = time.Since(cleanupStart)

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseEvacuation)
	evacuationStart := time.Now()
	evacuationErr := h.evacuateLocked(&stats)
	h.finishCycleLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseEvacuation)
	stats.PhaseDurations[PhaseEvacuation] = time.Since(evacuationStart)

	stats.PauseDuration = stats.PhaseDurations[PhaseInitialMark] + stats.PhaseDurations[PhaseRemark] + stats.PhaseDurations[PhaseCleanup] + stats.PhaseDurations[PhaseEvacuation]
	stats.AfterUsedBytes = h.UsedBytes()
	stats.Completed = true
	h.mu.Lock()
	h.lastStats = cloneStats(stats)
	h.mu.Unlock()
	if evacuationErr != nil {
		// An evacuation failure is recoverable: failed regions retain their live
		// objects and the cycle still completes after sweeping dead objects.
		return stats, evacuationErr
	}
	return stats, nil
}

// GC is the concise explicit-collection form.
func (h *Heap) GC() (Stats, error) {
	return h.Collect(context.Background(), CauseExplicit)
}
