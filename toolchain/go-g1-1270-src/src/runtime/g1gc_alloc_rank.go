// Copyright 2026 The Go Runtime G1 project.
//
// G1 region-aware refill ranking (D05): best-of-N dense-span
// preference on the mcentral fast path. Reads
// mark-termination counters as a hint; refill never runs STW.
package runtime

// g1AllocChoiceSpans bounds how many partial-swept spans the region-aware
// refill path inspects. Refills happen once per cached span, so a small
// constant keeps the worst-case cost trivial while still finding a dense
// span on fragmented size classes.
const g1AllocChoiceSpans = 8

// g1PreferDenseSpan implements region-aware allocation on the mcentral fast
// path. It is called with one span already popped from partialSwept(sg); it
// pops up to g1AllocChoiceSpans-1 more, keeps the span ranked best by
// g1SpanAllocRank, and pushes the rest back. Every examined span is swept
// for sg, so re-pushing preserves the list invariant, and any returned span
// satisfies cacheSpan's free-space contract exactly as before. The ranking
// reads mark-termination counters as a hint: refill never runs while the
// world is stopped, so the STW-only scanTag/generation fields cannot change
// under it.
func (c *mcentral) g1PreferDenseSpan(sg uint32, first *mspan) *mspan {
	best := first
	bestTier, bestLive, bestUsed := g1SpanAllocRank(first)
	if bestTier == 0 && bestLive == bestUsed {
		return best
	}
	var stash [g1AllocChoiceSpans - 1]*mspan
	n := 0
	for n < len(stash) {
		s := c.partialSwept(sg).pop()
		if s == nil {
			break
		}
		tier, live, used := g1SpanAllocRank(s)
		if g1AllocRankBetter(tier, live, used, bestTier, bestLive, bestUsed) {
			stash[n] = best
			best, bestTier, bestLive, bestUsed = s, tier, live, used
			if tier == 0 && live == used {
				n++
				break
			}
		} else {
			stash[n] = s
		}
		n++
	}
	for i := 0; i < n; i++ {
		c.partialSwept(sg).push(stash[i])
	}
	return best
}

// g1SpanAllocRank scores a refill candidate: tier 0 allocates into an
// already-dense region (packing it further), tier 1 is an untracked or empty
// region (neutral), and tier 2 is an evacuation-candidate or sparse region
// (leave it to drain so it can be collected). The sparse predicate mirrors
// the evacuation eligibility density check.
func g1SpanAllocRank(s *mspan) (tier uint32, live, used uint64) {
	index := g1gcRegionIndex(s.base())
	region := &g1Regions[index]
	used = region.usedBytes.Load()
	live = region.liveBytes.Load()
	if region.scanTag == g1ScanTag {
		return 2, live, used
	}
	if used == 0 {
		return 1, live, used
	}
	if live*2 < used {
		return 2, live, used
	}
	return 0, live, used
}

// g1AllocRankBetter reports whether a candidate outranks the current best.
// Lower tiers always win; within the dense tier the higher live fraction
// wins via cross-multiplication (region counters are far below overflow).
func g1AllocRankBetter(tier uint32, live, used uint64, bestTier uint32, bestLive, bestUsed uint64) bool {
	if tier != bestTier {
		return tier < bestTier
	}
	if tier != 0 {
		return false
	}
	return live*bestUsed > bestLive*used
}
