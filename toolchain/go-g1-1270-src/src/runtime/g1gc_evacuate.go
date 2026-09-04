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

// A window must project at least this many dead bytes behind its live ones to
// justify engaging at all: moving a few kilobytes of survivors to free their
// sparse spans only pays when the reclaimed space covers a meaningful slice of
// the copy budget. Live bytes additionally face the absolute floor below so a
// window never moves a handful of tiny objects.
const (
	g1EvacuationMinReclaimBytes = g1EvacuationCopyBudget / 8
	g1EvacuationMinLiveFloor    = 1 << 10
)

// Windows whose selection finds nothing viable end without consuming their
// allocation credit, so an accumulation of reclaimable garbage retries soon.
// Heaps where selection keeps coming up empty (uniformly dense live data)
// would pay the marking-time recording tax forever, so after this many
// consecutive idle windows evacuation suspends entirely until the heap grows
// by the re-arm fraction, which is the signal that fragmentation returned.
const (
	g1EvacuationIdleSuspendAfter = 3
	g1EvacuationRearmNum         = 5
	g1EvacuationRearmDen         = 4
)

// g1EvacRegionEpoch is a conservative address filter for pointer rewriting.
// It is reset logically by the epoch rather than by clearing the table.
var g1EvacRegionEpoch [g1RegionCount]uint64

// g1EvacEpoch is only read while the world is stopped during pointer rewrite.
var g1EvacEpoch uint64
var g1EvacLastAlloc uint64

// Idle-window accounting for adaptive arming. All three are written only
// while the world is stopped (mark termination and cycle start) and read in
// the same contexts.
var (
	g1EvacIdleWindows     int
	g1EvacSuspended       bool
	g1EvacSuspendHeapLive uint64
)

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

// g1EvacuationMaxCandidates bounds the spans one window may copy. The copy
// budget already caps total bytes; each candidate is at least one page.
var g1EvacCandidates [1 << 8]*mspan
var g1EvacCandidateLive [1 << 8]uint64

// Keep the architecture import tied to this file's pointer-slot contract.
var _ = goarch.PtrSize
