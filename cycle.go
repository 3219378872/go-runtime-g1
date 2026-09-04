package g1gc

import (
	"context"
	"fmt"
	"time"
)

func (h *Heap) phaseLocked(phase Phase) {
	h.state = phase
}

// Collect performs one full G1-style cycle. Mutators are allowed during the
// concurrent-mark phase and are stopped for the other phases.
func (h *Heap) Collect(ctx context.Context, cause Cause) (Stats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.cycleMu.Lock()
	defer h.cycleMu.Unlock()

	if err := ctx.Err(); err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	if err := h.checkOpenLocked(); err != nil {
		h.mu.Unlock()
		h.world.Unlock()
		return Stats{}, err
	}
	if h.state != PhaseIdle {
		h.mu.Unlock()
		h.world.Unlock()
		return Stats{}, ErrCycleInProgress
	}
	h.cycle++
	stats := Stats{
		Cycle:           h.cycle,
		Cause:           cause,
		BeforeUsedBytes: h.usedBytesLocked(),
		PhaseDurations:  make(map[Phase]time.Duration),
	}
	h.phaseLocked(PhaseInitialMark)
	initialStart := time.Now()
	h.beginMarkingLocked()
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseInitialMark)
	stats.PhaseDurations[PhaseInitialMark] = time.Since(initialStart)

	h.mu.Lock()
	h.phaseLocked(PhaseConcurrentMark)
	h.mu.Unlock()
	concurrentStart := time.Now()
	err := h.runConcurrentMark(ctx)
	concurrentDuration := time.Since(concurrentStart)
	stats.Phases = append(stats.Phases, PhaseConcurrentMark)
	stats.PhaseDurations[PhaseConcurrentMark] = concurrentDuration
	stats.ConcurrentMarkDuration = concurrentDuration
	if err != nil {
		h.abortCycle()
		return stats, err
	}

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Remark, cleanup, and evacuation are stop-the-world phases. The shared
	// mutex makes every public mutator wait while these snapshots change.
	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseRemark)
	remarkStart := time.Now()
	h.finishMarkingLocked()
	h.collectMarkStatsLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseRemark)
	stats.PhaseDurations[PhaseRemark] = time.Since(remarkStart)

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseCleanup)
	cleanupStart := time.Now()
	h.cleanupLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseCleanup)
	stats.PhaseDurations[PhaseCleanup] = time.Since(cleanupStart)

	if err := ctx.Err(); err != nil {
		h.abortCycle()
		return stats, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	h.world.Lock()
	h.mu.Lock()
	h.phaseLocked(PhaseEvacuation)
	evacuationStart := time.Now()
	evacuationErr := h.evacuateLocked(&stats)
	h.finishCycleLocked(&stats)
	h.mu.Unlock()
	h.world.Unlock()
	stats.Phases = append(stats.Phases, PhaseEvacuation)
	stats.PhaseDurations[PhaseEvacuation] = time.Since(evacuationStart)

	stats.PauseDuration = stats.PhaseDurations[PhaseInitialMark] + stats.PhaseDurations[PhaseRemark] + stats.PhaseDurations[PhaseCleanup] + stats.PhaseDurations[PhaseEvacuation]
	// finishCycleLocked recorded AfterUsedBytes inside the evacuation critical section.
	stats.Completed = true
	h.mu.Lock()
	h.lastStats = cloneStats(stats)
	h.mu.Unlock()
	if evacuationErr != nil {
		// An evacuation failure is recoverable: failed regions retain their live
		// objects and the cycle still completes after sweeping dead objects.
		return stats, evacuationErr
	}
	return stats, nil
}

// GC is the concise explicit-collection form.
func (h *Heap) GC() (Stats, error) {
	return h.Collect(context.Background(), CauseExplicit)
}

func (h *Heap) finishCycleLocked(stats *Stats) {
	// No per-object mark clearing: the epoch is bumped at the next
	// beginMarkingLocked, invalidating all marks in O(1).
	h.marking = false
	h.markCancelled = false
	h.satb = h.satb[:0]
	h.markQueue = h.markQueue[:0]
	h.markActive = 0
	h.state = PhaseIdle
	for _, r := range h.regions {
		if r.kind != RegionFree {
			r.lastLiveBytes = r.used
		}
	}
	h.rebuildRememberedSetsLocked()
	stats.AfterUsedBytes = h.usedBytesLocked()
}

func (h *Heap) abortCycle() {
	h.world.Lock()
	h.mu.Lock()
	// Invalidate all marks in O(1) by bumping the epoch instead of
	// clearing every object.
	h.markEpoch++
	if h.markEpoch == 0 {
		for _, obj := range h.objects {
			obj.markEpoch = 0
		}
		h.markEpoch = 1
	}
	h.marking = false
	h.markCancelled = true
	h.satb = h.satb[:0]
	h.markQueue = h.markQueue[:0]
	h.markActive = 0
	h.state = PhaseIdle
	h.markCond.Broadcast()
	h.mu.Unlock()
	h.world.Unlock()
}
