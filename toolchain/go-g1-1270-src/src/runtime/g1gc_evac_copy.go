// Copyright 2026 The Go Runtime G1 project.
//
// G1 evacuation copy: destination allocation, span copying,
// source retirement, and window finalization.
package runtime

import (
	"internal/runtime/atomic"
	"unsafe"
)

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
