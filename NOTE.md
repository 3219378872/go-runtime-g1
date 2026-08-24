# Current State

This checkout contains a real Go 1.26.1-based runtime fork under
`toolchain/go-g1-1261-src`. The root `justfile` is the supported entry point
for format checks, runtime/SSA gates, project tests, race tests, and matched
official-versus-candidate benchmarks.

## Iteration 2026-08-24: evacuation frequency and bounded selection

Throughput follow-up to the dirty-region rework:

- Evacuation windows trigger twice as often: the allocation threshold is
  max(2 GiB, 8x heapLive), down from max(4 GiB, 16x). Per-window cost is
  unchanged (copy budget stays at 1 MiB), so compaction rate doubles while
  the pause distribution keeps its shape.
- Selection tagging is now bounded by the copy budget instead of heap size:
  `g1gcTagLowLiveRegions` tags low-live regions until their used bytes cover
  8 MiB, skipping root-referenced regions. At LIVE_ROOTS=131072 this cut the
  selection sweep from ~724us to ~40us per window and removed a 3.8x
  stw_max regression.
- The per-span mark-bit census moved out of the selection sweep; only the
  handful of collected candidates are censused exactly before copying
  (`g1gcEvacEligibleCheap` filters, `g1gcEvacEligible` verifies).
- An experiment that also built a collection set for every window cycle and
  let the sweeper consume it unconditionally showed no throughput benefit
  on this workload and extra variance; it was reverted.
- `bench/run.sh` retries toolchain builds: freshly installed pkg/tool
  binaries intermittently fail to exec on this WSL host (the same flake can
  hit make.bash itself), so builds now retry with backoff.

Same-window paired medians vs official go1.26.6 (n=7, DURATION=5s,
GOMAXPROCS=2): pointer64 evac tp 1.008 / pointer64 live=128K evac tp 0.959 /
pointer256 evac tp 1.021 / fork-default tp 0.984, with stw_max 0.80-1.05 and
stw_p99 0.90-1.02 across the same runs. Across all windows measured today
the evacuation configuration lands between -4% and +2% throughput versus
official: parity within machine drift, not a stable win. The reliable wins
remain tail latency (stw_max/stw_p99) and GC-side costs; making throughput
exceed upstream will need compaction benefits to show up as mutator-visible
locality, which this uniform-churn synthetic workload does not reward.

## Iteration 2026-08-23: incremental used-region accounting

The mark-termination span walk in `g1gcInitializeUsed` is gone. Region
accounting is now maintained incrementally:

- Lifecycle hooks flag logical regions dirty instead of rebuilding state:
  mcache allocation batches (`refill`, `releaseAll`, `allocLarge`), sweep
  frees (`sweepLocked.sweep`, beside totalFree), evacuation source
  retirement (`g1gcClearSourceBits`), and user-arena/evacuation-destination
  registration (`g1gcRecordSpanAllocation`). The hooks only set a per-region
  dirty flag and append the region to a bounded dirty list; no arithmetic
  runs on the hot path.
- Mark termination walks only dirty regions. Spans are enumerated straight
  from the page allocator's pallocBits (`g1gcForEachWindowSpan`): a page
  whose `spanOfHeap` base equals its address starts a span, so packed
  adjacent spans are separated without any persistent span registry.
- The logical region index hashes addresses modulo 64 Ki regions, so the
  heap sweep iterates `mheap_.pages.inUse` ranges chunk by chunk and
  dispatches windows whose hash is dirty or scan-tagged
  (`g1gcForEachHeapChunk`, `g1gcSweepHeapSpans`). A 1 MiB window never
  crosses a 4 MiB chunk boundary; bitmap words are scanned wholesale and
  window offsets are word-aligned by construction. Totals accumulate into
  scratch buffers first so colliding windows sum exactly, then reconcile
  publishes membership (publish/retire in the used-region index).
- Selection passes (evacuation benefit/projection/copy collection and
  collection-set build) share one tag-and-sweep mechanism instead of three
  full walks; copies replay from a bounded candidate list because
  destination allocation must not run inside the heap walk.
- `GODEBUG=g1evac=4` recounts every listed region from allspans at each
  evacuation window and throws on undercount; `g1evac>=5` dumps both
  enumerations. This validation caught three real bugs during development:
  a dangling intrusive-span-list design (replaced outright), a window/bitmap
  offset mismatch that silently dropped windows at non-zero chunk offsets,
  and it now guards the ledger continuously.

Evacuation pause shrink:

- The three selection sweeps merged into one collect pass (~50us -> ~27us).
- Stack rescanning mirrors only the `_Gscan` ownership bit for stopped
  goroutines instead of the full suspend/resume protocol (the world is
  already stopped).
- Root slots can no longer reference evacuated objects at all: while an
  evacuation index is active, `scanblock` and `scanConservative` record the
  target region of every root pointer into `g1RootRegions`, and selection
  excludes those regions. Root rescanning is skipped entirely; under
  `g1evac=4` a full rescan runs anyway and throws if it would have
  forwarded anything, proving the filter sound.
- Measured phase cost per evacuation window on the pointer64 workload:
  select ~27us, rewrite ~4us, copy ~0us; init (dirty refresh) ~0us. The
  whole pause addition is now ~35us versus ~170us before this iteration and
  multi-millisecond before the census skip existed.

Matched 5-run alternating benchmarks vs official go1.26.6 (pointer64,
LIVE_ROOTS=1024, GOMAXPROCS=2, CPU_LIST=0,2, `g1gc=1,g1evac=1`):

- stw_max median ratio 2.33 -> 0.98 (p95 4.96 -> 1.18)
- stw_p99 median 1.53 -> 0.99
- gc_cpu median 1.03 -> 0.97..1.02 across rounds
- heap_sys median 1.73 -> 1.00 (and 0.39 in one round; small-heap
  heap_sys noise dominates, see earlier note)
- throughput median ~0.98-1.00, stw_total ~0.95-1.02

Matched 7-run alternating benchmarks vs official go1.26.6 (GOMAXPROCS=2,
CPU_LIST=0,2, DURATION=5s; median candidate/official ratios):

| config | GODEBUG | throughput | stw_total | stw_max | stw_p99 | gc_cpu |
|---|---|---|---|---|---|---|
| pointer64 live=1024 | default | 1.035 | 0.991 | 1.051 | 0.957 | 1.009 |
| pointer64 live=1024 | g1evac | 0.992 | 1.002 | **0.866** | **0.890** | 1.014 |
| pointer64 live=128K | default | 0.982 | 1.015 | 1.040 | 1.116 | 1.007 |
| pointer64 live=128K | g1evac | 0.990 | 1.010 | 1.021 | 0.954 | 1.008 |
| pointer256 live=1024 | default | 1.011 | 0.995 | 1.141 | 1.133 | 1.012 |
| pointer256 live=1024 | g1evac | 1.008 | 1.018 | **0.955** | **0.798** | 1.025 |

The default-path rows have every G1 hook disabled, so their spread is the
noise floor of this shared machine plus upstream go1.26.1->go1.26.6 drift;
the evacuation rows sit inside or below that band on the pause metrics.
A later 5-run spot check of the final build measured stw_max median 0.846
(min 0.44) with throughput 1.014 and gc_cpu 0.998.

Known limits of the new design:

- Refresh cost is linear in mapped footprint chunks per evacuation window
  even when few regions are dirty (the inUse walk). Fine for current
  benchmark scales; a chunk-presence bitmap would bound it further.
- Regions referenced by root slots are never evacuated in that window;
  stacks pointing at long-lived garbage reduce candidate coverage.
- The one-time bootstrap still walks allspans at the first accounting-
  enabled mark termination to cover spans created before GODEBUG parsing.

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
