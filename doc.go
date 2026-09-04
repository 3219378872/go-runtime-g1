// Package g1gc is a standalone object-heap simulator used to validate G1
// collection algorithms: regions, marking, remembered sets, collection sets,
// and evacuation.
//
// Objects are addressed through stable ObjectID handles instead of Go
// pointers, because a plain Go package cannot intercept Go pointers. The Go
// runtime still manages this package's own metadata; G1 manages the simulated
// object graph built through the Heap API.
//
// Layout (one concern per file, low coupling, high cohesion):
//
//	Heap lifecycle and locking discipline: heap.go (state), cycle.go (Collect/GC)
//	Configuration and identities: config.go, id.go, errors.go
//	Heap data model: object.go, region.go, stats.go
//	Mutator paths: alloc.go (allocation), pool.go (free pool, active cache,
//	used accounting), refs.go (roots/references/pins)
//	Collector paths: mark.go (concurrent marking), sweep.go (cleanup),
//	  cset.go (collection-set selection), evac.go (evacuation),
//	  rset.go (remembered sets)
//	Policy and observability: policy.go, snapshot.go, validate.go.
//	Tests mirror the layout: cycle/evac/rset/mark/policy/pool_test.go plus
//	bench_sim_test.go for the benchmark gate.
//
// Locking discipline: mu guards every field of Heap. world is a
// stop-the-world gate — mutators hold world.RLock, STW phases hold
// world.Lock. cycleMu serializes whole Collect calls. Methods whose names
// end in Locked require the caller to hold mu (and world as documented at
// the call site).
package g1gc
