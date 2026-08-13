// Copyright 2026 The Go Runtime G1 project.
//
// Stop-the-world evacuation for the experimental runtime G1 policy. This is
// deliberately conservative: spans carrying runtime special records, pins,
// tiny allocations, or user-arena objects remain in place until their update
// protocols are covered explicitly.
package runtime

import (
	"internal/goarch"
	"internal/runtime/atomic"
	"unsafe"
)

const g1EvacuationMaxBytes = 4 << 20

// Rewriting every pointerful object is proportional to the retained heap, so
// do not evacuate a tiny amount of live data from a large heap. The absolute
// floor still lets small heaps exercise evacuation instead of turning the
// feature into a no-op under the benchmark's initial live set.
const (
	g1EvacuationMinLiveBytes     = 1 << 10
	g1EvacuationLiveBenefitScale = 4096
)

// Full-heap rewriting is deliberately throttled. A cycle still computes the
// live-region policy every time, but moving objects only when enough elapsed
// allocation has accumulated keeps the experimental stop-the-world path from
// turning every small GC into a global rewrite.
const g1EvacuationMinAllocBytes = 512 << 20

// g1EvacRegionEpoch is a conservative address filter for pointer rewriting.
// It is reset logically by the epoch rather than by clearing the table.
var g1EvacRegionEpoch [g1RegionCount]uint64

// g1EvacEpoch is only read while the world is stopped during pointer rewrite.
var g1EvacEpoch uint64

var g1EvacLastAlloc uint64

// The phase timings are diagnostic data emitted with gctrace. They are plain
// fields because evacuation and trace emission are both stop-the-world.
var (
	g1LastEvacNanos    int64
	g1LastEvacSelectNs int64
	g1LastEvacCopyNs   int64
	g1LastEvacRootsNs  int64
	g1LastEvacHeapNs   int64
	g1LastEvacSpans    uint64
	g1LastEvacObjects  uint64
	g1LastEvacBytes    uint64
	g1LastRewriteSpans uint64
)

// g1gcRewriteActive is only enabled while the world is stopped. The marking
// scanners use it to rewrite precise pointer slots without creating mark work.
var g1gcRewriteActive uint32

// g1EvacDestHead owns destination spans until gcSweep has advanced the sweep
// generation. Keeping the list separate from mheap.allspans is important:
// sweeping may recycle the source mspan metadata before finalization.
var g1EvacDestHead *mspan

// g1RewriteSpanHead is built from the inbound index for the current
// evacuation. It contains only heap spans that may hold pointers into the
// selected source regions.
var g1RewriteSpanHead *mspan

// Temporary diagnostic target for tracing the first post-evacuation mark of a
// free slot. It is removed with the evacuation diagnostics after the cause is
// fixed.
var g1DebugLastDestination *mspan

func g1gcDebugUserArenaState(label string) {
	if debug.g1evac < 2 {
		return
	}
	print("g1evac user arena ", label, " reuse-len=", len(userArenaState.reuse), " reuse-data=")
	if cap(userArenaState.reuse) != 0 {
		print(hex(uintptr(unsafe.Pointer(&userArenaState.reuse[:cap(userArenaState.reuse)][0]))))
	}
	print(" fault-len=", len(userArenaState.fault), " fault-data=")
	if cap(userArenaState.fault) != 0 {
		print(hex(uintptr(unsafe.Pointer(&userArenaState.fault[:cap(userArenaState.fault)][0]))))
	}
	print("\\n")
}

// g1gcForwardPointer preserves an interior-pointer offset while forwarding a
// pointer out of an evacuated span. The source allocation bits remain intact
// until all roots and live objects have been rewritten.
//
//go:nosplit
func g1gcForwardPointer(p uintptr) uintptr {
	if p == 0 || g1gcRewriteActive == 0 {
		return p
	}
	if g1EvacRegionEpoch[(p>>g1RegionShift)&(g1RegionCount-1)] != g1EvacEpoch {
		return p
	}
	base, span, objIndex := findObject(p, 0, 0)
	if base == 0 || span == nil || span.g1evacDest == nil {
		return p
	}
	dest := span.g1evacDest
	if !dest.allocBitsForIndex(objIndex).isMarked() {
		return p
	}
	destBase := dest.base() + objIndex*dest.elemsize
	forwarded := destBase + (p - base)
	if debug.g1evac > 1 {
		print("g1evac forward p=", hex(p), " source=", hex(base), " index=", objIndex, " dest=", hex(forwarded), " destbase=", hex(dest.base()), "\\n")
	}
	return forwarded
}

func g1gcCountLive(s *mspan) (count uint64, bytes uint64) {
	// At mark termination, gcmarkBits contains exactly the objects retained by
	// this cycle. countAlloc scans it a machine word at a time with popcount.
	count = uint64(s.countAlloc())
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
	liveObjects, liveBytes := g1gcCountLive(s)
	spanUsed := uint64(s.allocCount) * uint64(s.elemsize)
	if liveObjects == 0 || liveBytes*2 >= spanUsed {
		return 0, 0, false
	}
	return liveObjects, liveBytes, true
}

// Runtime scheduler objects have ordinary heap type information, but the
// scheduler also keeps uintptr-typed back-pointers and references from TLS or
// assembly state. Moving them would require updating protocols outside the GC
// pointer maps, so pin their complete spans while evacuation is enabled.
func g1gcSpanContainsG(s *mspan) bool {
	contains := func(p unsafe.Pointer) bool {
		if p == nil {
			return false
		}
		addr := uintptr(p)
		return addr >= s.base() && addr < s.limit
	}
	for _, gp := range allGsSnapshot() {
		if gp == nil {
			continue
		}
		if contains(unsafe.Pointer(gp)) {
			return true
		}
	}
	for _, pp := range allp {
		if contains(unsafe.Pointer(pp)) {
			return true
		}
	}
	for mp := allm; mp != nil; mp = mp.alllink {
		if contains(unsafe.Pointer(mp)) || contains(unsafe.Pointer(mp.g0)) ||
			contains(unsafe.Pointer(mp.gsignal)) || contains(unsafe.Pointer(mp.curg)) {
			return true
		}
	}
	for mp := sched.freem; mp != nil; mp = mp.freelink {
		if contains(unsafe.Pointer(mp)) || contains(unsafe.Pointer(mp.g0)) ||
			contains(unsafe.Pointer(mp.gsignal)) || contains(unsafe.Pointer(mp.curg)) {
			return true
		}
	}
	return false
}

func g1gcMarkPinnedSpan(p unsafe.Pointer, epoch uint64) {
	if p == nil {
		return
	}
	if s := spanOfHeap(uintptr(p)); s != nil {
		s.g1evacPinEpoch = epoch
	}
}

// g1gcMarkPinnedSpans builds the scheduler-object exclusion set once per
// evacuation cycle. The old per-candidate scan was quadratic in the number of
// live goroutines and heap spans.
func g1gcMarkPinnedSpans(epoch uint64) {
	for _, gp := range allGsSnapshot() {
		g1gcMarkPinnedSpan(unsafe.Pointer(gp), epoch)
	}
	for _, pp := range allp {
		g1gcMarkPinnedSpan(unsafe.Pointer(pp), epoch)
	}
	for mp := allm; mp != nil; mp = mp.alllink {
		g1gcMarkPinnedSpan(unsafe.Pointer(mp), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.g0), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.gsignal), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.curg), epoch)
	}
	for mp := sched.freem; mp != nil; mp = mp.freelink {
		g1gcMarkPinnedSpan(unsafe.Pointer(mp), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.g0), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.gsignal), epoch)
		g1gcMarkPinnedSpan(unsafe.Pointer(mp.curg), epoch)
	}
}

func g1gcMarkEvacuatedRegions(s *mspan, epoch uint64) {
	start := s.base() >> g1RegionShift
	end := (s.limit - 1) >> g1RegionShift
	for region := start; ; region++ {
		g1EvacRegionEpoch[region&(g1RegionCount-1)] = epoch
		if region == end {
			return
		}
	}
}

func g1gcAllocEvacDestination(src *mspan) *mspan {
	var dst *mspan
	if src.spanclass.sizeclass() == 0 {
		dst = mheap_.alloc(src.npages, src.spanclass)
		if dst == nil {
			return nil
		}
		dst.limit = dst.base() + src.elemsize
		dst.largeType = src.largeType
		dst.initHeapBits()
		return dst
	}
	return mheap_.central[src.spanclass].mcentral.grow()
}

func g1gcCopyEvacuatedSpan(src, dst *mspan, liveObjects uint64) {
	mbits := src.markBitsForIndex(0)
	abits := src.allocBitsForIndex(0)
	dst.allocCount = 0
	for i := uintptr(0); i < uintptr(src.nelems); i++ {
		if (i < uintptr(src.freeindex) || abits.isMarked()) && mbits.isMarked() {
			srcObj := src.base() + i*src.elemsize
			dstObj := dst.base() + i*dst.elemsize
			if debug.g1evac > 1 {
				print("g1evac live index=", i, " src=", hex(srcObj), " dst=", hex(dstObj), "\\n")
			}
			memmove(unsafe.Pointer(dstObj), unsafe.Pointer(srcObj), src.elemsize)
			dst.allocBitsForIndex(i).setMarkedNonAtomic()
			dst.allocCount++
		}
		mbits.advance()
		abits.advance()
	}
	if dst.allocCount != uint16(liveObjects) {
		throw("runtime: G1 evacuation live count changed")
	}
	if !src.spanclass.noscan() && heapBitsInSpan(src.elemsize) {
		srcBits := src.heapBits()
		dstBits := dst.heapBits()
		if len(srcBits) != len(dstBits) {
			throw("runtime: G1 evacuation heap bitmap size changed")
		}
		copy(dstBits, srcBits)
	}
	dst.freeindex = 0
	dst.freeIndexForScan = 0
	dst.allocCache = 0
	dst.refillAllocCache(0)
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
	g1LastEvacHeapNs = 0
	g1LastEvacSpans = 0
	g1LastEvacObjects = 0
	g1LastEvacBytes = 0
	g1LastRewriteSpans = 0
	if debug.g1gc == 0 || debug.g1evac == 0 || g1EvacIndexActive.Load() == 0 {
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
	if allocNow < g1EvacLastAlloc || allocNow-g1EvacLastAlloc < g1EvacuationMinAllocBytes {
		return
	}
	g1EvacLastAlloc = allocNow
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

	// Select twice. The first pass avoids allocating destination spans when
	// the total benefit cannot amortize the full-heap rewrite; the second pass
	// performs the existing copy with the same deterministic eligibility walk.
	remaining := uint64(g1EvacuationMaxBytes)
	var selectedLiveBytes uint64
	for i := uint64(0); i < g1LowLiveCount && remaining != 0; i++ {
		for s := g1RegionSpans[g1LowLiveRegions[i]]; s != nil; s = s.g1next {
			_, liveBytes, ok := g1gcEvacEligible(s, epoch)
			if !ok {
				continue
			}
			spanBytes := uint64(s.npages) * uint64(pageSize)
			if spanBytes > remaining {
				continue
			}
			selectedLiveBytes += liveBytes
			remaining -= spanBytes
		}
	}
	if selectedLiveBytes < minLiveBytes {
		g1LastEvacSelectNs = nanotime() - selectStart
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}

	remaining = uint64(g1EvacuationMaxBytes)
	for i := uint64(0); i < g1LowLiveCount && remaining != 0; i++ {
		for s := g1RegionSpans[g1LowLiveRegions[i]]; s != nil; s = s.g1next {
			liveObjects, _, ok := g1gcEvacEligible(s, epoch)
			if !ok {
				continue
			}
			spanBytes := uint64(s.npages) * uint64(pageSize)
			if spanBytes > remaining {
				continue
			}
			dst := g1gcAllocEvacDestination(s)
			if dst == nil {
				continue
			}
			if debug.g1evac > 1 {
				print("g1evac source base=", hex(s.base()), " span=", s, " elemsize=", s.elemsize, " nelems=", s.nelems, " alloc=", s.allocCount, " live=", liveObjects, " sweepgen=", s.sweepgen, "\\n")
			}
			copyStart := nanotime()
			g1gcCopyEvacuatedSpan(s, dst, liveObjects)
			g1LastEvacCopyNs += nanotime() - copyStart
			if dst.elemsize == 256 {
				g1DebugLastDestination = dst
			}
			if debug.g1evac > 1 {
				print("g1evac dest base=", hex(dst.base()), " span=", dst, " elemsize=", dst.elemsize, " nelems=", dst.nelems, " alloc=", dst.allocCount, " freeindex=", dst.freeindex, " sweepgen=", dst.sweepgen, "\\n")
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
			remaining -= spanBytes
		}
	}
	g1LastEvacSelectNs = nanotime() - selectStart
	if g1EvacDestHead == nil {
		g1LastEvacNanos = nanotime() - evacStart
		g1gcSetEvacIndexActive(0)
		return
	}

	g1gcDebugUserArenaState("before-rewrite")
	g1gcRewriteActive = 1
	rootStart := nanotime()
	g1gcUpdateRoots()
	g1LastEvacRootsNs = nanotime() - rootStart
	heapStart := nanotime()
	// Rewrite both the existing heap owners and the newly allocated
	// destination spans. Destination objects can themselves point at another
	// evacuated source span, so omitting the second pass leaves stale pointers.
	g1gcRewriteHeap(len(allspans))
	g1LastEvacHeapNs = nanotime() - heapStart
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
	g1LastEvacNanos = nanotime() - evacStart
	g1gcSetEvacIndexActive(0)
}

func g1gcDebugBits(label string, s *mspan) {
	if debug.g1evac < 2 {
		return
	}
	print("g1evac bits ", label, " base=", hex(s.base()), " alloc=")
	abits := s.allocBitsForIndex(0)
	for i := uintptr(0); i < uintptr(s.nelems); i++ {
		if abits.isMarked() {
			print(i, ",")
		}
		abits.advance()
	}
	print(" mark=")
	mbits := s.markBitsForIndex(0)
	for i := uintptr(0); i < uintptr(s.nelems); i++ {
		if mbits.isMarked() {
			print(i, ",")
		}
		mbits.advance()
	}
	print("\\n")
}

func g1gcUpdateRoots() {
	gcw := &getg().m.p.ptr().gcw
	for i := uint32(0); i < work.baseStacks; i++ {
		if i == fixedRootFreeGStacks {
			continue
		}
		markroot(gcw, i, false)
	}
	for _, gp := range allGsSnapshot() {
		g1gcRewriteStack(gp, gcw)
	}
}

func g1gcRewriteStack(gp *g, gcw *gcWork) {
	status := readgstatus(gp)
	if status == _Gdead {
		return
	}
	if (status == _Gwaiting || status == _Gsyscall) && gp.waitsince == 0 {
		gp.waitsince = work.tstart
	}

	// Stack scanning has to use the same suspend/resume protocol as the
	// ordinary markroot path. In particular, suspendG handles a goroutine
	// that is still running or already preempted, and scanstack may shrink the
	// stack while adjusting the sudogs that point into it.
	systemstack(func() {
		userG := getg().m.curg
		selfScan := gp == userG && readgstatus(userG) == _Grunning
		if selfScan {
			casGToWaitingForSuspendG(userG, _Grunning, waitReasonGarbageCollectionScan)
		}

		stopped := suspendG(gp)
		if stopped.dead {
			return
		}
		scanstack(gp, gcw)
		resumeG(stopped)

		if selfScan {
			casgstatus(userG, _Gwaiting, _Grunning)
		}
	})
}

func g1gcRewriteObject(s *mspan, obj uintptr) {
	if s.spanclass.noscan() {
		return
	}
	tp := s.typePointersOfUnchecked(obj)
	for {
		var addr uintptr
		if tp, addr = tp.next(obj + s.elemsize); addr == 0 {
			return
		}
		slot := (*uintptr)(unsafe.Pointer(addr))
		*slot = g1gcForwardPointer(*slot)
	}
}

func g1gcMarkRewriteSpan(s *mspan) {
	if s == nil || s.state.get() != mSpanInUse || s.g1evacDest != nil || s.spanclass.noscan() {
		return
	}
	if s.g1rewriteEpoch == g1EvacEpoch {
		return
	}
	s.g1rewriteEpoch = g1EvacEpoch
	s.g1rewriteNext = g1RewriteSpanHead
	g1RewriteSpanHead = s
	g1LastRewriteSpans++
}

func g1gcCollectInboundForSource(s *mspan) {
	start := s.base() >> g1RegionShift
	end := (s.limit - 1) >> g1RegionShift
	for regionIndex := start; ; regionIndex++ {
		region := &g1InboundRegions[regionIndex&(g1RegionCount-1)]
		for edgeIndex := region.head.Load(); edgeIndex != 0; {
			if edgeIndex > g1InboundSpanLimit {
				throw("runtime: G1 inbound edge index corrupt")
			}
			edge := &g1InboundSpanEdges[edgeIndex-1]
			g1gcMarkRewriteSpan(edge.owner)
			edgeIndex = edge.next
		}
		if regionIndex == end {
			return
		}
	}
}

func g1gcRewriteAllHeap(oldSpanCount int) {
	if g1UsedInitialized.Load() != 0 {
		for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
			index := g1UsedRegions[i]
			if g1Regions[index].usedBytes.Load() == 0 {
				continue
			}
			for s := g1RegionSpans[index]; s != nil; s = s.g1next {
				g1gcRewriteSpan(s)
			}
		}
	} else {
		for i := 0; i < oldSpanCount; i++ {
			g1gcRewriteSpan(mheap_.allspans[i])
		}
	}
}

func g1gcRewriteHeap(oldSpanCount int) {
	g1RewriteSpanHead = nil
	if g1InboundOverflow.Load() != 0 {
		g1gcRewriteAllHeap(oldSpanCount)
	} else {
		// Build the owner set from every selected source region, then rewrite
		// only spans which actually contained an inbound pointer. Any missing
		// edge or bounded-index overflow takes the conservative path above.
		for i := 0; i < oldSpanCount; i++ {
			s := mheap_.allspans[i]
			if s != nil && s.g1evacDest != nil {
				g1gcCollectInboundForSource(s)
			}
		}
		for s := g1RewriteSpanHead; s != nil; {
			next := s.g1rewriteNext
			g1gcRewriteSpan(s)
			s.g1rewriteNext = nil
			s = next
		}
	}
	for s := g1EvacDestHead; s != nil; s = s.g1evacNext {
		if s.spanclass.noscan() {
			continue
		}
		abits := s.allocBitsForIndex(0)
		for j := uintptr(0); j < uintptr(s.nelems); j++ {
			if abits.isMarked() {
				g1gcRewriteObject(s, s.base()+j*s.elemsize)
			}
			abits.advance()
		}
	}
	g1RewriteSpanHead = nil
}

func g1gcRewriteSpan(s *mspan) {
	if s == nil || s.state.get() != mSpanInUse || s.g1evacDest != nil || s.spanclass.noscan() {
		return
	}
	mbits := s.markBitsForIndex(0)
	abits := s.allocBitsForIndex(0)
	for j := uintptr(0); j < uintptr(s.nelems); j++ {
		if (j < uintptr(s.freeindex) || abits.isMarked()) && mbits.isMarked() {
			g1gcRewriteObject(s, s.base()+j*s.elemsize)
		}
		mbits.advance()
		abits.advance()
	}
}

func g1gcClearSourceBits(s *mspan) {
	if debug.g1evac > 1 {
		print("g1evac clear source base=", hex(s.base()), " span=", s, " alloc=", s.allocCount, " freeindex=", s.freeindex, "\\n")
	}
	if s.g1evacLiveObjects == 0 || s.g1evacLiveObjects > s.allocCount {
		throw("runtime: G1 source span live count invalid")
	}
	// Keep allocCount consistent with the bits that remain conceptually
	// allocated until sweep. The copied live objects are now represented by
	// the destination span, so only dead source objects belong in totalFree.
	s.allocCount -= s.g1evacLiveObjects
	s.g1evacLiveObjects = 0
	for i := uintptr(0); i < divRoundUp(uintptr(s.nelems), 8); i++ {
		*s.allocBits.bytep(i) = 0
		*s.gcmarkBits.bytep(i) = 0
	}
	s.allocCache = 0
	s.freeindex = 0
	s.freeIndexForScan = 0
}

func g1gcFinalizeEvacuation() {
	assertWorldStopped()
	for s := g1EvacDestHead; s != nil; {
		next := s.g1evacNext
		s.g1evacNext = nil
		atomic.Store(&s.sweepgen, mheap_.sweepgen)
		g1gcDebugBits("finalize", s)
		if s.allocCount == s.nelems {
			mheap_.central[s.spanclass].mcentral.fullSwept(mheap_.sweepgen).push(s)
		} else {
			mheap_.central[s.spanclass].mcentral.partialSwept(mheap_.sweepgen).push(s)
		}
		s = next
	}
	g1EvacDestHead = nil
}

// Keep the architecture import tied to this file's pointer-slot contract.
var _ = goarch.PtrSize
