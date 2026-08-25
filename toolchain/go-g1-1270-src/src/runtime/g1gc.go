// Copyright 2026 The Go Runtime G1 project.
//
// Experimental region accounting and collection-set scheduling for the real Go
// runtime. The policy is kept separate from object movement until evacuation
// has been proven against the runtime's precise pointer maps.
package runtime

import (
	"internal/runtime/atomic"
	"internal/runtime/sys"
	"unsafe"
)

const (
	g1RegionShift = 20 // 1 MiB logical regions, matching the initial G1 policy.
	g1RegionCount = 1 << 16
	g1RegionPages = (1 << g1RegionShift) / pageSize

	// The queue is bounded so a pathological heap cannot make the runtime
	// allocate metadata during mark termination. Spans beyond this limit are
	// still swept by the regular central queues.
	g1CandidateSpanLimit = 1 << 18

	// The inbound index is built while the normal marker scans live objects.
	// It is deliberately bounded: if a process has more edges than the
	// static table can hold, evacuation falls back to the old full rewrite.
	g1InboundSpanLimit = 1 << 20

	// Collection-set scheduling is a pause-budgeted priority queue. The
	// ordinary sweeper still covers every span; this queue only changes which
	// low-live spans are swept first, so it must not grow with the whole heap.
	g1CollectionSetMaxBytes = 2 << 20
	// Rebuilding the priority hint is not required for correctness because the
	// central sweeper remains authoritative. Refresh it periodically to keep
	// the selection walk from becoming a fixed cost of every short GC cycle.
	g1CollectionSetPeriod = 16
)

type g1RegionStats struct {
	generation  atomic.Uint64
	liveBytes   atomic.Uint64
	liveObjects atomic.Uint64
	liveGen     atomic.Uint64
	usedListed  atomic.Uint32
	dirty       atomic.Uint32
	usedBytes   atomic.Uint64
	usedObjects atomic.Uint64
	// usedPos is this region's slot in g1UsedRegions while listed. It is
	// written only by the registration paths and by stop-the-world
	// retirement, which cannot overlap.
	usedPos uint32
	// scanTag gates which regions a stop-the-world heap sweep touches. Both
	// fields are only manipulated while the world is stopped.
	scanTag uint32
}

// The table is static because runtime code must not allocate metadata from the
// managed heap while a GC cycle is being initialized. The index is a bounded
// address hash; it is exact for the normal process heap range and still safe
// as telemetry if a process maps more than 64 GiB of heap address space.
var g1Regions [g1RegionCount]g1RegionStats

var g1UsedRegions [g1RegionCount]uintptr
var g1UsedCount atomic.Uint64
var g1UsedInitialized atomic.Uint32

// Regions whose spans were allocated into, swept, or freed since the last
// mark termination. The list is appended to concurrently under the per-region
// dirty flag and drained only while the world is stopped, so the
// mark-termination refresh walks touched regions instead of allspans.
var g1DirtyList [g1RegionCount]uintptr
var g1DirtyCount atomic.Uint64

// Scratch space for the g1evac=4 authoritative recount. Only touched by the
// stop-the-world validation pass.
var g1ValidateScratch [g1RegionCount]uint64

// Accumulation buffers for one mark-termination refresh. The heap sweep
// adds every span it finds in a dirty region's windows here; the reconcile
// pass then moves the totals into the region structs. Indexed by the same
// hashed region index as the tables above.
var g1RefreshUsed [g1RegionCount]uint64
var g1RefreshObjects [g1RegionCount]uint64
var g1RefreshLive [g1RegionCount]uint64
var g1RefreshLiveObjects [g1RegionCount]uint64

// g1ScanTag is the current generation of scanTag marks on regions.
var g1ScanTag uint32

// g1StickyRegions gates which target regions the marker indexes inbound
// edges for. It holds the union of the previous window's selected regions,
// so per-pointer recording costs track the candidate set rather than the
// whole heap; regions outside it become candidates only after a window
// without recording converges. Seeded to all-ones so the very first window
// observes everything.
var g1StickyRegions [g1RegionCount / 64]uint64

func init() {
	for i := range g1StickyRegions {
		g1StickyRegions[i] = ^uint64(0)
	}
}

//go:nosplit
func g1RegionSticky(index uintptr) bool {
	return g1StickyRegions[index/64]>>(index%64)&1 != 0
}

// g1LowLiveRegions lists the used regions whose live bytes are below half of
// their used bytes. Evacuation can only select spans from these regions, so
// the list lets selection walk region span lists instead of all of allspans.
// It is rebuilt at mark termination while the world is stopped.
// g1EdgeTables holds the per-P inbound-edge dedup tables. The tables live
// outside the p struct: adding 8KB to p shifted the hot scheduler and GC
// fields' cache layout and measurably increased mark-term stall frequency.
// The index is the P id; a P beyond the static table falls back to the
// conservative full-heap rewrite via g1InboundOverflow.
var g1EdgeTables [64][g1EdgeBufSize]g1EdgeEntry

var g1LowLiveRegions [g1RegionCount]uintptr
var g1LowLiveCount uint64

type g1InboundRegion struct {
	head   atomic.Uint32
	listed atomic.Uint32
}

// g1EdgeBufSize is the per-P dedup table size for inbound edges. Each P
// records at most one (owner span, target region) pair per cycle here; if the
// table fills, the conservative full-heap rewrite fallback is armed instead.
const g1EdgeBufSize = 512

// g1EdgeEntry is one deduplicated inbound edge slot. owner is stored as a
// uintptr so the concurrent marking paths (including the write barrier flush,
// which forbids write barriers) can publish it without barriers.
type g1EdgeEntry struct {
	owner  uintptr
	region uintptr
}

type g1InboundSpanEdge struct {
	owner *mspan
	next  uint32
}

// g1InboundRegions maps a target logical region to heap spans containing
// pointer slots which referenced that region during marking. The edge table is
// off-heap-lifetime metadata and is reset at the next STW cycle start.
var g1InboundRegions [g1RegionCount]g1InboundRegion
var g1InboundTouched [g1RegionCount]uintptr
var g1InboundTouchedCount atomic.Uint64
var g1InboundSpanEdges [g1InboundSpanLimit]g1InboundSpanEdge
var g1InboundSpanCount atomic.Uint64

// Diagnostic accumulation of per-cycle time spent in inbound recording.
var g1EvacRecordNs atomic.Int64
var g1InboundOverflow atomic.Uint32

var g1LastCycle struct {
	liveBytes        atomic.Uint64
	liveObjects      atomic.Uint64
	liveRegions      atomic.Uint64
	usedBytes        atomic.Uint64
	usedObjects      atomic.Uint64
	usedRegions      atomic.Uint64
	candidateRegions atomic.Uint64
	candidateSpans   atomic.Uint64
	reclaimBytes     atomic.Uint64
}

// g1CandidateSpans is a bounded, off-heap-lifetime queue of spans selected at
// mark termination. It is published before the world is restarted, then
// consumed by concurrent sweepers with g1CandidateNext. A span can remain in
// its central unswept set as well; sweepgen ownership makes that duplicate
// queueing harmless.
var g1CandidateSpans [g1CandidateSpanLimit]*mspan
var g1CandidateCount atomic.Uint64
var g1CandidateNext atomic.Uint64

// g1GlobalRootRegions records, per logical region, whether a non-stack root
// (data/BSS globals, the finalizer and cleanup queues, span specials, or
// mutator-time special registration) held a pointer into it while marking ran
// during an evacuation-window cycle. Stack-derived targets are deliberately
// absent: scanblock's stk parameter distinguishes them, and stack slots are
// rewritten by the stop-the-world rescan instead. Selection excludes only the
// regions listed here.
var g1GlobalRootRegions [g1RegionCount / 8]byte

// g1WindowAllocRegions tracks the logical regions that received heap
// allocations while an evacuation index was active. Objects allocated during
// a window cycle are black and are never scanned by that cycle's marker, and
// bulk copies into their fresh memory (slice growth, untyped memmove) leave
// no write-barrier entries, so inbound edges originating there cannot be
// captured by the edge index. Selection therefore adds these regions to the
// stop-the-world rewrite coverage. The bitmap is the fast membership test;
// the bounded list drives the rewrite sweep. Both are manipulated while the
// world is stopped or under the same per-P dedup discipline as the edge
// tables, and both reset at window start.
var g1WindowAllocRegions [g1RegionCount / 64]uint64
var g1WindowAllocList [512]uintptr
var g1WindowAllocCount uint64

// g1gcNoteWindowAllocRegion records that a span in this region was populated
// by an allocation batch during the active evacuation window. Called from the
// mcache batch hooks; the world may be running.
//
//go:nosplit
func g1gcNoteWindowAllocRegion(s *mspan) {
	if g1EvacIndexActive == 0 {
		return
	}
	index := g1gcRegionIndex(s.base())
	word := &g1WindowAllocRegions[index/64]
	mask := uint64(1) << (index % 64)
	for {
		old := atomic.Load64(word)
		if old&mask != 0 {
			return
		}
		if atomic.Cas64(word, old, old|mask) {
			break
		}
	}
	position := atomic.Xadd64(&g1WindowAllocCount, 1) - 1
	if position >= uint64(len(g1WindowAllocList)) {
		// More distinct allocating regions than the rewrite sweep can
		// cover: degrade to the conservative no-evacuation path for this
		// window rather than risk a missed edge.
		g1InboundOverflow.Store(1)
		return
	}
	g1WindowAllocList[position] = index
}

// g1DebugForwardCount counts pointer rewrites performed by
// g1gcForwardPointer. Stop-the-world diagnostic state used by the g1evac=4
// crosscheck to prove that skipped root rescans were safe.
var g1DebugForwardCount int

// g1gcMarkRootRegion notes that a root slot pointed into p's region. Called
// from the root scanning hot paths while an evacuation index is active;
// spanOfHeap filters non-heap targets so stack and metadata addresses cannot
// poison unrelated hashed regions.
//
//go:nosplit
func g1gcMarkRootRegion(p uintptr) {
	if p == 0 || spanOfHeap(p) == nil {
		return
	}
	index := (p >> g1RegionShift) & (g1RegionCount - 1)
	g1GlobalRootRegions[index/8] |= 1 << (index & 7)
}

// g1gcSetEvacIndexActive publishes the evacuation-index state both to the
// runtime and to the compiler-generated write-barrier fast path. The latter
// reads the field in writeBarrier at offset four, after the enabled byte and
// its padding.
func g1gcSetEvacIndexActive(value uint32) {
	atomic.Store(&g1EvacIndexActive, value)
	atomic.Store(&writeBarrier.g1Evac, value)
}

// g1gcResetWBSlots invalidates metadata left in per-P buffers by an earlier
// evacuation cycle. Buffers may still contain ordinary marking entries when
// a new cycle publishes the active flag, so stale owners must not be reused.
func g1gcResetWBSlots() {
	for _, pp := range allp {
		if pp != nil {
			clear(pp.wbBuf.slots[:])
			if uint32(pp.id) >= uint32(len(g1EdgeTables)) {
				continue
			}
			for i := range g1EdgeTables[pp.id] {
				g1EdgeTables[pp.id][i] = g1EdgeEntry{}
			}
		}
	}
}

func g1gcCycleActive(epoch uint64) bool {
	if debug.g1trace != 0 || debug.g1gcset != 0 && epoch%g1CollectionSetPeriod == 0 {
		return true
	}
	if debug.g1evac == 0 {
		return false
	}
	allocNow := gcController.totalAlloc.Load()
	return allocNow >= g1EvacLastAlloc && allocNow-g1EvacLastAlloc >= g1gcEvacThreshold()
}

// g1gcStartCycle publishes which G1 consumers need region state for this
// cycle. The world is stopped at this point, so the flags do not need a second
// synchronization protocol with the mark workers.
func g1gcStartCycle() {
	if debug.g1gc == 0 {
		return
	}
	g1gcCycleStarted = 1
	// gcStart has already advanced work.cycles immediately before this hook.
	// Reuse that authoritative cycle number instead of maintaining a second
	// locked counter for the optional G1 metadata.
	epoch := uint64(work.cycles.Load())
	traceActive := debug.g1trace != 0
	setActive := debug.g1gcset != 0 && epoch%g1CollectionSetPeriod == 0
	evacActive := false
	g1gcSetEvacIndexActive(0)
	if debug.g1evac != 0 {
		// A suspended collector stays off until the retained heap grows by
		// the re-arm fraction: growth is the cheap proxy for new
		// fragmentation worth rescanning for.
		armed := !g1EvacSuspended
		if g1EvacSuspended {
			live := gcController.heapLive.Load()
			armed = live*g1EvacuationRearmDen >= g1EvacSuspendHeapLive*g1EvacuationRearmNum
			if armed {
				g1EvacSuspended = false
			}
		}
		allocNow := gcController.totalAlloc.Load()
		// High-allocation-rate heaps cross any byte threshold constantly;
		// require quiet time in cycles too so window costs stay amortized.
		if armed && allocNow >= g1EvacLastAlloc && allocNow-g1EvacLastAlloc >= g1gcEvacThreshold() && epoch-g1EvacLastWindowEpoch >= g1EvacMinCycleGap {
			g1gcResetInbound()
			g1gcResetWBSlots()
			clear(g1GlobalRootRegions[:])
			clear(g1WindowAllocRegions[:])
			g1WindowAllocCount = 0
			// Re-base the incremental live totals for this window's marking
			// cycle so its mark termination can skip the mark-bit census.
			g1gcResetLiveCounts()
			g1EvacLastWindowEpoch = epoch
			evacActive = true
			g1gcSetEvacIndexActive(1)
		}
	}
	g1gcObjectStatsActive = uint32(bool2int(traceActive))
	g1gcUsedStatsActive = uint32(bool2int(traceActive || setActive || evacActive))
}

// Object counts are diagnostic-only. Collection-set selection uses bytes, so
// keep the extra object counters out of the default g1gc=1 hot path and enable
// them when g1trace is requested.
var g1gcObjectStatsActive uint32

// Used-region accounting has the same consumers as live-region accounting.
// Keep plain g1gc=1 as the lifecycle switch without charging allocation and
// sweep paths for a ledger that no policy or diagnostic reads.
var g1gcUsedStatsActive uint32

// g1EvacIndexActive is published at cycle start. Inbound edges are only
// recorded while the flag is set, and the evacuation rewrite consumes them
// at mark termination. It is written only while the world is stopped and
// read during concurrent marking, so the STW barriers make plain reads
// correct (same protocol as gcphase).
var g1EvacIndexActive uint32

// g1gcCycleStarted is set only while the world is stopped. It records that
// the current GC passed g1gcCycleActive at start; the allocation counter may
// cross the evacuation threshold while the world is running, so recomputing
// eligibility at cycle end would otherwise pair an end hook with no start.
var g1gcCycleStarted uint32

// g1gcResetInbound clears only target regions touched by the previous mark.
// This keeps the cycle-start cost proportional to the live pointer graph
// rather than to the 65,536-entry logical region table.
func g1gcResetInbound() {
	for i, count := uint64(0), g1InboundTouchedCount.Load(); i < count; i++ {
		region := &g1InboundRegions[g1InboundTouched[i]]
		region.head.Store(0)
		region.listed.Store(0)
	}
	g1InboundTouchedCount.Store(0)
	g1InboundSpanCount.Store(0)
	g1InboundOverflow.Store(0)
	g1EvacRecordNs.Store(0)
}

// g1gcAppendInbound publishes one heap owner span for a pointer into a target
// region. It is called by the marker and by write-barrier flushing while the
// world is running; the edge list is consumed only after the world stops.
//
//go:nosplit
func g1gcAppendInbound(owner *mspan, targetRegion uintptr) {
	if owner == nil || debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive == 0 {
		return
	}
	g1gcAppendInboundActive(owner, targetRegion)
}

// g1gcAppendInboundActive is the per-edge body after the caller has
// established that the current mark cycle is evacuation-eligible. Edges are
// deduplicated per P in a small open-addressing table and flushed into the
// global inbound tables at the evacuation STW, so the hot path performs no
// global atomics.
//
//go:nosplit
func g1gcAppendInboundActive(owner *mspan, targetRegion uintptr) {
	pp := getg().m.p.ptr()
	if uint32(pp.id) >= uint32(len(g1EdgeTables)) {
		g1InboundOverflow.Store(1)
		return
	}
	if !g1RegionSticky(targetRegion) {
		return
	}
	h := uintptr(unsafe.Pointer(owner))>>4 ^ targetRegion
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h &= g1EdgeBufSize - 1
	for i := uintptr(0); i < g1EdgeBufSize; i++ {
		e := &g1EdgeTables[pp.id][(h+i)&(g1EdgeBufSize-1)]
		if e.owner == uintptr(unsafe.Pointer(owner)) && e.region == targetRegion {
			return
		}
		if e.owner == 0 {
			e.owner = uintptr(unsafe.Pointer(owner))
			e.region = targetRegion
			return
		}
	}
	g1InboundOverflow.Store(1)
}

// g1gcFlushInboundEdges merges the per-P dedup tables into the global inbound
// tables. The world is stopped, so the global appends use the same lock-free
// region chains as the original per-edge path without concurrent competitors.
func g1gcFlushInboundEdges() {
	for _, pp := range allp {
		if pp == nil || uint32(pp.id) >= uint32(len(g1EdgeTables)) {
			continue
		}
		for i := range g1EdgeTables[pp.id] {
			e := &g1EdgeTables[pp.id][i]
			if e.owner == 0 {
				continue
			}
			owner := (*mspan)(unsafe.Pointer(e.owner))
			targetRegion := e.region
			region := &g1InboundRegions[targetRegion&(g1RegionCount-1)]
			if region.listed.CompareAndSwap(0, 1) {
				position := g1InboundTouchedCount.Add(1) - 1
				if position >= g1RegionCount {
					throw("runtime: G1 inbound-region index overflow")
				}
				g1InboundTouched[position] = targetRegion & (g1RegionCount - 1)
			}
			position := g1InboundSpanCount.Add(1) - 1
			if position >= g1InboundSpanLimit {
				g1InboundOverflow.Store(1)
				return
			}
			edge := &g1InboundSpanEdges[position]
			edge.owner = owner
			for {
				head := region.head.Load()
				edge.next = head
				if region.head.CompareAndSwap(head, uint32(position+1)) {
					break
				}
			}
		}
	}
}

// g1gcRecordInbound records a heap-to-heap edge without doing another object
// lookup when the caller already has the target span.
//
//go:nosplit
func g1gcRecordInbound(gcw *gcWork, owner, target *mspan, pointer uintptr) {
	if owner == nil || target == nil || debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive == 0 {
		return
	}
	g1gcRecordInboundActive(gcw, owner, target, pointer)
}

// g1gcRecordInboundActive is the per-edge body used by the marker after its
// batch-level active-cycle check has succeeded.
//
//go:nosplit
func g1gcRecordInboundActive(gcw *gcWork, owner, target *mspan, pointer uintptr) {
	if owner == nil || target == nil {
		return
	}
	_ = gcw
	g1gcAppendInboundActive(owner, (pointer>>g1RegionShift)&(g1RegionCount-1))
}

// g1gcRecordInboundSlot records a current heap slot value observed while
// flushing a compiler write barrier. The slot itself is the owner; the
// current value is the target. Global and stack slots are rewritten through
// the normal root path and do not need this index.
//
//go:nosplit
func g1gcRecordInboundSlot(slot, pointer uintptr) {
	if debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive == 0 || slot == 0 || pointer == 0 {
		return
	}
	g1gcRecordInboundSlotActive(&getg().m.p.ptr().gcw, slot, pointer)
}

// g1gcRecordInboundSlotActive is the slot lookup after the write-barrier
// flush has established that this batch belongs to an evacuation-eligible
// cycle.
//
//go:nosplit
func g1gcRecordInboundSlotActive(gcw *gcWork, slot, pointer uintptr) {
	owner := spanOfHeap(slot)
	target := spanOfHeap(pointer)
	if owner == nil || target == nil {
		return
	}
	g1gcRecordInboundActive(gcw, owner, target, pointer)
}

// g1gcRegisterUsedRegion records a region in the append-only used-region
// index. The index is bounded by the logical region table, so a successful
// compare-and-swap can publish exactly one entry for each region.
//
//go:nosplit
func g1gcRegisterUsedRegion(region *g1RegionStats, index uintptr) {
	if region.usedListed.CompareAndSwap(0, 1) {
		position := g1UsedCount.Add(1) - 1
		if position >= g1RegionCount {
			throw("runtime: G1 used-region index overflow")
		}
		g1UsedRegions[position] = index
		region.usedPos = uint32(position)
	}
}

// g1gcRecordAllocBatch flags the region behind an mcache allocation batch
// (refill, releaseAll, or a large allocation). Totals are not maintained
// arithmetically here: every consumer reads them after mark termination,
// where the authoritative per-region recount has long since absorbed the
// change. Only the dirty flag matters.
//
//go:nosplit
func g1gcRecordAllocBatch(s *mspan, objects uint64) {
	if debug.g1gc == 0 || objects == 0 {
		return
	}
	g1gcNoteWindowAllocRegion(s)
	g1gcMarkRegionDirty(g1gcRegionIndex(s.base()))
}

// g1gcRecordSweepFreed flags the region behind objects freed by sweeping.
// It fires beside the totalFree accounting and covers preserved and
// fully-dead spans uniformly.
//
//go:nosplit
func g1gcRecordSweepFreed(s *mspan, objects uint64) {
	if debug.g1gc == 0 || objects == 0 {
		return
	}
	g1gcMarkRegionDirty(g1gcRegionIndex(s.base()))
}

// g1gcRecordSpanAllocation accounts for a direct heap-span allocation that
// does not pass through an mcache, such as a user-arena chunk.
//
//go:nosplit
func g1gcRecordSpanAllocation(span *mspan, objects uint64) {
	if g1gcUsedStatsActive == 0 || g1UsedInitialized.Load() == 0 || (objects == 0 && span.g1evacLiveObjects == 0) {
		return
	}
	index := g1gcRegionIndex(span.base())
	region := &g1Regions[index]
	delta := objects * uint64(span.elemsize)
	g1gcRegisterUsedRegion(region, index)
	region.usedBytes.Add(int64(delta))
	if g1gcObjectStatsActive != 0 {
		region.usedObjects.Add(int64(objects))
	}
	g1gcMarkRegionDirty(index)
}

// g1gcMarkRegionDirty records that a region's span set changed and must be
// recounted at the next mark termination. The flag also lists the region in
// g1DirtyList exactly once per refresh epoch.
//
//go:nosplit
func g1gcMarkRegionDirty(index uintptr) {
	region := &g1Regions[index]
	if region.dirty.CompareAndSwap(0, 1) {
		position := g1DirtyCount.Add(1) - 1
		if position >= g1RegionCount {
			throw("runtime: G1 dirty region index overflow")
		}
		g1DirtyList[position] = index
	}
}

// g1gcForEachWindowSpan visits every in-use span based in the one-megabyte
// address window at wbase. Span boundaries come from the spans themselves (a
// page whose spanOfHeap base equals the page address starts a span), so
// packed adjacent spans are told apart without any separately maintained
// registry. The bitmap is scanned a machine word at a time, skipping free
// stretches wholesale. A 1 MiB window never crosses a 4 MiB chunk boundary,
// so one radix lookup serves the whole walk. The callback returns false to
// stop early.
func g1gcForEachWindowSpan(wbase uintptr, fn func(s *mspan) bool) {
	chunk := mheap_.pages.tryChunkOf(chunkIndex(wbase))
	if chunk == nil {
		return
	}
	// Window offsets inside a chunk are multiples of 1 MiB, so the window's
	// first page is always word-aligned in the bitmap.
	w0 := uintptr(chunkPageIndex(wbase)) / 64
	bits := &chunk.pallocBits
	for k := uintptr(0); k < g1RegionPages/64; k++ {
		word := uint64(bits[w0+k])
		for word != 0 {
			i := k*64 + uintptr(sys.TrailingZeros64(word))
			word &= word - 1
			s := spanOfHeap(wbase + i*pageSize)
			if s == nil || s.base() != wbase+i*pageSize || s.allocCount == 0 || s.elemsize == 0 {
				continue
			}
			if !fn(s) {
				return
			}
		}
	}
}

// g1gcForEachHeapChunk calls fn with the base address of every chunk in the
// page allocator's mapped ranges. Iterating the inUse ranges is the only
// supported way to walk the bitmap: the chunk radix covers address-space
// gaps that must not be visited.
func g1gcForEachHeapChunk(fn func(cbase uintptr) bool) {
	for _, r := range mheap_.pages.inUse.ranges {
		limit := r.limit.addr()
		for cbase := r.base.addr() &^ (pallocChunkBytes - 1); cbase < limit; cbase += pallocChunkBytes {
			if !fn(cbase) {
				return
			}
		}
	}
}

// g1gcSweepHeapSpans visits every in-use heap span whose logical region is
// marked with tag, calling fn with the span and its hashed region index.
//
// The logical region index hashes the true address modulo the region table,
// so spans cannot be enumerated per region directly; instead this walks the
// page allocator's mapped chunks and dispatches windows whose hash is
// marked. It must run while the world is stopped: callers rely on the chunk
// radix and the tagged region set being stable, and fn must not allocate
// spans.
func g1gcSweepHeapSpans(tag uint32, fn func(s *mspan, index uintptr) bool) {
	g1gcForEachHeapChunk(func(cbase uintptr) bool {
		chunk := mheap_.pages.tryChunkOf(chunkIndex(cbase))
		if chunk == nil {
			return true
		}
		for off := uintptr(0); off < pallocChunkBytes; off += 1 << g1RegionShift {
			wbase := cbase + off
			index := g1gcRegionIndex(wbase)
			if g1Regions[index].scanTag != tag {
				continue
			}
			stop := false
			g1gcForEachWindowSpan(wbase, func(s *mspan) bool {
				if !fn(s, index) {
					stop = true
					return false
				}
				return true
			})
			if stop {
				return false
			}
		}
		return true
	})
}

// g1LiveGenMark identifies the current accumulation window. A region whose
// liveGen differs was not touched since the last re-base, so its counter may
// hold stale bytes from an earlier cycle and the first mark overwrites
// instead of accumulating.
var g1LiveGenMark uint64

// g1gcResetLiveCounts prepares the incremental live totals for one marking
// cycle. It runs at evacuation-window cycle start while the world is stopped,
// so the counters never straddle cycles.
func g1gcResetLiveCounts() {
	g1LiveGenMark++
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		region := &g1Regions[g1UsedRegions[i]]
		region.liveBytes.Store(0)
		region.liveObjects.Store(0)
		region.liveGen.Store(g1LiveGenMark)
	}
}

// g1gcRecordLiveObject charges one first-marked object to its region's live
// total during an evacuation-window marking cycle. This replaces the
// stop-the-world mark-bit census for those cycles; every other cycle skips
// the hook entirely. Two racers that both see a stale liveGen overwrite each
// other with single-object sizes, which undercounts rather than overcounts
// and only makes selection more conservative.
//
//go:nosplit
func g1gcRecordLiveObject(span *mspan) {
	if debug.g1evac == 0 || g1EvacIndexActive == 0 {
		return
	}
	region := &g1Regions[g1gcRegionIndex(span.base())]
	mark := g1LiveGenMark
	if region.liveGen.Load() != mark {
		region.liveBytes.Store(uint64(span.elemsize))
		region.liveGen.Store(mark)
		if g1gcObjectStatsActive != 0 {
			region.liveObjects.Store(1)
		}
		return
	}
	region.liveBytes.Add(int64(span.elemsize))
	if g1gcObjectStatsActive != 0 {
		region.liveObjects.Add(1)
	}
}

// g1gcInitializeUsed brings region accounting up to date at mark
// termination. The mcache batching points and the sweeper keep
// region.usedBytes charged and flag touched regions continuously, so this
// pass walks only those regions and recounts them authoritatively from the
// page allocator's bitmap, which also absorbs any accounting drift by
// construction.
func g1gcInitializeUsed() {
	initStart := nanotime()
	// During an evacuation window the incremental totals were re-based at
	// cycle start and hold this cycle's exact live bytes, so the mark-bit
	// census can be skipped entirely for clean as well as dirty regions.
	countLive := debug.g1evac == 0 || g1EvacIndexActive == 0
	if g1UsedInitialized.Load() == 0 {
		g1gcBootstrapUsed(countLive)
	} else {
		g1gcRefreshDirtyRegions(countLive)
	}
	epoch := uint64(work.cycles.Load())
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		index := g1UsedRegions[i]
		g1Regions[index].generation.Store(epoch << 1)
	}
	g1UsedInitialized.Store(1)
	if debug.g1evac >= 4 {
		g1gcValidateIncremental()
	}
	if g1EvacIndexActive != 0 {
		g1LastEvacInitNs = nanotime() - initStart
		count := uint64(0)
		for i := uint64(0); i < g1UsedCount.Load(); i++ {
			index := g1UsedRegions[i]
			region := &g1Regions[index]
			used := region.usedBytes.Load()
			live := region.liveBytes.Load()
			if used == 0 || live*2 >= used {
				continue
			}
			g1LowLiveRegions[count] = index
			count++
		}
		g1LowLiveCount = count
	}
}

// g1gcPublishStickyRegions republishes the candidate snapshot for the next
// evacuation window. It must run AFTER g1gcEvacuate consumed the previous
// snapshot: selection inside a window may only pick regions whose inbound
// edges this window's marker actually recorded, so the recorder and the
// selector have to observe the same bitmap for the whole window. Republishing
// before selection (as initializeUsed once did) let freshly low-live regions
// slip into the collection set with no indexed edges, which surfaced as
// bounded rewrite misses on workloads whose live set shifts between windows.
func g1gcPublishStickyRegions() {
	clear(g1StickyRegions[:])
	for i := uint64(0); i < g1LowLiveCount; i++ {
		index := g1LowLiveRegions[i]
		if g1GlobalRootRegions[index/8]>>(index&7)&1 != 0 {
			continue
		}
		g1StickyRegions[index/64] |= 1 << (index % 64)
	}
}

// g1gcRefreshDirtyRegions recounts every region flagged by the allocation
// and sweep hooks. The heap sweep accumulates into the refresh buffers so a
// logical region fed by several address windows (the index hashes addresses
// modulo the table) totals exactly once; the reconcile pass then publishes
// the authoritative numbers.
func g1gcRefreshDirtyRegions(countLive bool) {
	g1gcForEachHeapChunk(func(cbase uintptr) bool {
		chunk := mheap_.pages.tryChunkOf(chunkIndex(cbase))
		if chunk == nil {
			return true
		}
		for off := uintptr(0); off < pallocChunkBytes; off += 1 << g1RegionShift {
			wbase := cbase + off
			index := g1gcRegionIndex(wbase)
			if g1Regions[index].dirty.Load() == 0 {
				continue
			}
			g1gcForEachWindowSpan(wbase, func(s *mspan) bool {
				bytes := uint64(s.allocCount) * uint64(s.elemsize)
				g1RefreshUsed[index] += bytes
				if countLive || g1gcObjectStatsActive != 0 {
					liveObjs, liveBytes := g1gcCountLive(s)
					if countLive {
						g1RefreshLive[index] += liveBytes
					}
					if g1gcObjectStatsActive != 0 {
						g1RefreshObjects[index] += uint64(s.allocCount)
						if countLive {
							g1RefreshLiveObjects[index] += liveObjs
						}
					}
				}
				return true
			})
		}
		return true
	})
	for i, count := uint64(0), g1DirtyCount.Load(); i < count; i++ {
		index := g1DirtyList[i]
		region := &g1Regions[index]
		region.dirty.Store(0)
		usedBytes := g1RefreshUsed[index]
		liveBytes := g1RefreshLive[index]
		usedObjects := g1RefreshObjects[index]
		liveObjects := g1RefreshLiveObjects[index]
		g1RefreshUsed[index] = 0
		g1RefreshLive[index] = 0
		g1RefreshObjects[index] = 0
		g1RefreshLiveObjects[index] = 0
		region.usedBytes.Store(usedBytes)
		if g1gcObjectStatsActive != 0 {
			region.usedObjects.Store(usedObjects)
		}
		listed := region.usedListed.Load() != 0
		if usedBytes != 0 {
			if !listed {
				position := g1UsedCount.Load()
				if position >= g1RegionCount {
					throw("runtime: G1 used-region index overflow")
				}
				region.usedListed.Store(1)
				g1UsedRegions[position] = index
				region.usedPos = uint32(position)
				g1UsedCount.Store(position + 1)
			}
		} else if listed {
			// Every allocated page in the region died since the last
			// refresh.
			last := g1UsedCount.Load() - 1
			if last == ^uint64(0) {
				throw("runtime: G1 used-region index underflow")
			}
			pos := uintptr(region.usedPos)
			if pos > uintptr(last) {
				throw("runtime: G1 used-region position corrupt")
			}
			moved := g1UsedRegions[last]
			g1UsedRegions[pos] = moved
			g1Regions[moved].usedPos = uint32(pos)
			g1UsedCount.Store(last)
			region.usedListed.Store(0)
		}
		if countLive {
			region.liveBytes.Store(liveBytes)
			if g1gcObjectStatsActive != 0 {
				region.liveObjects.Store(liveObjects)
			}
		}
	}
	g1DirtyCount.Store(0)
}

// g1gcBootstrapUsed builds the initial region snapshot with one full allspans
// walk. This runs once, at the first accounting-enabled mark termination;
// spans created before the GODEBUG vars were parsed are invisible to the
// dirty-tracking hooks, so the bootstrap closes that gap. Afterwards the
// incremental maintenance covers everything.
func g1gcBootstrapUsed(countLive bool) {
	epoch := uint64(work.cycles.Load())
	for _, span := range mheap_.allspans {
		if span == nil || span.state.get() != mSpanInUse || span.allocCount == 0 || span.elemsize == 0 {
			continue
		}
		index := g1gcRegionIndex(span.base())
		region := &g1Regions[index]
		if region.usedListed.Load() == 0 {
			position := g1UsedCount.Load()
			if position >= g1RegionCount {
				throw("runtime: G1 used-region index overflow")
			}
			region.usedListed.Store(1)
			g1UsedRegions[position] = index
			region.usedPos = uint32(position)
			g1UsedCount.Store(position + 1)
		}
		region.generation.Store(epoch << 1)
		region.usedBytes.Store(region.usedBytes.Load() + uint64(span.allocCount)*uint64(span.elemsize))
		if g1gcObjectStatsActive != 0 {
			region.usedObjects.Store(region.usedObjects.Load() + uint64(span.allocCount))
		}
		if countLive {
			liveObjects, liveBytes := g1gcCountLive(span)
			if liveBytes != 0 {
				region.liveBytes.Store(region.liveBytes.Load() + liveBytes)
				if g1gcObjectStatsActive != 0 {
					region.liveObjects.Store(region.liveObjects.Load() + liveObjects)
				}
			}
		}
	}
	// The snapshot is authoritative for every listed region; drop whatever
	// dirt accumulated during boot so the next pass is purely incremental.
	for i, count := uint64(0), g1DirtyCount.Load(); i < count; i++ {
		g1Regions[g1DirtyList[i]].dirty.Store(0)
	}
	g1DirtyCount.Store(0)
}

// g1gcValidateIncremental recomputes every region's used bytes straight from
// allspans and compares against the incrementally maintained totals. It runs
// only under GODEBUG=g1evac=4 while the world is stopped. Regions hosting
// user-arena chunks can legitimately hold more than the recount because
// arena allocations do not move allocCount; the reverse direction is always
// a real bug.
func g1gcValidateIncremental() {
	clear(g1ValidateScratch[:])
	for _, span := range mheap_.allspans {
		if span == nil || span.state.get() != mSpanInUse || span.allocCount == 0 || span.elemsize == 0 {
			continue
		}
		index := g1gcRegionIndex(span.base())
		g1ValidateScratch[index] += uint64(span.allocCount) * uint64(span.elemsize)
	}
	drift := false
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		index := g1UsedRegions[i]
		auth := g1ValidateScratch[index]
		got := g1Regions[index].usedBytes.Load()
		if auth > got {
			print("runtime: G1 region ", index, " used-bytes undercount auth=", auth, " got=", got, "\n")
			drift = true
			if debug.g1evac >= 5 {
				print("g1dump allspans:\n")
				for _, span := range mheap_.allspans {
					if span != nil && span.state.get() == mSpanInUse && span.allocCount != 0 && span.elemsize != 0 &&
						g1gcRegionIndex(span.base()) == index {
						b := span.base()
						ci := chunkIndex(b)
						ch := mheap_.pages.tryChunkOf(ci)
						bit := uint64(0)
						if ch != nil {
							page := chunkPageIndex(b)
							bit = uint64(ch.pallocBits[page/64]) >> (page % 64) & 1
						}
						print("  A base=", hex(b), " npages=", span.npages, " ac=", span.allocCount, " es=", span.elemsize,
							" ci=", uint64(ci), " chunknil=", ch == nil, " bit=", bit, " limitok=", b < span.limit, "\n")
					}
				}
				print("g1dump windowscan:\n")
				pages := &mheap_.pages
				for _, r := range pages.inUse.ranges {
					limit := r.limit.addr()
					for cbase := r.base.addr() &^ (pallocChunkBytes - 1); cbase < limit; cbase += pallocChunkBytes {
						for off := uintptr(0); off < pallocChunkBytes; off += 1 << g1RegionShift {
							wbase := cbase + off
							if g1gcRegionIndex(wbase) != index {
								continue
							}
							g1gcForEachWindowSpan(wbase, func(s *mspan) bool {
								print("  S base=", hex(s.base()), "\n")
								return true
							})
						}
					}
				}
			}
		}
		g1ValidateScratch[index] = 0
	}
	if drift {
		throw("runtime: G1 incremental used-region accounting drifted")
	}
}

func g1gcSnapshotUsed() {
	var usedBytes, usedObjects, usedRegions uint64
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		region := &g1Regions[g1UsedRegions[i]]
		bytes := region.usedBytes.Load()
		if bytes != 0 {
			usedBytes += bytes
			usedRegions++
			if g1gcObjectStatsActive != 0 {
				usedObjects += region.usedObjects.Load()
			}
		}
	}
	g1LastCycle.usedBytes.Store(usedBytes)
	g1LastCycle.usedObjects.Store(usedObjects)
	g1LastCycle.usedRegions.Store(usedRegions)
}

func g1gcEndCycle() {
	if debug.g1gc == 0 {
		return
	}
	epoch := uint64(work.cycles.Load())
	if debug.g1trace != 0 {
		var liveBytes, liveObjects, liveRegions uint64
		for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
			region := &g1Regions[g1UsedRegions[i]]
			bytes := region.liveBytes.Load()
			objects := region.liveObjects.Load()
			if bytes == 0 {
				continue
			}
			liveBytes += bytes
			liveObjects += objects
			liveRegions++
		}
		g1LastCycle.liveBytes.Store(liveBytes)
		g1LastCycle.liveObjects.Store(liveObjects)
		g1LastCycle.liveRegions.Store(liveRegions)
		g1gcSnapshotUsed()
	}
	if debug.g1gcset != 0 && epoch%g1CollectionSetPeriod == 0 {
		g1gcBuildCollectionSet()
	} else {
		g1CandidateNext.Store(0)
		g1CandidateCount.Store(0)
		g1LastCycle.candidateRegions.Store(0)
		g1LastCycle.candidateSpans.Store(0)
		g1LastCycle.reclaimBytes.Store(0)
	}
}

// g1gcRegionIndex maps a span to the logical region containing its base. A
// large span may cross a logical region boundary, but keeping the span whole
// is required by the existing allocator and keeps the policy metadata exact
// at span granularity.
//
//go:nosplit
func g1gcRegionIndex(base uintptr) uintptr {
	return (base >> g1RegionShift) & (g1RegionCount - 1)
}

// g1gcBuildCollectionSet selects low-live regions from the persistent used
// snapshot and queues their spans. It runs after gcSweep has initialized the
// next sweep generation and before the world is restarted.
func g1gcBuildCollectionSet() {
	// Phase one walks the used-region snapshot for per-region policy data
	// and tags the regions whose spans may join the queue. Phase two sweeps
	// the heap once, filling the bounded queue from tagged regions only.

	var candidateRegions, reclaimBytes uint64
	var candidateCount, candidateBytes uint64
	tag := g1ScanTag + 1
	g1ScanTag = tag
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		index := g1UsedRegions[i]
		region := &g1Regions[index]
		used := region.usedBytes.Load()
		if used == 0 {
			continue
		}
		live := region.liveBytes.Load()
		if live >= used {
			continue
		}
		reclaim := used - live
		candidateRegions++
		reclaimBytes += reclaim
		region.scanTag = tag
	}
	g1gcSweepHeapSpans(tag, func(span *mspan, _ uintptr) bool {
		if candidateCount >= g1CandidateSpanLimit {
			return false
		}
		// gcSweep has already advanced the generation. Only spans
		// still waiting for this sweep are useful to the priority
		// queue; swept or cached spans would otherwise make every
		// proportional-sweep lookup walk stale candidates.
		if atomic.Load(&span.sweepgen) != mheap_.sweepgen-2 {
			return true
		}
		spanBytes := uint64(span.npages) * uint64(pageSize)
		if spanBytes > g1CollectionSetMaxBytes {
			return true
		}
		if candidateBytes+spanBytes > g1CollectionSetMaxBytes {
			return false
		}
		g1CandidateSpans[candidateCount] = span
		candidateCount++
		candidateBytes += spanBytes
		return true
	})
	g1CandidateNext.Store(0)
	g1CandidateCount.Store(candidateCount)

	g1LastCycle.candidateRegions.Store(candidateRegions)
	g1LastCycle.candidateSpans.Store(candidateCount)
	g1LastCycle.reclaimBytes.Store(reclaimBytes)
}

// g1gcNextCandidate returns the next selected span that is still unswept in
// the current generation. It intentionally does not remove the span from its
// central unswept set; the sweep generation CAS in sweepone arbitrates between
// this queue, allocation, and the ordinary sweeper.
func g1gcNextCandidate(sweepgen uint32) *mspan {
	count := g1CandidateCount.Load()
	if count == 0 {
		return nil
	}
	for {
		index := g1CandidateNext.Add(1) - 1
		if index >= count {
			// Once every queued entry has been claimed, leave the
			// central sweep path alone until the next collection. The
			// queue is only a priority hint and the central lists still
			// own the correctness path for entries not claimed here.
			g1CandidateCount.CompareAndSwap(count, 0)
			return nil
		}
		span := g1CandidateSpans[index]
		if span == nil || span.state.get() != mSpanInUse || atomic.Load(&span.sweepgen) != sweepgen-2 {
			continue
		}
		return span
	}
}

func g1gcTrace() {
	if debug.g1gc == 0 {
		return
	}
	print(" g1-regions ", g1LastCycle.liveRegions.Load(),
		" live-bytes ", g1LastCycle.liveBytes.Load(),
		" live-objects ", g1LastCycle.liveObjects.Load(),
		" used-regions ", g1LastCycle.usedRegions.Load(),
		" used-bytes ", g1LastCycle.usedBytes.Load(),
		" cset-regions ", g1LastCycle.candidateRegions.Load(),
		" cset-spans ", g1LastCycle.candidateSpans.Load(),
		" reclaim-bytes ", g1LastCycle.reclaimBytes.Load(),
		" evac-us ", g1LastEvacNanos/1e3,
		" evac-select-us ", g1LastEvacSelectNs/1e3,
		" evac-copy-us ", g1LastEvacCopyNs/1e3,
		" evac-roots-us ", g1LastEvacRootsNs/1e3,
		" evac-rootsmark-us ", g1LastEvacRootsMarkNs/1e3,
		" evac-rootsstack-us ", g1LastEvacRootsStackNs/1e3,
		" evac-init-us ", g1LastEvacInitNs/1e3,
		" evac-final-us ", g1LastEvacFinalNs/1e3,
		" evac-heap-us ", g1LastEvacHeapNs/1e3,
		" evac-spans ", g1LastEvacSpans,
		" evac-objects ", g1LastEvacObjects,
		" evac-bytes ", g1LastEvacBytes,
		" rewrite-spans ", g1LastRewriteSpans,
		" inbound-edges ", g1InboundSpanCount.Load(),
		" inbound-record-us ", g1EvacRecordNs.Load()/1e3,
		" inbound-overflow ", g1InboundOverflow.Load(),
		" dbg-cands ", g1DbgCands.Load(), " dbg-nilcens ", g1DbgNilCens.Load(),
		" dbg-dstnil ", g1DbgDstNil.Load(), " dbg-sellive ", g1DbgSelLive.Load(),
		" dbg-minlive ", g1DbgMinLive.Load(), " dbg-lowlive ", g1DbgLowLive.Load(),
		" dbg-tagged ", g1DbgTagged.Load(),
		" evac-idle ", uint64(g1EvacIdleWindows), " evac-suspended ", bool2int(g1EvacSuspended))
}
