# Current State

This checkout contains a real Go runtime fork under
`toolchain/go-g1-1270-src` (go1.27.0-based; the older go1.26.6 and
go1.26.1 trees are kept for reference). The root `justfile` is the supported
entry point for format checks, runtime/SSA gates, project tests, race tests,
and matched official-versus-candidate benchmarks. The benchmark comparator
is now official go1.27.0 (`toolchain/official-go-1270/`, gitignored).

## Iteration 2026-08-24e: rebase to go1.27.0

Session goal was "跟进 go runtime 1.27.0". The rebase landed first-try with
zero merge conflicts and zero manual hook fixes — see REBASE-1.27.md for
the drift table (17 fork-modified files; only ten drifted upstream, all
three-way merged cleanly; the new xRegScan async-register scan arrives via
new preempt_{,no}xreg.go files and routes through existing hooks).

### Gates (all green)

make.bash on the go1.26.6 bootstrap; TestUnsafePoint/TestGcSys; SSA tests;
project tests; race tests; g1gc-demo; pointer64 `g1evac=1,g1trace=1` smoke
(12 productive evacuation windows, no faults, no missed rewrites).

### Baseline matrix vs official go1.27.0 (n=7 alternating medians,
GOMAXPROCS=2, CPU_LIST=0,2, DURATION=5s, LIVE_ROOTS=1024, labels b1270-*):
| scenario | config | tp | stw_max | stw_p99 | gc_cpu |
|---|---|---|---|---|---|
| pointer64 | default | 0.976 | 1.611 | 1.140 | 1.036 |
| pointer64 | evac | 0.946 | **0.885** | 1.145 | 1.057 |
| pointer256 | default | 0.968 | 0.697 | 1.129 | 1.013 |
| pointer256 | evac | 0.898 | **0.351** | 1.043 | 1.029 |
| alloc | default | 0.982 | 0.936 | 0.871 | 1.059 |
| alloc | evac | 0.964 | 0.881 | 1.020 | 1.052 |
| frag | default | 0.946 | 0.990 | 1.000 | 1.059 |
| frag | evac | crashed — same pre-existing failure as 1.26.6 baseline | | | |

Reading: against official go1.27.0 the evacuation path keeps its tail
latency win (stw_max 0.35-0.89 across pointer/alloc scenarios) but
throughput sits 0.90-0.98x — part of the gap is upstream 1.27 improving
the default collector (register-state conservative scanning et al.), which
the fork inherits but does not yet exploit in its G1 paths. This is the
new reference baseline; success bar unchanged (tp>1.0 stable,
stw_max<1.0, stw_p99<1.0, gc_cpu<=1.0 everywhere, frag no worse than
default).

### Pre-existing correctness items re-confirmed unchanged by the rebase

A/B stress against the go1.26.6 fork with identical binaries/parameters:

- frag + `g1evac>=4,g1trace=1` (per-cycle used-region validation): "G1
  incremental used-region accounting drifted" throws 6/6 on BOTH forks.
  The incremental ledger undercounts a frag region before any window
  engages; per-cycle validation was not previously combined with frag.
- frag + `g1evac=4` without trace: bounded rewrite misses (2-67 slots)
  plus one downstream nil-gp SIGSEGV reproduce on both forks — the open
  rewrite-miss issue from 2026-08-24d, not a port regression.
- pointer64 + `g1evac=4,g1trace=1`: clean 3/3 on both forks.

### Next session

Root-cause the residual rewrite misses (greenteagc inline-mark handling in
g1gcRewriteSpan remains prime suspect), then the newly documented frag
used-region accounting undercount (reproducible deterministically with
frag + g1trace validation on both forks), then resume Phase 1 (window
pause floor).

## Iteration 2026-08-24d: rebase to go1.26.6 + frag evacuation correctness

Session goal was "comprehensively surpass go1.26.6"; work converged early
because go1.27.0 shipped mid-session — the next session should rebase onto
1.27 first using the same procedure, then resume Phase 1 (window pause
floor). What landed:

### Rebase (P0, complete)

- Upstream delta go1.26.1 -> go1.26.6 inside the fork-touched files is
  zero except `runtime/mgcmark.go` (one additive sigpanic-LR frame block
  in scanstack). Procedure: fresh pristine 1.26.6 tree, overlay the 18
  verbatim-compatible fork files, `git merge-file` three-way for
  mgcmark.go only. Compiler surface confirmed tiny in practice:
  writeBarrier struct field in builtin.go/_builtin/runtime.go only.
- justfile/bench defaults point at go-g1-1266-src; .gitignore covers its
  bin/pkg/. Gates green: TestUnsafePoint/TestGcSys, SSA tests, project
  tests, race tests, g1gc-demo, g1evac=4 smoke.
- Behavioral parity spot-check vs the 1261 fork: same ~600 windows per
  pointer64 smoke, same single productive window — the port is faithful.

### Baseline matrix after rebase (n=7 alternating medians, GOMAXPROCS=2,
CPU_LIST=0,2, DURATION=5s, LIVE_ROOTS=1024, labels p0b-*):

| scenario | config | tp | stw_max | stw_p99 | gc_cpu |
|---|---|---|---|---|---|
| pointer64 | default | 0.99 | 1.05 | 0.95 | 1.05 |
| pointer64 | evac | 0.99 | 0.98 | 1.09 | 1.08 |
| pointer256 | default | 1.03 | 0.65 | 0.69 | 1.01 |
| pointer256 | evac | 0.99 | 1.41 | 1.17 | 1.03 |
| alloc | default | 1.09 | 1.25 | 1.00 | 1.04 |
| alloc | evac | 1.01 | 0.87 | 0.79 | 1.04 |
| frag | default | 0.99 | 1.04 | 0.99 | 1.05 |
| frag | evac | crashed — see below | | | |

### frag+evac correctness chain (root-caused, two fixes landed, one open)

The crash ("found pointer to free object" / "found bad pointer") also
reproduces on the OLD 1261 fork with the same parameters — pre-existing
latent bug newly exposed because 2024-24c made stack regions evacuable
and frag now engages. Evidence chain via a new full-heap verification
pass (below): the bounded inbound-index rewrite misses heap slots whose
stale pointers keep referencing cleared source spans.

Fix 1 — terminate-time write-barrier drain
(`g1gcDrainPendingWBSlots`): upstream gcMarkTermination discards per-P
wbBufs after gcMarkDone because marking no longer needs them; an active
evacuation index still needs those (slot, pointer) pairs. Draining them
inside g1gcEvacuate before any overflow check removed most incidents.

Fix 2 — window-allocation region coverage
(`g1WindowAllocRegions/List`, hooked from g1gcRecordAllocBatch):
objects allocated during a window cycle are black, never scanned by that
cycle's marker, and bulk copies into their fresh memory (slice growth)
leave no barrier entries — their inbound edges are structurally absent
from the index. Regions that received window-cycle allocations are now
added to the stop-the-world rewrite coverage; list overflow degrades to
the conservative defer path.

Open item: misses dropped (169/59/42 -> single digits to ~250) but did
not reach zero; every residual miss has winreg=true, i.e. inside covered
regions, so something about the rewrite walk itself skips live objects —
prime suspect is mark-bit layout handling (greenteagc inline marks vs
gcmarkBits) inside g1gcRewriteSpan, which the copy path handles via
g1gcCountLive-style dual reads but the rewrite walk may not. Until
root-caused, production `g1evac=1` must be considered UNSAFE on frag-class
workloads (pointer64/pointer256/alloc smokes stay clean). Under
`g1evac>=4` the new `g1gcVerifyFullRewrite` pass rewrites whatever the
bounded passes missed before restart, converting latent corruption into
a counted diagnostic ("rewrite missed N heap slots", per-miss dump at
`g1evac>=5`) — stress results 11/12 clean runs at evac=4.

### Where this leaves the plan

- P0 done. P1 (pause floor: prewarmed destinations, copied-source list,
  chunk bitmap, quickselect, gated generation stores) untouched — still
  the throughput/stw_p99 lever. P2's exclusion-relaxation idea is moot
  until the open correctness item lands. Success bar unchanged: tp>1.0
  stable, stw_max<1.0, stw_p99<1.0, gc_cpu<=1.0 everywhere, frag no worse
  than default.
- Next session: rebase fork onto go1.27.0 (same overlay+merge-file
  procedure; re-diff drift first), re-run this matrix as the new
  baseline, then root-cause the residual rewrite misses before resuming
  performance work.

## Iteration 2026-08-24c: split root exclusion — stack regions evacuable

The root-region exclusion bitmap is now global-only. `scanblock`'s `stk`
parameter already discriminates root kinds (nil for globals, finalizer and
cleanup queues, span specials, and mutator-time special registration; non-nil
for everything stack-derived), so recording narrowed to `stk == nil` calls,
and `scanConservative` dropped its recording hook entirely (every caller is
stack-derived). Stack targets stopped being recorded, which also removes a
spanOfHeap per stack pointer from the marking hot path.

At the evacuation pause, `g1gcRescanStacks` runs unconditionally: scanstack
driven with the lightweight `_Gscan` protocol rewrites any stack slot that
points into a moved object (~36us measured). The global rescan
(`g1gcUpdateRoots`) stays skipped in production; under `g1evac=4` it runs
after the stack pass with its own forward-count baseline and throws if a
global root referenced an evacuated region.

Selection tagging is greedy by reclaimable bytes (descending, until dead
bytes cover 4 MiB ≈ several copy budgets), which keeps the sweep proportional
to the budget instead of the low-live set. Noscan source spans are eligible
again — they carry no outbound pointers, so copying them relieves
fragmentation at zero rewrite cost, and their dest spans are skipped by the
rescan pass. Copy budget reduced to 256 KiB to bound window pauses on
allocation-heavy heaps.

Results:

- pointer64 evac vs official go1.26.6 (same window, n=7): tp 1.021,
  stw_max 0.889, stw_p99 1.092, gc_cpu 1.004 — evacuation now beats the
  fork's own default path measured adjacently (default tp 0.986).
- frag evac engages for the first time on this workload (82 spans / 546 KB
  moved in one observed window; stack rescan forwards zero because the live
  set is reached through the heap-internal roots slice, whose edges the
  inbound index rewrites). Same-window tp 1.010.
- frag tail latency remains an open boundary: stw_max ~3.5x persists because
  a window's fixed costs (selection over top-reclaim regions, destination
  allocation, owner rewriting) floor around a millisecond on a 400 MB churn
  heap regardless of the 256 KiB byte budget. Next lever is cheaper window
  mechanics (pre-warmed destinations, span-sampled selection), not smaller
  budgets.

## Iteration 2026-08-24b: fragmentation workload and candidate gating

- New `frag` bench scenario: payload sizes cycle through 24B/264B/3KB/40KB
  (the last crosses into the large-object path) and once every 50k
  operations each worker migrates a contiguous quarter of its live set,
  abandoning sparse survivors behind. This is the fragmentation pressure a
  compacting collector targets.
- Evacuation windows are now also rate-limited in cycles:
  `epoch - g1EvacLastWindowEpoch >= 32`. High-allocation-rate heaps cross
  any byte threshold constantly, and unbounded window density showed up as
  gc_cpu 1.28x and stw_max 3.6x on frag.
- Inbound-edge indexing gained a sticky candidate gate (`g1StickyRegions`):
  the marker records edges only for target regions that looked like
  candidates in the previous window, so recording cost tracks the candidate
  set instead of the whole heap. Selection only picks regions that are both
  low-live and sticky, which keeps every selected source's edges fully
  indexed; non-sticky regions join the sticky set after one observed window.
- Result on frag: evacuation declines to engage (root slots reference most
  regions, and the sticky intersection stays empty), so it runs at parity
  with a large stw_max ratio from ordinary mark-term variance plus window
  taxes on the few armed windows. This documents a real boundary rather
  than a regression: root-region exclusion trades evacuation coverage for
  the pause wins on pointer workloads, and a heap whose live set is
  continuously referenced from stacks everywhere cannot be evacuated under
  that design without reintroducing root rescanning. The frag scenario
  stays as the stress case for future designs (per-G remembered sets).
- justfile now passes an absolute CANDIDATE_ROOT; a relative GOROOT breaks
  tool resolution when the go command changes working directory mid-build,
  which intermittently surfaced as fork/exec ENOENT bursts after make.bash.

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
  `iter-fix2-live128k-pointer64`, `p0b-*`, and `b1270-*` under
  `bench/results/repeated/`.

## Known Limits

- The runtime fork has not reached stable performance parity. Throughput is
  parity or better without GODEBUG flags; enabling `g1evac` still costs one
  selection-bound pause per evacuation window, now dominated by the span walk
  in `g1gcInitializeUsed`. Eliminating it requires incremental used-region
  tracking integrated with sweep; both the design and the counting hooks are
  in place.
- The simulated package remains O(heap) in several operations and is intended
  for testing, not scalability work.
- Official/candidate comparisons now pair go1.27.0 against the fork based
  on go1.27.0 (same base tag; earlier sessions compared go1.26.6 against a
  go1.26.1-based fork, an uncontrolled upstream delta).

Generated toolchain output and benchmark history are intentionally excluded
from Git. Recreate them with `just build-toolchain` and the benchmark recipes.
