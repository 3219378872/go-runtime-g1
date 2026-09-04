// Copyright 2026 The Go Runtime G1 project.
//
// G1 region accounting: dirty tracking, heap-sweep recount,
// live totals, sticky snapshot publish, and validation.
// Refresh paths run STW only; dirty flags are set concurrently.
package runtime

import (
	"internal/runtime/sys"
)

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

// g1gcRegionIndex maps a span to the logical region containing its base. A
// large span may cross a logical region boundary, but keeping the span whole
// is required by the existing allocator and keeps the policy metadata exact
// at span granularity.
//
//go:nosplit
func g1gcRegionIndex(base uintptr) uintptr {
	return (base >> g1RegionShift) & (g1RegionCount - 1)
}
