// Copyright 2026 The Go Runtime G1 project.
//
// G1 cycle lifecycle: per-cycle activation flags, start/end hooks,
// and the trace snapshot. STW writes, concurrent plain reads
// (same protocol as gcphase).
package runtime

import (
	"internal/runtime/atomic"
)

// g1gcSetEvacIndexActive publishes the evacuation-index state both to the
// runtime and to the compiler-generated write-barrier fast path. The latter
// reads the field in writeBarrier at offset four, after the enabled byte and
// its padding.
func g1gcSetEvacIndexActive(value uint32) {
	atomic.Store(&g1EvacIndexActive, value)
	atomic.Store(&writeBarrier.g1Evac, value)
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
