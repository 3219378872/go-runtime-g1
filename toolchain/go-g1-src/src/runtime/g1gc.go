// Copyright 2026 The Go Runtime G1 project.
//
// Experimental region accounting and collection-set scheduling for the real Go
// runtime. The policy is kept separate from object movement until evacuation
// has been proven against the runtime's precise pointer maps.
package runtime

import "internal/runtime/atomic"

const (
	g1RegionShift = 20 // 1 MiB logical regions, matching the initial G1 policy.
	g1RegionCount = 1 << 16

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
	usedListed  atomic.Uint32
	usedBytes   atomic.Uint64
	usedObjects atomic.Uint64
}

// The table is static because runtime code must not allocate metadata from the
// managed heap while a GC cycle is being initialized. The index is a bounded
// address hash; it is exact for the normal process heap range and still safe
// as telemetry if a process maps more than 64 GiB of heap address space.
var g1Regions [g1RegionCount]g1RegionStats

var g1UsedRegions [g1RegionCount]uintptr
var g1UsedCount atomic.Uint64
var g1UsedInitialized atomic.Uint32
var g1RegionSpans [g1RegionCount]*mspan

type g1InboundRegion struct {
	head   atomic.Uint32
	listed atomic.Uint32
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

func g1gcCycleActive(epoch uint64) bool {
	if debug.g1trace != 0 || debug.g1gcset != 0 && epoch%g1CollectionSetPeriod == 0 {
		return true
	}
	if debug.g1evac == 0 {
		return false
	}
	allocNow := gcController.totalAlloc.Load()
	return allocNow >= g1EvacLastAlloc && allocNow-g1EvacLastAlloc >= g1EvacuationMinAllocBytes
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
	g1EvacIndexActive.Store(0)
	if debug.g1evac != 0 {
		allocNow := gcController.totalAlloc.Load()
		if allocNow >= g1EvacLastAlloc && allocNow-g1EvacLastAlloc >= g1EvacuationMinAllocBytes {
			g1gcResetInbound()
			evacActive = true
			g1EvacIndexActive.Store(1)
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
// useful in a cycle that is eligible to evacuate, so most mark cycles can skip
// the per-pointer reverse-index work entirely.
var g1EvacIndexActive atomic.Uint32

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
}

// g1gcAppendInbound publishes one heap owner span for a pointer into a target
// region. It is called by the marker and by write-barrier flushing while the
// world is running; the edge list is consumed only after the world stops.
//
//go:nosplit
func g1gcAppendInbound(owner *mspan, targetRegion uintptr) {
	if owner == nil || debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive.Load() == 0 {
		return
	}
	g1gcAppendInboundActive(owner, targetRegion)
}

// g1gcAppendInboundActive is the append path after the caller has established
// that the current mark cycle is evacuation-eligible. Keeping the configuration
// and active-cycle checks out of the per-edge path matters for dense graphs.
//
//go:nosplit
func g1gcAppendInboundActive(owner *mspan, targetRegion uintptr) {
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
			return
		}
	}
}

// g1gcRecordInbound records a heap-to-heap edge without doing another object
// lookup when the caller already has the target span.
//
//go:nosplit
func g1gcRecordInbound(gcw *gcWork, owner, target *mspan, pointer uintptr) {
	if owner == nil || target == nil || debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive.Load() == 0 {
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
	if debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive.Load() == 0 || slot == 0 || pointer == 0 {
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
	}
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
}

// g1gcLinkSpan adds a fully initialized heap span to its region list. The
// caller must serialize with other list updates; collection-set traversal is
// stop-the-world, while normal registration holds mheap_.lock.
func g1gcLinkSpan(span *mspan) {
	index := g1gcRegionIndex(span.base())
	span.g1region = index
	span.g1next = g1RegionSpans[index]
	g1RegionSpans[index] = span
}

// g1gcInitializeUsed rebuilds region state from authoritative span allocation
// and mark bits at mark termination.
func g1gcInitializeUsed() {
	previousUsedCount := g1UsedCount.Load()
	for i := uint64(0); i < previousUsedCount; i++ {
		index := g1UsedRegions[i]
		region := &g1Regions[index]
		region.usedListed.Store(0)
		region.usedBytes.Store(0)
		region.usedObjects.Store(0)
		region.liveBytes.Store(0)
		region.liveObjects.Store(0)
		g1RegionSpans[index] = nil
	}
	g1UsedCount.Store(0)
	epoch := uint64(work.cycles.Load())
	spanIndexActive := g1EvacIndexActive.Load() != 0 || debug.g1gcset != 0 && epoch%g1CollectionSetPeriod == 0
	for _, span := range mheap_.allspans {
		if span == nil {
			continue
		}
		span.g1next = nil
		span.g1region = g1gcRegionIndex(span.base())
		if span.state.get() != mSpanInUse || span.allocCount == 0 || span.elemsize == 0 {
			continue
		}
		if spanIndexActive {
			g1gcLinkSpan(span)
		}
		index := g1gcRegionIndex(span.base())
		region := &g1Regions[index]
		// The world is stopped, so publish each region and advance the index
		// without the locked RMW operations needed by concurrent allocation.
		if region.usedListed.Load() == 0 {
			position := g1UsedCount.Load()
			if position >= g1RegionCount {
				throw("runtime: G1 used-region index overflow")
			}
			region.usedListed.Store(1)
			g1UsedRegions[position] = index
			g1UsedCount.Store(position + 1)
		}
		region.generation.Store(epoch << 1)
		region.usedBytes.Store(region.usedBytes.Load() + uint64(span.allocCount)*uint64(span.elemsize))
		if g1gcObjectStatsActive != 0 {
			region.usedObjects.Store(region.usedObjects.Load() + uint64(span.allocCount))
		}
		liveObjects, liveBytes := g1gcCountLive(span)
		if liveBytes != 0 {
			region.liveBytes.Store(region.liveBytes.Load() + liveBytes)
			if g1gcObjectStatsActive != 0 {
				region.liveObjects.Store(region.liveObjects.Load() + liveObjects)
			}
		}
	}
	g1UsedInitialized.Store(1)
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
	// g1gcEndCycle has already flushed the P-local allocation batches and
	// captured the used snapshot. Select regions and queue their spans in one
	// walk; a separate selected flag and second region traversal only added
	// stop-the-world metadata traffic.

	var candidateRegions, reclaimBytes uint64
	var candidateCount, candidateBytes uint64
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
		// Region-local span lists fill the selected set. Spans that overflow
		// the bounded queue remain reachable through the regular central sweep
		// path.
		for span := g1RegionSpans[index]; span != nil; span = span.g1next {
			if span.state.get() != mSpanInUse || span.g1region != index || span.allocCount == 0 || span.elemsize == 0 {
				continue
			}
			// gcSweep has already advanced the generation. Only spans
			// still waiting for this sweep are useful to the priority
			// queue; swept or cached spans would otherwise make every
			// proportional-sweep lookup walk stale candidates.
			if atomic.Load(&span.sweepgen) != mheap_.sweepgen-2 {
				continue
			}
			if candidateCount >= g1CandidateSpanLimit {
				break
			}
			spanBytes := uint64(span.npages) * uint64(pageSize)
			if spanBytes > g1CollectionSetMaxBytes {
				continue
			}
			if candidateBytes+spanBytes > g1CollectionSetMaxBytes {
				break
			}
			g1CandidateSpans[candidateCount] = span
			candidateCount++
			candidateBytes += spanBytes
		}
	}
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
		" evac-heap-us ", g1LastEvacHeapNs/1e3,
		" evac-spans ", g1LastEvacSpans,
		" evac-objects ", g1LastEvacObjects,
		" evac-bytes ", g1LastEvacBytes,
		" rewrite-spans ", g1LastRewriteSpans,
		" inbound-edges ", g1InboundSpanCount.Load(),
		" inbound-overflow ", g1InboundOverflow.Load())
}
