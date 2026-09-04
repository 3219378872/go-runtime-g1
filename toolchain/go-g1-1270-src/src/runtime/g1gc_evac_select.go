// Copyright 2026 The Go Runtime G1 project.
//
// G1 evacuation selection: thresholds, adaptive arming,
// eligibility, candidate tagging, and the window driver.
package runtime

import (
	"internal/runtime/atomic"
)

// Full-heap rewriting is deliberately throttled. A cycle still computes the
// live-region policy every time, but moving objects only when enough elapsed
// allocation has accumulated keeps the experimental stop-the-world path from
// turning every small GC into a global rewrite. The threshold scales with the
// retained heap so large heaps do not pay a full-heap rewrite every few
// cycles.
func g1gcEvacThreshold() uint64 {
	const minThreshold = 2 << 30
	live := gcController.heapLive.Load()
	scaled := live * 8
	if scaled < minThreshold {
		return minThreshold
	}
	return scaled
}

// g1gcNoteIdleWindow records an activation whose selection produced no
// copies, suspending evacuation after repeated idle windows so dense heaps
// stop paying the recording tax.
func g1gcNoteIdleWindow() {
	g1EvacIdleWindows++
	if g1EvacIdleWindows >= g1EvacuationIdleSuspendAfter && !g1EvacSuspended {
		g1EvacSuspended = true
		g1EvacSuspendHeapLive = gcController.heapLive.Load()
	}
}

// g1gcCommitProductiveWindow consumes the window's allocation credit only
// when objects actually moved, and re-arms a suspended collector outright.
func g1gcCommitProductiveWindow(allocNow uint64) {
	g1EvacLastAlloc = allocNow
	g1EvacIdleWindows = 0
	g1EvacSuspended = false
}
func g1gcCountLive(s *mspan) (count uint64, bytes uint64) {
	// At mark termination, the objects retained by this cycle are marked in
	// the inline mark bits for small spans (Green Tea) or in gcmarkBits
	// otherwise. countAlloc scans the external bitmap a machine word at a
	// time; inline marks are merged into gcmarkBits only during sweep.
	if gcUsesSpanInlineMarkBits(s.elemsize) {
		count = g1gcCountInlineMarks(s)
	} else {
		count = uint64(s.countAlloc())
	}
	return count, count * uint64(s.elemsize)
}
func g1gcEvacEligible(s *mspan, epoch uint64) (uint64, uint64, bool) {
	if s == nil || s.state.get() != mSpanInUse || s.g1evacDest != nil {
		return 0, 0, false
	}
	if s.isUserArenaChunk || s.spanclass == tinySpanClass || s.specials != nil || s.pinnerBits != nil || s.g1evacPinEpoch == epoch {
		return 0, 0, false
	}
	if s.allocCount == 0 || s.elemsize == 0 || s.sweepgen != mheap_.sweepgen {
		return 0, 0, false
	}
	region := &g1Regions[g1gcRegionIndex(s.base())]
	if region.generation.Load() != epoch<<1 {
		return 0, 0, false
	}
	used := region.usedBytes.Load()
	live := region.liveBytes.Load()
	if used == 0 || live*2 >= used {
		return 0, 0, false
	}
	// No per-span density rejection here: marks include allocate-black
	// objects from this cycle, so a freshly written span always reads as
	// dense. The region-level totals above carry the survived-from-before
	// signal; copying some floating garbage is safe and capped by the byte
	// budget.
	liveObjects, liveBytes := g1gcCountLive(s)
	if liveObjects == 0 {
		return 0, 0, false
	}
	return liveObjects, liveBytes, true
}

// g1gcEvacEligibleCheap is the selection-time filter without the per-span
// mark-bit census. The census runs later, on the handful of spans that end
// up copied, so a large low-live set costs scanning proportional to the
// copy budget rather than to the heap.
func g1gcEvacEligibleCheap(s *mspan, epoch uint64) bool {
	if s == nil || s.state.get() != mSpanInUse || s.g1evacDest != nil {
		return false
	}
	if s.isUserArenaChunk || s.spanclass == tinySpanClass || s.specials != nil || s.pinnerBits != nil || s.g1evacPinEpoch == epoch {
		return false
	}
	if s.allocCount == 0 || s.elemsize == 0 || s.sweepgen != mheap_.sweepgen {
		return false
	}
	region := &g1Regions[g1gcRegionIndex(s.base())]
	return region.generation.Load() == epoch<<1 && region.liveBytes.Load()*2 < region.usedBytes.Load()
}
func g1gcTagLowLiveRegions() uint32 {
	tag := g1ScanTag + 1
	g1ScanTag = tag
	tagged := 0
	// Greedy by reclaimable bytes: tag sticky, root-free low-live regions in
	// descending order until their dead bytes cover several copies' worth of
	// budget. Sorting keeps the stop-the-world sweep proportional to the
	// byte budget instead of to the size of the low-live set.
	type entry struct {
		index   uintptr
		reclaim uint64
	}
	var list [512]entry
	n := 0
	for i := uint64(0); i < g1LowLiveCount && n < len(list); i++ {
		index := g1LowLiveRegions[i]
		if !g1RegionSticky(index) {
			continue
		}
		if g1GlobalRootRegions[index/8]>>(index&7)&1 != 0 {
			continue
		}
		region := &g1Regions[index]
		used := region.usedBytes.Load()
		live := region.liveBytes.Load()
		if used == 0 || live >= used {
			continue
		}
		list[n] = entry{index, used - live}
		n++
	}
	for i := 1; i < n; i++ {
		e := list[i]
		j := i - 1
		for j >= 0 && list[j].reclaim < e.reclaim {
			list[j+1] = list[j]
			j--
		}
		list[j+1] = e
	}
	const reclaimTarget = uint64(64) << 20
	var covered uint64
	for i := 0; i < n && covered < reclaimTarget; i++ {
		g1Regions[list[i].index].scanTag = tag
		covered += list[i].reclaim
		tagged++
	}
	g1DbgLowLive.Store(int64(g1LowLiveCount))
	g1DbgTagged.Store(int64(tagged))
	return tag
}

// g1gcEvacuate copies a bounded set of low-live spans before the ordinary
// sweep transition. Destination spans are intentionally not put on central
// lists until gcSweep has advanced the generation.
func g1gcEvacuate() {
	assertWorldStopped()
	g1LastEvacNanos = 0
	g1LastEvacSelectNs = 0
	g1LastEvacCopyNs = 0
	g1LastEvacRootsNs = 0
	g1LastEvacRootsMarkNs = 0
	g1LastEvacRootsStackNs = 0
	g1LastEvacHeapNs = 0
	g1LastEvacInitNs = 0
	g1LastEvacFinalNs = 0
	g1LastEvacSpans = 0
	g1LastEvacObjects = 0
	g1LastEvacBytes = 0
	g1LastRewriteSpans = 0
	if debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive == 0 {
		return
	}
	evacStart := nanotime()
	for _, pp := range allp {
		if pp.mcache != nil {
			pp.mcache.releaseAll()
		}
	}
	if mcache0 != nil {
		mcache0.releaseAll()
	}
	// usedBytes is the current retained heap and can stay flat while a
	// workload repeatedly allocates and collects. Throttle on the monotonic
	// allocation counter instead, so small heaps eventually receive real
	// evacuation without making every GC globally rewrite the heap.
	allocNow := gcController.totalAlloc.Load()
	if allocNow < g1EvacLastAlloc || allocNow-g1EvacLastAlloc < g1gcEvacThreshold() {
		return
	}
	// The credit is consumed only when a window actually moves objects (see
	// g1gcCommitProductiveWindow); unproductive attempts leave it intact so
	// accumulating garbage retries on the next cycle instead of waiting for
	// another full threshold of allocation.
	// Edges written after the last mutator-time flush sit in the per-P
	// write-barrier buffers. Record them before any overflow check so the
	// inbound index covers every store of this marking window.
	g1gcDrainPendingWBSlots()
	if g1InboundOverflow.Load() != 0 {
		// Marking overflowed the bounded inbound-edge index, so the targeted
		// rewrite would degrade into an unbounded full-heap walk inside this
		// pause. Defer evacuation instead: sources stay unmoved, the stale
		// edge state is discarded, and the next activation starts clean.
		g1gcResetInbound()
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}
	epoch := uint64(work.cycles.Load())
	g1EvacEpoch = epoch
	g1gcMarkPinnedSpans(epoch)
	allspans := mheap_.allspans
	selectStart := nanotime()
	var usedBytes uint64
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		usedBytes += g1Regions[g1UsedRegions[i]].usedBytes.Load()
	}
	minLiveBytes := usedBytes / g1EvacuationLiveBenefitScale
	if minLiveBytes < g1EvacuationMinLiveBytes {
		minLiveBytes = g1EvacuationMinLiveBytes
	}

	// One sweep collects everything selection needs: the copy candidates
	// with their live counts, the benefit estimate, and the projected
	// rewrite set. The copies themselves allocate destination spans, so they
	// must not run inside the heap walk; the collected list is replayed
	// after the bounds checks below.
	g1gcFlushInboundEdges()
	if g1InboundOverflow.Load() != 0 {
		// Marking overflowed the bounded inbound-edge index, so the targeted
		// rewrite would degrade into an unbounded full-heap walk inside this
		// pause. Defer evacuation: sources stay unmoved and stale edge state
		// is discarded.
		g1gcResetInbound()
		g1LastEvacSelectNs = nanotime() - selectStart
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}
	var selectedLiveBytes uint64
	var selectedReclaimBytes uint64
	var projectedObjects uint64
	budgetRemaining := uint64(g1EvacuationCopyBudget)
	candidateSpans := 0
	tag := g1gcTagLowLiveRegions()
	g1gcSweepHeapSpans(tag, func(s *mspan, _ uintptr) bool {
		if budgetRemaining == 0 || candidateSpans == len(g1EvacCandidates) {
			return false
		}
		if !g1gcEvacEligibleCheap(s, epoch) {
			return true
		}
		spanBytes := uint64(s.npages) * uint64(pageSize)
		if spanBytes > budgetRemaining {
			return true
		}
		budgetRemaining -= spanBytes
		g1gcCollectInboundForSource(s)
		g1EvacCandidates[candidateSpans] = s
		candidateSpans++
		return true
	})
	for c := 0; c < candidateSpans; c++ {
		liveObjects, liveBytes, ok := g1gcEvacEligible(g1EvacCandidates[c], epoch)
		if !ok {
			// The cheap filter accepted it but the exact census disagrees;
			// drop it from the copy list.
			g1EvacCandidates[c] = nil
			g1DbgNilCens.Add(1)
			continue
		}
		selectedLiveBytes += liveBytes
		selectedReclaimBytes += uint64(g1EvacCandidates[c].npages)*uint64(pageSize) - liveBytes
		projectedObjects += liveObjects
		g1EvacCandidateLive[c] = liveObjects
	}
	g1DbgCands.Store(int64(candidateSpans))
	g1DbgSelLive.Store(int64(selectedLiveBytes))
	g1DbgMinLive.Store(int64(minLiveBytes))
	if selectedReclaimBytes < g1EvacuationMinReclaimBytes || selectedLiveBytes < g1EvacuationMinLiveFloor {
		// The viable set cannot justify a window. End it without consuming
		// the allocation credit; the discarded edge state is rebuilt by the
		// next activation.
		g1gcDiscardProjectedSet()
		for c := 0; c < candidateSpans; c++ {
			g1EvacCandidates[c] = nil
		}
		g1gcNoteIdleWindow()
		g1gcResetInbound()
		g1LastEvacSelectNs = nanotime() - selectStart
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}
	if g1LastRewriteSpans > g1EvacuationMaxRewriteSpans || projectedObjects > g1EvacuationMaxRewriteObject {
		// Unwind the projected set without rewriting. The epoch tags make the
		// stale marks invisible to the next evacuation, and no destination
		// spans exist yet, so nothing on the heap refers to moved objects.
		g1gcDiscardProjectedSet()
		for c := 0; c < candidateSpans; c++ {
			g1EvacCandidates[c] = nil
		}
		g1gcNoteIdleWindow()
		g1gcResetInbound()
		g1LastEvacSelectNs = nanotime() - selectStart
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}

	for c := 0; c < candidateSpans; c++ {
		s := g1EvacCandidates[c]
		g1EvacCandidates[c] = nil
		if s == nil {
			continue
		}
		liveObjects := g1EvacCandidateLive[c]
		dst := g1gcAllocEvacDestination(s)
		if dst == nil {
			g1DbgDstNil.Add(1)
			continue
		}
		if debug.g1evac > 1 {
			print("g1evac source base=", hex(s.base()), " span=", s, " elemsize=", s.elemsize, " nelems=", s.nelems, " alloc=", s.allocCount, " live=", liveObjects, " sweepgen=", s.sweepgen, "\n")
		}
		copyStart := nanotime()
		g1gcCopyEvacuatedSpan(s, dst, liveObjects)
		g1LastEvacCopyNs += nanotime() - copyStart
		if dst.elemsize == 256 {
			g1DebugLastDestination = dst
		}
		if debug.g1evac > 1 {
			print("g1evac dest base=", hex(dst.base()), " span=", dst, " elemsize=", dst.elemsize, " nelems=", dst.nelems, " alloc=", dst.allocCount, " freeindex=", dst.freeindex, " sweepgen=", dst.sweepgen, "\n")
		}
		atomic.Store(&dst.sweepgen, mheap_.sweepgen+1)
		s.g1evacDest = dst
		if liveObjects > uint64(^uint16(0)) {
			throw("runtime: G1 evacuation live object count overflow")
		}
		s.g1evacLiveObjects = uint16(liveObjects)
		g1gcMarkEvacuatedRegions(s, epoch)
		dst.g1evacNext = g1EvacDestHead
		g1EvacDestHead = dst
		g1gcRecordSpanAllocation(dst, liveObjects)
		g1LastEvacSpans++
		g1LastEvacObjects += liveObjects
		g1LastEvacBytes += uint64(liveObjects) * uint64(s.elemsize)
	}
	g1LastEvacSelectNs = nanotime() - selectStart
	if g1EvacDestHead == nil {
		// Nothing was copied even though selection passed, so discard the
		// projected rewrite set and treat the window as idle.
		for s := g1RewriteSpanHead; s != nil; {
			next := s.g1rewriteNext
			s.g1rewriteNext = nil
			s = next
		}
		g1RewriteSpanHead = nil
		g1LastRewriteSpans = 0
		g1gcNoteIdleWindow()
		g1gcResetInbound()
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}

	g1gcDebugUserArenaState("before-rewrite")
	// Non-stack roots (globals, finalizer/cleanup queues, specials) are
	// guaranteed not to reference evacuated regions — selection excluded
	// every region they touched during marking — so their rescans stay
	// skipped. Stack slots are NOT excluded: the rescan below rewrites any
	// that point into moved objects, at the measured cost of a plain
	// stop-the-world scanstack pass. Under g1evac=4 the skipped global
	// rescan runs anyway and throws if it would have forwarded anything.
	g1gcRewriteActive = 1
	stackStart := nanotime()
	g1gcRescanStacks()
	g1LastEvacRootsStackNs = nanotime() - stackStart
	heapStart := nanotime()
	// Rewrite the bounded owner set that was built and committed before the
	// copies, then the newly allocated destination spans. Destination objects
	// can themselves point at another evacuated source span, so omitting the
	// second pass leaves stale pointers.
	for s := g1RewriteSpanHead; s != nil; {
		next := s.g1rewriteNext
		g1gcRewriteSpan(s)
		s.g1rewriteNext = nil
		s = next
	}
	if g1WindowAllocCount > 0 {
		// Objects allocated during this window cycle are black and were
		// never scanned, and bulk stores into their fresh memory leave no
		// barrier entries, so their inbound edges are absent from the
		// index. Sweep the regions that received window-cycle allocations
		// and rewrite their marked objects directly.
		tag := g1ScanTag + 1
		g1ScanTag = tag
		limit := g1WindowAllocCount
		if limit > uint64(len(g1WindowAllocList)) {
			limit = uint64(len(g1WindowAllocList))
		}
		for i := uint64(0); i < limit; i++ {
			g1Regions[g1WindowAllocList[i]].scanTag = tag
		}
		g1gcSweepHeapSpans(tag, func(s *mspan, _ uintptr) bool {
			g1gcRewriteSpan(s)
			return true
		})
	}
	for d := g1EvacDestHead; d != nil; d = d.g1evacNext {
		if d.spanclass.noscan() {
			continue
		}
		abits := d.allocBitsForIndex(0)
		for j := uintptr(0); j < uintptr(d.nelems); j++ {
			if abits.isMarked() {
				g1gcRewriteObject(d, d.base()+j*d.elemsize)
			}
			abits.advance()
		}
	}
	g1RewriteSpanHead = nil
	if debug.g1evac >= 4 {
		// Crosscheck the global exclusion: nothing here may forward. The
		// baseline starts after the stack rescan, whose forwards are the
		// expected mechanism this pass replaces.
		verifyBase := g1DebugForwardCount
		rootStart := nanotime()
		g1gcUpdateRoots()
		if g1DebugForwardCount != verifyBase {
			throw("runtime: G1 global root referenced an evacuated region")
		}
		g1LastEvacRootsNs = nanotime() - rootStart
	}
	g1LastEvacHeapNs = nanotime() - heapStart
	if debug.g1evac >= 4 {
		// The forwarder is still armed, so any additional forward here is
		// a heap slot the bounded rewrite missed.
		g1gcVerifyFullRewrite()
	}
	g1gcRewriteActive = 0
	g1gcDebugUserArenaState("after-rewrite")

	for i := 0; i < len(allspans); i++ {
		s := allspans[i]
		if s == nil || s.g1evacDest == nil {
			continue
		}
		g1gcClearSourceBits(s)
		s.g1evacDest = nil
	}
	g1gcCommitProductiveWindow(allocNow)
	g1LastEvacNanos = nanotime() - evacStart
	g1gcSetEvacIndexActive(0)
}
