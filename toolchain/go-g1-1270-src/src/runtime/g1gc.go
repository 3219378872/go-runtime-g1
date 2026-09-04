// Copyright 2026 The Go Runtime G1 project.
//
// Experimental region accounting and collection-set scheduling for the real Go
// runtime. The policy is kept separate from object movement until evacuation
// has been proven against the runtime's precise pointer maps.
package runtime

import (
	"internal/runtime/atomic"
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

// g1DebugForwardCount counts pointer rewrites performed by
// g1gcForwardPointer. Stop-the-world diagnostic state used by the g1evac=4
// crosscheck to prove that skipped root rescans were safe.
var g1DebugForwardCount int

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

// g1LiveGenMark identifies the current accumulation window. A region whose
// liveGen differs was not touched since the last re-base, so its counter may
// hold stale bytes from an earlier cycle and the first mark overwrites
// instead of accumulating.
var g1LiveGenMark uint64

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
