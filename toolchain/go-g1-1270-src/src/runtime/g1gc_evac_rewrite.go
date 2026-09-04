// Copyright 2026 The Go Runtime G1 project.
//
// G1 evacuation rewrite: pointer forwarding over indexed
// owner spans, root/stack rescans, and rewrite verification.
package runtime

import (
	"internal/runtime/atomic"
	"unsafe"
)

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
							objIdx2 := (p - b2) / sp2.elemsize
							dbit := sp2.g1evacDest.allocBitsForIndex(objIdx2).isMarked()
							print("  miss owner=", hex(s.base()), " es=", s.elemsize, " objidx=", j,
								" winreg=", window,
								" listed=", s.g1rewriteEpoch == g1EvacEpoch,
								" fi=", s.freeindex, " ac=", s.allocCount, " ne=", s.nelems,
								" abit=", j < uintptr(s.freeindex) || abits.isMarked(),
								" mbit=", mbits.isMarked(),
								" imc=", gcUsesSpanInlineMarkBits(s.elemsize),
								" tbase=", hex(b2), " tes=", sp2.elemsize, " tac=", sp2.allocCount,
								" tdbit=", dbit, "\n")
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
