# Current State

This checkout contains a real Go 1.26.1-based runtime fork under
`toolchain/go-g1-1261-src`. The root `justfile` is the supported entry point
for format checks, runtime/SSA gates, project tests, race tests, and matched
official-versus-candidate benchmarks.

## Iteration 2026-08-21: consistency fixes and pause attribution

Consistency fixes (root tree):

- `bench/run.sh` derives the candidate toolchain from `CANDIDATE_ROOT`
  (default `toolchain/go-g1-1261-src`, matching the justfile); the justfile
  bench recipes pass it through. Previously the scripts silently defaulted to
  the stale go-g1-src fork.
- Removed a redundant `AfterUsedBytes` recomputation outside the evacuation
  critical section (`types.go`) and aligned `RegionCount` locking (`heap.go`).
- `go.mod` moved to go 1.26; REBASE-1.26.md no longer claims the fork is fully
  gitignored (only `bin/` and `pkg/` are).

Evacuation hardening (`runtime/g1gc_evacuate.go`):

- An inbound-index overflow now defers the whole evacuation instead of
  degrading into an unbounded full-heap pointer rewrite inside the pause.
- The projected owner-span set is built before any copy commits; evacuations
  whose owner set exceeds 512 spans or whose live objects exceed 16384 are
  deferred. The copy budget shrank from 4 MiB to 1 MiB so the destination
  slot walk stays cheap.

Incremental live accounting (`runtime/g1gc.go`, `runtime/mgcmark.go`,
`runtime/mgc.go`):

- First marks in `greyobject` and `gcmarknewobject` charge the object's region
  through `g1gcRecordLiveObject`, gated on `g1EvacIndexActive` so only
  evacuation-window cycles pay anything.
- A window cycle re-bases the counters at start (`g1gcResetLiveCounts`) and
  its mark termination skips the mark-bit census entirely; a per-region
  generation tag overwrites stale totals on first touch. All other cycles,
  including every non-window cycle, keep the exact pre-existing code path.

Measured attribution (7-run alternating matrices vs official go1.26.6,
pointer64, LIVE_ROOTS=131072, GOMAXPROCS=2, CPU_LIST=0,2):

- Fork intrinsic overhead (no GODEBUG flags): throughput 0.98-1.04x, no STW
  anomalies. `g1gc=1` bookkeeping alone: throughput 0.99x, zero spikes.
- With `g1gc=1,g1evac=1`: exactly one multi-ms mark-termination pause per
  evacuation window (candidate max ~5x official max). Differential testing
  ruled out the copy, the targeted rewrite, and inbound recording as the
  dominant term; verbose `g1evac=2` runs showed the windows copy nothing on
  this workload, so the pause was pure selection overhead.
- The census skip removed the mark-bit walking share of that pause (spikes
  1.5-3.9 ms -> 0.9-1.6 ms in matched smokes). What remains is the span walk
  in `g1gcInitializeUsed` itself plus selection; removing it needs fully
  incremental used-region tracking integrated with sweep.
- heap_sys and small-scale stw-tail ratios reported by earlier summaries were
  measurement noise: official heap_sys itself swings 7.6-47.6 MB between
  identical runs at a 4 MB live set.

## Verified

- `just fmt`
- `just check-format`
- `just verify-runtime`
- `just test-project`
- `just test-race`
- Correctness smoke under `gctrace=1,g1gc=1,g1evac=1`: repeated real
  evacuations with no bad-pointer faults.
- Benchmark labels `iter-baseline-*`, `iter-e1-*`, `iter-e2-*`,
  `iter-live128k-pointer64`, `iter-fix-live128k-pointer64`,
  `iter-fix2-live128k-pointer64` under `bench/results/repeated/`.

## Known Limits

- The runtime fork has not reached stable performance parity. Throughput is
  parity or better without GODEBUG flags; enabling `g1evac` still costs one
  selection-bound pause per evacuation window, now dominated by the span walk
  in `g1gcInitializeUsed`. Eliminating it requires incremental used-region
  tracking integrated with sweep; both the design and the counting hooks are
  in place.
- The simulated package remains O(heap) in several operations and is intended
  for testing, not scalability work.
- Official/candidate comparisons pair go1.26.6 against a fork based on
  go1.26.1; upstream deltas between those tags are not controlled for.

Generated toolchain output and benchmark history are intentionally excluded
from Git. Recreate them with `just build-toolchain` and the benchmark recipes.
