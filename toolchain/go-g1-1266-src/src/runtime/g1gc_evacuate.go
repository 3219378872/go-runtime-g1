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

// Rewriting every pointerful object is proportional to the retained heap, so
// do not evacuate a tiny amount of live data from a large heap. The absolute
// floor still lets small heaps exercise evacuation instead of turning the
// feature into a no-op under the benchmark's initial live set.
const (
	g1EvacuationMinLiveBytes     = 1 << 10
	g1EvacuationLiveBenefitScale = 4096
)

// The rewrite pause is bounded by the number of owner spans that hold pointers
// into the selected source regions, which a dense pointer graph can push
// toward a full-heap walk regardless of the copy budget. Deferring whole
// evacuations keeps that pause inside this bound instead. The object bound
// additionally caps the destination rescan and the per-span walks, whose cost
// scales with live objects rather than spans.
const (
	g1EvacuationMaxRewriteSpans  = 512
	g1EvacuationMaxRewriteObject = 64 << 10
)

// Evacuation pauses scale with the copied byte budget through the destination
// slot walk, so keep the per-window copy small enough for the pause to stay
// near ordinary mark termination.
const g1EvacuationCopyBudget = 256 << 10

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

// g1EvacRegionEpoch is a conservative address filter for pointer rewriting.
// It is reset logically by the epoch rather than by clearing the table.
var g1EvacRegionEpoch [g1RegionCount]uint64

// g1EvacEpoch is only read while the world is stopped during pointer rewrite.
var g1EvacEpoch uint64

var g1EvacLastAlloc uint64

// Last-window selection diagnostics for g1trace.
var (
	g1DbgCands   atomic.Int64
	g1DbgNilCens atomic.Int64
	g1DbgDstNil  atomic.Int64
	g1DbgSelLive atomic.Int64
	g1DbgMinLive atomic.Int64
	g1DbgLowLive atomic.Int64
	g1DbgTagged  atomic.Int64
)

// g1EvacLastWindowEpoch and g1EvacMinCycleGap rate-limit evacuation windows
// in GC cycles as well as bytes, so allocation-rate-heavy heaps cannot turn
// every collection into a window. Written only while the world is stopped.
var g1EvacLastWindowEpoch uint64

const g1EvacMinCycleGap = 32

// g1gcDrainPendingWBSlots records inbound edges for write-barrier entries
// still buffered at mark termination. Upstream discards these buffers after
// gcMarkDone because marking no longer needs them; an active evacuation index
// still needs their (slot, pointer) pairs, so drain them here instead of
// letting the termination-time reset drop the final window of stores. The
// world is stopped, so plain iteration cannot race a flush.
func g1gcDrainPendingWBSlots() {
	for _, pp := range allp {
		if pp == nil {
			continue
		}
		buf := &pp.wbBuf
		n := (buf.next - uintptr(unsafe.Pointer(&buf.buf[0]))) / unsafe.Sizeof(buf.buf[0])
		if n == 0 {
			continue
		}
		ptrs := buf.buf[:n]
		slots := buf.slots[:n]
		for i, ptr := range ptrs {
			slot := slots[i]
			if slot != 0 {
				g1gcRecordInboundSlotActive(&pp.gcw, slot, ptr)
			} else if ptr != 0 && spanOfHeap(ptr) != nil {
				// A heap pointer without an owner slot is not safe to
				// omit from the rewrite. Force the conservative fallback.
				g1InboundOverflow.Store(1)
			}
		}
		buf.reset()
	}
}

// The phase timings are diagnostic data emitted with gctrace. They are plain
// fields because evacuation and trace emission are both stop-the-world.
var (
	g1LastEvacNanos        int64
	g1LastEvacSelectNs     int64
	g1LastEvacCopyNs       int64
	g1LastEvacRootsNs      int64
	g1LastEvacRootsMarkNs  int64
	g1LastEvacRootsStackNs int64
	g1LastEvacHeapNs       int64
	g1LastEvacInitNs       int64
	g1LastEvacFinalNs      int64
	g1LastEvacSpans        uint64
	g1LastEvacObjects      uint64
	g1LastEvacBytes        uint64
	g1LastRewriteSpans     uint64
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
	g1DebugForwardCount++
	if debug.g1evac > 1 {
		print("g1evac forward p=", hex(p), " source=", hex(base), " index=", objIndex, " dest=", hex(forwarded), " destbase=", hex(dest.base()), "\\n")
	}
	return forwarded
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

// g1EvacuationMaxCandidates bounds the spans one window may copy. The copy
// budget already caps total bytes; each candidate is at least one page.
var g1EvacCandidates [1 << 8]*mspan
var g1EvacCandidateLive [1 << 8]uint64

// g1gcDiscardProjectedSet unwinds a projection made by
// g1gcCollectInboundForSource without rewriting anything. The epoch tags make
// the stale marks invisible to the next evacuation, and no destination spans
// exist yet, so nothing on the heap refers to moved objects.
func g1gcDiscardProjectedSet() {
	for s := g1RewriteSpanHead; s != nil; {
		next := s.g1rewriteNext
		s.g1rewriteNext = nil
		s = next
	}
	g1RewriteSpanHead = nil
	g1LastRewriteSpans = 0
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
	g1EvacLastAlloc = allocNow
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
		projectedObjects += liveObjects
		g1EvacCandidateLive[c] = liveObjects
	}
	g1DbgCands.Store(int64(candidateSpans))
	g1DbgSelLive.Store(int64(selectedLiveBytes))
	g1DbgMinLive.Store(int64(minLiveBytes))
	if selectedLiveBytes < minLiveBytes {
		g1gcDiscardProjectedSet()
		for c := 0; c < candidateSpans; c++ {
			g1EvacCandidates[c] = nil
		}
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
		// Nothing was copied, so discard the projected rewrite set.
		for s := g1RewriteSpanHead; s != nil; {
			next := s.g1rewriteNext
			s.g1rewriteNext = nil
			s = next
		}
		g1RewriteSpanHead = nil
		g1LastRewriteSpans = 0
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

// g1gcHasSpanSpecials reports whether any heap span has specials (finalizers,
// weak handles, or pinner bits). The span-root markroot shards only do work
// when this bitmap is non-empty, so evacuation can skip the whole span-root
// pass when it is empty.
func g1gcHasSpanSpecials() bool {
	for _, ai := range mheap_.markArenas {
		ha := mheap_.arenas[ai.l1()][ai.l2()]
		for i := range ha.pageSpecials {
			if atomic.Load8(&ha.pageSpecials[i]) != 0 {
				return true
			}
		}
	}
	return false
}

// g1gcVerifyFullRewrite rescans every used-region span after the bounded
// rewrite and reports heap slots that still required forwarding. It runs only
// under GODEBUG=g1evac>=4 while the world is stopped and the forwarder is
// still armed; a nonzero count is direct evidence of a missed inbound edge.
func g1gcVerifyFullRewrite() {
	if g1UsedInitialized.Load() == 0 {
		return
	}
	base := g1DebugForwardCount
	tag := g1ScanTag + 1
	g1ScanTag = tag
	for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
		index := g1UsedRegions[i]
		if g1Regions[index].usedBytes.Load() == 0 {
			continue
		}
		g1Regions[index].scanTag = tag
	}
	g1gcSweepHeapSpans(tag, func(s *mspan, _ uintptr) bool {
		g1gcRewriteSpan(s)
		return true
	})
	if n := g1DebugForwardCount - base; n != 0 {
		print("runtime: G1 evacuation rewrite missed ", n, " heap slots\n")
		if debug.g1evac >= 5 {
			tag := g1ScanTag + 1
			g1ScanTag = tag
			for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
				index := g1UsedRegions[i]
				if g1Regions[index].usedBytes.Load() == 0 {
					continue
				}
				g1Regions[index].scanTag = tag
			}
			g1gcSweepHeapSpans(tag, func(s *mspan, _ uintptr) bool {
				idx := g1gcRegionIndex(s.base())
				window := g1WindowAllocRegions[idx/64]>>(idx%64)&1 != 0
				mbits := s.markBitsForIndex(0)
				abits := s.allocBitsForIndex(0)
				for j := uintptr(0); j < uintptr(s.nelems); j++ {
					if (j < uintptr(s.freeindex) || abits.isMarked()) && mbits.isMarked() {
						obj := s.base() + j*s.elemsize
						tp := s.typePointersOfUnchecked(obj)
						for {
							var addr uintptr
							if tp, addr = tp.next(obj + s.elemsize); addr == 0 {
								break
							}
							p := *(*uintptr)(unsafe.Pointer(addr))
							if p == 0 || g1EvacRegionEpoch[(p>>g1RegionShift)&(g1RegionCount-1)] != g1EvacEpoch {
								continue
							}
							b2, sp2, _ := findObject(p, 0, 0)
							if b2 == 0 || sp2 == nil || sp2.g1evacDest == nil {
								continue
							}
							print("  miss owner=", hex(s.base()), " es=", s.elemsize, " objidx=", j,
								" winreg=", window, " tgt=", hex(p), "\n")
						}
					}
					mbits.advance()
					abits.advance()
				}
				return true
			})
		}
	}
}

// g1gcUpdateRoots rescans the non-stack roots: fixed finalizer and cleanup
// queues, data/BSS globals, and span specials. It exists for the g1evac>=4
// crosscheck only — selection excludes every region these roots reference,
// so a production pause skips this pass entirely, and any pointer it
// forwards proves the exclusion bitmap leaked.
func g1gcUpdateRoots() {
	gcw := &getg().m.p.ptr().gcw
	rootStart := nanotime()
	for i := uint32(0); i < work.baseSpans; i++ {
		if i == fixedRootFreeGStacks {
			continue
		}
		markroot(gcw, i, false)
	}
	if g1gcHasSpanSpecials() {
		for i := work.baseSpans; i < work.baseStacks; i++ {
			markroot(gcw, i, false)
		}
	}
	g1LastEvacRootsMarkNs = nanotime() - rootStart
}

// g1gcRescanStacks rewrites stack slots that point into evacuated regions.
// Stack targets are not part of the exclusion bitmap, so every evacuation
// pause pays this pass; the world is stopped and scanstack is driven with
// the lightweight _Gscan ownership protocol instead of preempt signals.
func g1gcRescanStacks() {
	gcw := &getg().m.p.ptr().gcw
	stackStart := nanotime()
	for _, gp := range allGsSnapshot() {
		g1gcRewriteStack(gp, gcw)
	}
	g1LastEvacRootsStackNs = nanotime() - stackStart
}

func g1gcRewriteStack(gp *g, gcw *gcWork) {
	status := readgstatus(gp)
	if status == _Gdead {
		return
	}
	if (status == _Gwaiting || status == _Gsyscall) && gp.waitsince == 0 {
		gp.waitsince = work.tstart
	}

	// The world is stopped, so no mutator can observe or race these
	// transitions. Stopped goroutines only need the _Gscan ownership bit
	// that scanstack requires; the preempt-signal machinery of suspendG is
	// unnecessary because there is nothing running to signal. Any
	// unexpected status falls back to the full protocol.
	systemstack(func() {
		userG := getg().m.curg
		selfScan := gp == userG && readgstatus(userG) == _Grunning
		if selfScan {
			casGToWaitingForSuspendG(userG, _Grunning, waitReasonGarbageCollectionScan)
			scanstack(gp, gcw)
			casgstatus(userG, _Gwaiting, _Grunning)
			return
		}

		s := readgstatus(gp)
		if (s == _Gwaiting || s == _Gsyscall) && castogscanstatus(gp, s, s|_Gscan) {
			gp.preemptStop = false
			gp.preempt = false
			gp.stackguard0 = gp.stack.lo + stackGuard
			scanstack(gp, gcw)
			casfrom_Gscanstatus(gp, s|_Gscan, s)
			return
		}
		if s == _Gdead || s == _Gdeadextra {
			return
		}

		stopped := suspendG(gp)
		if !stopped.dead {
			scanstack(gp, gcw)
		}
		resumeG(stopped)
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
		tag := g1ScanTag + 1
		g1ScanTag = tag
		for i, count := uint64(0), g1UsedCount.Load(); i < count; i++ {
			index := g1UsedRegions[i]
			if g1Regions[index].usedBytes.Load() == 0 {
				continue
			}
			g1Regions[index].scanTag = tag
		}
		g1gcSweepHeapSpans(tag, func(s *mspan, _ uintptr) bool {
			g1gcRewriteSpan(s)
			return true
		})
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
	if debug.g1gc != 0 {
		g1gcRecordSweepFreed(s, uint64(s.g1evacLiveObjects))
	}
	s.g1evacLiveObjects = 0
	for i := uintptr(0); i < divRoundUp(uintptr(s.nelems), 8); i++ {
		*s.allocBits.bytep(i) = 0
		*s.gcmarkBits.bytep(i) = 0
	}
	if gcUsesSpanInlineMarkBits(s.elemsize) {
		g1gcClearInlineMarks(s)
	}
	s.allocCache = 0
	s.freeindex = 0
	s.freeIndexForScan = 0
}

func g1gcFinalizeEvacuation() {
	assertWorldStopped()
	finalStart := nanotime()
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
	g1LastEvacFinalNs = nanotime() - finalStart
}

// Keep the architecture import tied to this file's pointer-slot contract.
var _ = goarch.PtrSize
