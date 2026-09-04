// Copyright 2026 The Go Runtime G1 project.
//
// G1 inbound-edge index: per-P dedup tables, global region chains,
// write-barrier slot recording, and root/window-allocation region
// tracking. Recorded concurrently, consumed only while STW.
package runtime

import (
	"internal/runtime/atomic"
	"unsafe"
)

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
