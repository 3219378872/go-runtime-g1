package g1gc

import "errors"

var (
	ErrInvalidConfig     = errors.New("g1gc: invalid configuration")
	ErrInvalidSize       = errors.New("g1gc: object size must be positive")
	ErrInvalidObject     = errors.New("g1gc: object does not exist")
	ErrInvalidReference  = errors.New("g1gc: reference target does not exist")
	ErrInvalidSlot       = errors.New("g1gc: reference slot is out of range")
	ErrOutOfMemory       = errors.New("g1gc: managed heap is out of memory")
	ErrEvacuationFailure = errors.New("g1gc: evacuation failed for at least one region")
	ErrCycleInProgress   = errors.New("g1gc: collection cycle already in progress")
	ErrContextCancelled  = errors.New("g1gc: collection cancelled")
	ErrInvalidRegion     = errors.New("g1gc: region does not exist")
	ErrAlreadyClosed     = errors.New("g1gc: heap is closed")
)
