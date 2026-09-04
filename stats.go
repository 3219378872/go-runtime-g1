package g1gc

import (
	"sort"
	"time"
)

// Stats describes one complete G1 cycle.
type Stats struct {
	Cycle                  uint64
	Cause                  Cause
	Completed              bool
	Phases                 []Phase
	PhaseDurations         map[Phase]time.Duration
	PauseDuration          time.Duration
	ConcurrentMarkDuration time.Duration
	BeforeUsedBytes        int64
	AfterUsedBytes         int64
	MarkedObjects          int
	MarkedBytes            int64
	ReclaimedBytes         int64
	MovedObjects           int
	EvacuatedBytes         int64
	FreedRegions           int
	SelectedRegions        []RegionID
	FailedRegions          []RegionID
	SkippedRegions         []RegionID
}

func clonePhaseDurations(in map[Phase]time.Duration) map[Phase]time.Duration {
	out := make(map[Phase]time.Duration, len(in))
	for phase, duration := range in {
		out[phase] = duration
	}
	return out
}

func cloneStats(in Stats) Stats {
	in.Phases = clonePhases(in.Phases)
	in.PhaseDurations = clonePhaseDurations(in.PhaseDurations)
	in.SelectedRegions = cloneRegionIDs(in.SelectedRegions)
	in.FailedRegions = cloneRegionIDs(in.FailedRegions)
	in.SkippedRegions = cloneRegionIDs(in.SkippedRegions)
	return in
}

func clonePhases(in []Phase) []Phase {
	if len(in) == 0 {
		return nil
	}
	out := make([]Phase, len(in))
	copy(out, in)
	return out
}

func cloneRegionIDs(in []RegionID) []RegionID {
	if len(in) == 0 {
		return nil
	}
	out := make([]RegionID, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
