package g1gc

// ObjectID is a stable handle into the managed object heap. A handle remains
// usable after evacuation; Resolve follows the forwarding chain to the new
// object.
type ObjectID uint64

const NullObject ObjectID = 0

// RegionID is the zero-based identifier of a heap region.
type RegionID int

// Phase is the externally observable G1 cycle phase.
type Phase uint8

const (
	PhaseIdle Phase = iota
	PhaseInitialMark
	PhaseConcurrentMark
	PhaseRemark
	PhaseCleanup
	PhaseEvacuation
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseInitialMark:
		return "initial-mark"
	case PhaseConcurrentMark:
		return "concurrent-mark"
	case PhaseRemark:
		return "remark"
	case PhaseCleanup:
		return "cleanup"
	case PhaseEvacuation:
		return "evacuation"
	default:
		return "unknown"
	}
}

// Cause describes why a collection was requested.
type Cause uint8

const (
	CauseExplicit Cause = iota
	CauseAllocationFailure
	CausePeriodic
)

func (c Cause) String() string {
	switch c {
	case CauseExplicit:
		return "explicit"
	case CauseAllocationFailure:
		return "allocation-failure"
	case CausePeriodic:
		return "periodic"
	default:
		return "unknown"
	}
}
