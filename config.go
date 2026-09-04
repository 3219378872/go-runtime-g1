package g1gc

import "time"

// Config controls the managed heap and the G1 policy. Zero values for policy
// fields are filled with defaults by New.
type Config struct {
	HeapSize                 int64
	RegionSize               int64
	GCWorkers                int
	MaxPause                 time.Duration
	MaxTenuringAge           uint8
	MixedGCCount             int
	InitiatingHeapOccupancy  int
	EvacuationReservePercent int
}

// DefaultConfig returns a practical configuration for a managed heap of the
// supplied size. RegionSize is chosen close to the G1 target of at most 2048
// regions and rounded to a power of two.
func DefaultConfig(heapSize int64) Config {
	regionSize := int64(1 << 20)
	if heapSize > 0 {
		for regionSize > 1 && heapSize/regionSize < 1024 {
			regionSize >>= 1
		}
		for regionSize < 32<<10 && regionSize < heapSize {
			regionSize <<= 1
		}
		if regionSize > heapSize {
			regionSize = heapSize
		}
	}
	return Config{
		HeapSize:                 heapSize,
		RegionSize:               regionSize,
		GCWorkers:                1,
		MaxTenuringAge:           3,
		MixedGCCount:             8,
		InitiatingHeapOccupancy:  45,
		EvacuationReservePercent: 10,
	}
}

func (c Config) normalized() (Config, error) {
	if c.HeapSize <= 0 || c.RegionSize <= 0 || c.RegionSize > c.HeapSize {
		return Config{}, ErrInvalidConfig
	}
	if c.MaxPause < 0 {
		return Config{}, ErrInvalidConfig
	}
	if c.GCWorkers <= 0 {
		c.GCWorkers = 1
	}
	if c.MaxTenuringAge == 0 {
		c.MaxTenuringAge = 3
	}
	if c.MixedGCCount <= 0 {
		c.MixedGCCount = 8
	}
	if c.InitiatingHeapOccupancy <= 0 {
		c.InitiatingHeapOccupancy = 45
	}
	if c.InitiatingHeapOccupancy > 100 {
		return Config{}, ErrInvalidConfig
	}
	if c.EvacuationReservePercent < 0 || c.EvacuationReservePercent >= 100 {
		return Config{}, ErrInvalidConfig
	}
	return c, nil
}
