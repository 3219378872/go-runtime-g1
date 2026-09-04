package g1gc

func testConfig() Config {
	return Config{
		HeapSize:                 16 * 1024,
		RegionSize:               1024,
		GCWorkers:                2,
		MaxTenuringAge:           3,
		MixedGCCount:             8,
		EvacuationReservePercent: 10,
	}
}
