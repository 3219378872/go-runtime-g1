# Current State

> 迁移状态：本文件为原始迭代日志档案（append-only），保留全文。结构化结论已迁移至 `docs/` 五层知识库，解读以 `docs/` 为准：意图 `docs/intent/`、规格 `docs/spec/`、设计 `docs/design/`、实现 `docs/impl/M02-fork-map.md`、证据 `docs/evidence/`，对照见 `docs/README.md#迁移对照`。

This checkout contains a real Go runtime fork under
`toolchain/go-g1-1270-src` (go1.27.0-based; the older go1.26.6 and
go1.26.1 trees are kept for reference). The root `justfile` is the supported
entry point for format checks, runtime/SSA gates, project tests, race tests,
and matched official-versus-candidate benchmarks. The benchmark comparator
is now official go1.27.0 (`toolchain/official-go-1270/`, gitignored).

## Iteration 2026-09-03c: same-session old-vs-new A/B — feature neutral

Built the pre-change tree separately (`/tmp/opencode/ab/oldroot`,
pristine `HEAD` sources, own `make.bash`) and ran interleaved A/B
(`OFFICIAL_GO`=old, candidate=new, same `GODEBUG` both sides,
alternating within `repeat.sh`, ratio = new/old), 15s n=7:

| label | tp med [min,max] | stw_max | stw_p99 med | gc_cpu | rss_final |
|---|---|---|---|---|---|
| ab-p64evac (g1gc+evac) | 1.020 [0.899,1.192] | 0.494 | 0.497 | 1.048 | 1.033 |
| ab-fragevac (g1gc+evac) | 0.999 [0.927,1.104] | 0.930 | 0.975 | 1.001 | 0.937 |
| ab-p64default (gate off) | 1.049 [0.999,1.082] | 0.622 | 0.886 | 0.995 | 1.019 |
| abr-p64default reversed | 1.001 [0.945,1.029] | — | — | 0.985 | — |

Verdict: **no adjudicable feature effect, and the session proves why**.
The gate-off control moved +4.9% with the feature inert (all 7 runs
≥0.999), yet the reversed control 10 minutes later reads 1.0005 —
the setup disagrees with itself, so per 25e discipline the ±5% band
here is unmeasurable (alignment lottery across two toolchain builds
plus host drift; alternating pairs cannot cancel either). The active
rows (+2.0% p64-evac, -0.1% frag-evac) sit inside that envelope, with
stw tails swinging on single-run spikes both sides (p99 max 18x on one
ab-p64evac run). gc_cpu is 1.00-1.05 throughout. Nothing replicates in
either direction: the MVP is performance-neutral within this host's
capability, with correctness green (verify + 9/9 stress from 0903b).

### Next session

- Only bare metal or a quieter host can adjudicate sub-5% alloc-path
  effects; on this host, further refill-hint tuning cannot show through.
- If evacuations must engage harder on frag, the lever is selection
  policy (reclaim gate / arming), not allocation hints — D04 territory.

## Iteration 2026-09-03b: region-aware allocation MVP (mcentral best-of-N)

Implemented the structural lever named in 2026-08-25c/25e: the fork's
default allocation path now prefers dense regions on refill.

### Change (all gated on `debug.g1gc`, zero behavior change when off)

- `mcentral.go cacheSpan`: the `partialSwept` first-pop — the common
  refill path — now passes through `g1PreferDenseSpan` when `debug.g1gc
  != 0` (3-line hook; `mcentral.go` joins the fork fmt list in
  `justfile`).
- `g1gc.go`: `g1PreferDenseSpan` pops up to 7 more spans (bounded
  `g1AllocChoiceSpans = 8`), keeps the best-ranked, pushes the rest
  back to the same swept set (list invariant preserved; any returned
  span satisfies the existing free-space contract). Ranking
  (`g1SpanAllocRank`): tier 0 dense `live*2 >= used` → pack here;
  tier 1 untracked/empty → neutral; tier 2 `scanTag`-selected or sparse
  `live*2 < used` → leave to drain for evacuation. Dense-tier ties
  break by live fraction via cross-multiplication. Counters are
  mark-termination hints; refill never runs STW so the STW-only
  `scanTag` reads cannot race. Cost is one refill per cached span
  (amortized over hundreds of allocations).
- Deliberately untouched: unswept sweep paths, `grow`/page-allocator
  placement, `uncacheSpan` routing — grow-from-dense-pages stays a
  follow-up (needs `mheap_.alloc` region input, invasive).

### Gates (all green on the rebuilt fork)

`just check-format`, `build-toolchain`, `test-runtime`
(`TestUnsafePoint|TestGcSys`), `test-ssa`, `test-project`,
`test-race`, `bench/stress.sh` 9/9 clean (frag/pointer64/alloc ×
`g1evac=4,g1trace=1`), `g1gc-demo` OK.

### Measurement (`ra1-*`, 15s n=7, vs `matrix-0903` control)

| row | tp | stw_max | stw_p99 | gc_cpu | rss_final |
|---|---|---|---|---|---|
| ra1-pointer64-default | 0.992 | 1.007 | 0.855 | 1.099 | 0.984 |
| ra1-pointer64-evac | 0.964 | 1.005 | 1.312 | 1.044 | 1.063 |
| ra1-frag-evac | 0.993 | 0.727 | 0.936 | 1.035 | 1.059 |

Verdict per 25e discipline: **no adjudicable effect**. The ra1 session
ran after matrix-0903 (cross-session, ±3-5% drift band) with no
same-session A/B; signs are mixed (pointer64 tp down ~2-5% but ranges
overlap 0.92-1.06 vs 0.93-1.10; frag-evac stw_max improved 0.73 while
pointer64-evac stw_p99 regressed 1.31; stw_max maxima 4-7x on both
sides are single-run host spikes). gc_cpu is identical (1.044 vs 1.049
on pointer64-evac). This is the noise signature, not a systematic
regression or win — the MVP's effect is below this host's floor, as
expected for a refill-path hint (uniform pointer64 regions offer
little density contrast by construction).

### Next session

- Same-session old-vs-new A/B (rebuild pre-change toolchain under a
  separate CANDIDATE_ROOT, alternate labels) or bare metal before any
  effect claim; frag-heavy sessions should show the drain benefit first
  (sparse regions left alone → more low-live candidates).
- Follow-ups if A/B confirms neutral: grow-time dense-page preference
  (`mheap_.alloc` region input), sweep-side span routing; both need
  their own correctness review against `EvacDestHead` lifecycle.

## Iteration 2026-09-03a: standing 15s n=7 matrix — first full-protocol baseline

Ran the standing reference matrix for the first time under the S04
protocol (`LABEL_PREFIX=matrix-0903`, 15s runs, n=7 alternating, in-tree
official go1.27.0, GOMAXPROCS=2, CPU_LIST=0,2; run was interrupted after
6/8 labels and completed with two follow-up `repeat.sh` invocations under
the same prefix). Preflight passed (offline cores ok, steal 0.00%; VM/WSL
warnings as usual). Paired medians (candidate/official):

| row | tp | stw_max | stw_p99 | gc_cpu | rss_avg | rss_final |
|---|---|---|---|---|---|---|
| pointer64 default | 1.013 | 0.792 | 0.885 | 1.076 | 1.015 | 0.966 |
| pointer64 evac | 1.017 | 0.818 | 0.880 | 1.049 | 1.027 | 1.018 |
| pointer256 default | 1.020 | 0.909 | 0.981 | 1.030 | 0.988 | 1.087 |
| pointer256 evac | 0.965 | 0.888 | 0.917 | 1.003 | 0.973 | 1.017 |
| alloc default | 1.009 | 1.180 | 0.958 | 1.044 | 1.030 | 1.063 |
| alloc evac | 0.990 | 0.836 | 0.884 | 1.045 | 0.988 | 0.869 |
| frag default | 0.974 | 1.015 | 1.066 | 1.031 | 1.007 | 0.994 |
| frag evac | 0.976 | 1.169 | 1.041 | 1.058 | 1.025 | 1.174 |

Reading (per 25e discipline: median + spread, sign flips = noise):

- pointer64 at parity-or-better on BOTH rows (default 1.013, evac
  1.017) with stw_max/p99 < 1.0 — first session the fork's default path
  beats upstream on a pointer workload. But spread is wide (evac tp
  min 0.930 / max 1.103), so this is not a claim, only a data point;
  the tp-parity question for pointer-class stays open pending repeat
  sessions.
- frag+evac at 0.976 tp, indistinguishable from frag default (0.974):
  evacuation still does not engage productively on frag in this
  session, consistent with the engagement boundary from 2026-08-25b.
- frag-evac rss_final 1.174 (min 0.964 / max 1.277) flips sign within
  the session — noise per discipline, not a memory regression signal.
  alloc-evac rss_final 0.869 likewise has a 0.51 min outlier; memory
  stays at parity overall.
- gc_cpu remains 1.00-1.08 everywhere: the known residual overhead,
  unchanged.

Correctness held: `bench/stress.sh` defaults 9/9 clean (frag,
pointer64, alloc × `g1evac=4,g1trace=1`).

### Next session

- Region-aware allocation in the fork's default path (the structural
  lever named in 2026-08-25c/25e for pointer-class tp parity), measured
  against THIS matrix as the control.
- Re-run at least pointer64 default/evac + frag evac at 15s n=7 after
  the change before any claim.

## Iteration 2026-08-25e: base-RSS elevation retracted — metric discipline

Follow-up on 2026-08-25d item 4 (fork binaries carry 29.6 MB BSS vs
0.22 MB upstream; suspected residency bug). Properly paired zero-GODEBUG
runs (alternating fork-binary vs official-binary, n=4, same session)
show **no base elevation**: final RSS identical within noise on both
sides (70/84/75/60 vs 71/84/70/81 MB), and both sides' max statistics
spike to 168-222 MB under host interference. The BSS tables are virtual
only — untouched while every GODEBUG path is inert, exactly as designed.
No code change warranted.

The p3 "rss_max 1.22x" headline from the previous session is likewise a
statistic artifact: per-run paired data shows avg RSS at parity
(66.4/69.0/67.2 vs 67.1/67.1/65.9) and final RSS improved in two of
three runs (-24%, -16%, ~0). Max-of-samples amplifies transient host
events and must not headline memory claims on this host.

Standing rules going forward:

- Memory comparisons report **avg and final** RSS from paired runs;
  rss_max is diagnostic only. repeat.sh now aggregates rss_avg alongside
  max/final so summaries carry the robust numbers by default.
- Any single-metric claim that flips sign between same-day sessions is
  noise, not signal, regardless of n.

Corrected memory story for the evacuation work: average footprint at
parity, end-of-run residency up to -24% when fragmentation accumulates,
peak cost unbounded by design (copy budget) and unobservable above host
noise. The region-aware-allocation question stays open strictly on
throughput grounds; there is no memory regression to justify it.

## Iteration 2026-08-25d: RSS instrumentation — memory story corrected

Session goal was region-aware allocation; before designing it, the
memory-behavior claim needed rigor, because the long-standing "heap_sys
0.28x on frag" number came from the era when the comparator silently was
the go1.26.6 fork (see 2026-08-25b). New instrumentation:

- bench/workload samples the process resident set every 50ms from
  /proc/self/statm over the measured window (`rss_max/avg/final_mb` plus
  the full series in the JSON); bench/compare prints RSS ratio rows and
  repeat.sh aggregates them into paired summaries.
- The sampler is noise-immune for effects of its own magnitude: paired
  throughput with sampling enabled stays inside the established band.

### Findings (frag, 15s, n=5 paired vs official go1.27.0, label p3-frag-mem)

| metric | official | candidate | ratio |
|---|---|---|---|
| rss max | 122 MB | 156 MB | 1.22x |
| rss final | 75 MB | 63 MB | **0.84x** |

Isolation runs attribute the pieces:

- Candidate with `g1gc=1` only: rss_max 161 / final 82 MB.
- Candidate with zero GODEBUG: rss_max 158 / final 75 MB.
- Official-toolchain-built workload binary: rss_max ~122-145 across
  sessions (host drift visible here too).

Conclusions:

1. **The old 0.28x heap_sys story was a fork-vs-fork artifact and is
   retracted.** Against real upstream the candidate reserves MORE virtual
   heap (381 vs 276 MB) while ending LOWER in kernel-resident pages.
2. **Evacuation's peak-memory cost is ~zero**: evac-on peaks at or below
   evac-off (156 vs 158-161 MB) because windows copy at most the bounded
   budget before sources retire.
3. **Evacuation delivers real memory return**: final RSS -16% vs upstream
   and -23% vs the same fork without evacuation — compaction + scavenge
   actually hand pages back on fragmented heaps.
4. **NEW open item — fork-base residency**: the fork toolchain's binaries
   carry **29.6 MB of BSS versus 0.22 MB upstream** (size(1)), which
   matches the g1 static tables (16 MB inbound-edge array, ~4 MB region
   stats, ~6 MB of per-region index arrays). Paired runs show the base
   elevation exists with every GODEBUG path inert, though the arrays are
   only touched sparsely by design — so either something touches them
   unintentionally, or the delta has a second source inside the fork's
   codegen/runtime layout. This is now the top investigation target.

### Next session

- Root-cause the fork-base RSS elevation: page-level attribution via
  /proc/self/smaps diffs between the two binaries, then shrink or lazily
  back the oversized static tables (the 1M-entry inbound-edge array does
  not need physical storage until an armed window actually records).
- Re-measure tp/RSS after the tables shrink; revisit whether region-aware
  allocation still matters once the baseline is clean.

## Iteration 2026-08-25c: overhead attribution — steady-state tax is noise

Session goal was chasing the gc_cpu overhead flagged by p1b (1.03-1.09x).
Three-config attribution on pointer64 (default / `g1gc=1` / `+g1evac=1`,
n=5 alternating vs official go1.27.0) measured **no separable bookkeeping
tax**: default tp 0.988 / gc_cpu 1.041, g1gc-only 0.992 / 1.007, evac
1.032 / 0.991 — the evac row beat default inside one session, confirming
that idle-window suspension drives evacuation's steady-state cost below
the noise floor. The considered optimization (gating the allocation/sweep
hooks on `g1gcUsedStatsActive`) was therefore REJECTED: it would add a
gate to hot paths for an unmeasurable win.

Full eight-row matrix (labels p2-*, n=7 @5s) came out host-noise-dominated:
alloc-default posted stw_max 1.85 and pointer256 rows split 1.035/0.924,
so five key rows were re-run at DURATION=15s (p2s-*):

| row | tp | stw_max | stw_p99 | gc_cpu |
|---|---|---|---|---|
| pointer64 evac | 0.958 | 0.913 | 1.103 | 1.030 |
| alloc evac | **1.007** | 0.929 | 0.844 | 1.039 |
| frag evac | 0.983 | 0.997 | 1.023 | 1.063 |
| frag default | 0.985 | 0.895 | 0.953 | 1.029 |

Reading: alloc-evac is stable at parity-or-better across three independent
measurement sessions (0.988 / 1.007 / 1.007). Everything else swings
0.96-1.04 between sessions on this shared WSL host — machine variance now
exceeds the remaining deltas, so single-session medians can no longer
adjudicate sub-3% effects. Correctness stress and gates stayed green
throughout; nothing in the runtime changed this session.

### Next session

- Structural work only: region-aware allocation in the fork's default path
  is the remaining lever for pointer-class throughput parity; tuning
  evacuation further cannot show through the noise.
- Any future benchmark claims on sub-3% effects need DURATION>=15s, n>=7,
  interleaved same-hour comparisons, or a quieter host.
- Optional hygiene: consider pinning repeat.sh to report per-run spread so
  session-to-session drift is visible in summaries.

## Iteration 2026-08-25b: engagement rework — Phase 1 redirected by attribution

Session goal was Phase 1 (window pause floor). Phase attribution on the
post-fix tree killed the premise: windows now cost **47us on frag and 12us
on pointer64** (select 80%/57%, everything else ~0) — the historical
~1ms fixed cost is gone, so prewarmed destinations, the chunk-presence
bitmap, quickselect, and gated generation stores all attribute to noise at
current scales and stay deferred. The real bottleneck moved to
**engagement**: most frag attempts selected nothing (`sellive < minlive`,
burning their allocation credit), and pointer64/alloc found zero candidates
at all — evacuation was paying marking-time recording tax for windows that
never copied anything.

### Changes (all in g1gc.go / g1gc_evacuate.go)

- **Credit only on productivity**: `g1EvacLastAlloc` is committed via
  `g1gcCommitProductiveWindow` only when objects actually moved. Unproductive
  attempts end the window (deactivate + reset inbound, matching the other
  early-return paths) and retry after the next cycle instead of after another
  full allocation threshold.
- **Reclaim-based selection gate**: a window proceeds when projected reclaim
  (`spanBytes - liveBytes` over accepted candidates) reaches
  `g1EvacuationMinReclaimBytes` (copy budget / 8 = 32 KiB) with an absolute
  live floor of 1 KiB — moving 8 KiB of survivors to free 56 KiB of sparse
  spans is a clear win that the old live-bytes gate (`minLiveBytes`, used /
  4096) rejected outright on frag.
- **Adaptive arming**: three consecutive idle windows suspend evacuation;
  a suspended collector skips activation entirely (no recording tax) until
  `heapLive` grows 25% past the suspension point. Dense uniform heaps
  (pointer64, alloc) go inert within a few windows; fragmentation returning
  re-arms automatically. Trace lines gained `evac-idle` / `evac-suspended`.
- **Comparator fix** (bench/run.sh): default OFFICIAL_GO now prefers
  `toolchain/official-go-1270/go/bin/go`; `/usr/local/go` on this host is a
  fork-built go1.26.6, which silently turned comparisons into
  fork-vs-fork. The p0-fix-frag numbers from the previous session were
  actually against go1.26.6-fork; direction unchanged, magnitude unverified.

### Baseline matrix (n=7 alternating medians vs official go1.27.0,
GOMAXPROCS=2, CPU_LIST=0,2, DURATION=5s, LIVE_ROOTS=1024, labels p1b-*):

| scenario | config | tp | stw_max | stw_p99 | gc_cpu |
|---|---|---|---|---|---|
| pointer64 | evac | 0.957 | 1.046 | 1.100 | 1.080 |
| frag | evac | **1.041** | **0.568** | 1.027 | 1.032 |
| alloc | evac | **1.007** | 0.940 | 1.028 | 1.089 |

Reading: frag+evac is fully healthy for the first time — it engages,
compacts, beats upstream throughput, and cuts worst-case pauses to 0.57x.
alloc crossed throughput parity. pointer64 improved (0.946 -> 0.957) but its
stw_max slipped versus the exceptional b1270 sample (0.885); its gap to tp
parity is now mostly early-window taxes plus g1gc bookkeeping, since
suspension zeroes the steady-state cost.

Correctness held throughout: frag/pointer64 x g1evac=4,g1trace=1 stress
10/10 clean; just verify green; production-flag smokes clean.

### Next session

- Chase the remaining gc_cpu overhead (~3-9%): profile whether it is
  mark-time inbound recording during armed windows or the always-on g1gc=1
  bookkeeping (my nextFree/refill hooks are candidates), then either bound
  recording tighter or cheapen the hooks.
- pointer64 tp parity: the workload's half-live heap offers no low-live
  regions, so the endgame there may be making the default path itself faster
  (region-aware allocation) rather than tuning evacuation further.
- Re-run the full eight-row matrix including default rows as the standing
  reference before any new performance work.

## Iteration 2026-08-25: frag correctness chain closed

Session goal was the P0 pair from 2026-08-24e: root-cause the residual
rewrite misses and the frag used-region accounting undercount, restoring
`g1evac` safety on frag-class workloads. All three open defects are fixed;
the "g1evac=1 UNSAFE on frag" caveat from 2026-08-24d is lifted.

### Fix 1 — used-region undercount came from cached-span allocations

Reproduced deterministically (frag + `g1evac=4,g1trace=1`, threw within
~75 cycles): `region N used-bytes undercount auth=X got=X-6048`, where 6048
is exactly one size-class-288 span's worth of objects (21×288). At `g1evac=5`
the allspans/windowscan dumps agreed completely, so the recount enumeration
was sound — the ledger was merely stale. Root cause: the accounting hooks
fired at mcache `refill` (uncaching the OLD span), `releaseAll`, and
`allocLarge`, but a span sitting cached in an mcache absorbs allocations via
the fast path across many GC cycles with no hook at all — modern runtimes do
not flush mcaches at GC boundaries. Any region whose only change was
cached-span growth stayed clean until an unrelated event dirtied it.

Fix (`malloc.go` `nextFree` slow path + `mcache.go` `refill`): dirty the
span's region once per allocCache batch (~64 allocations, amortized cost)
and when a fresh span enters the cache, so fast-path consumption of
preloaded cache words is covered too. Hooks remain gated on `debug.g1gc`.

### Fix 2 — rewrite misses came from sticky-snapshot timing

Enhanced the `g1evac>=5` miss dump (owner `listed=` membership, predicate
bits `abit/mbit/imc`, target `tbase/tac/tdbit`). Decisive facts from real
misses: every missed slot had `tdbit=false` yet the copy log showed the
target WAS copied — the owner span simply had `listed=false winreg=false`,
i.e. no bounded pass ever visited it. A forced-recording experiment (bypass
the sticky gate) drove misses to zero, convicting the gate itself:

`g1gcInitializeUsed` republished `g1StickyRegions` from fresh stats BEFORE
`g1gcEvacuate` selected candidates, but the window's marker had recorded
edges against the PREVIOUS window's snapshot. Any region newly low-live
since then (constant on frag, whose migrations shift live sets between
windows) entered the collection set with zero indexed edges. pointer64's
stable live sets made the two snapshots coincide, hiding the bug.

Fix: selection may only pick regions the recorder actually tracked, so the
publish moved out of `initializeUsed` into `g1gcPublishStickyRegions`
(g1gc.go), called after `g1gcEvacuate` (mgc.go mark termination). Recorder
and selector now observe the same bitmap for the whole window by
construction; newly low-live regions join one window later, matching the
original design intent.

### Fix 3 — the downstream nil-gp SIGSEGV was its own bug

With misses eliminated, `casgstatus(0x0)` in findRunnableGCWorker kept
reproducing (~2/6 runs) — independent of rewrite correctness. Root cause:
`gcBgMarkWorkerNode` was heap-allocated (`new(gcBgMarkWorkerNodePadded)`)
but referenced ONLY from non-scanned runtime structures (the lfstack worker
pool and `pp.nextGCMarkWorker`). The marker never sees those edges, so the
nodes' regions look low-live and get evacuated; ClearSourceBits zeroes the
old memory and `node.gp` reads 0. Fix (mgc.go): allocate the nodes from
`persistentalloc` with `tagAlign` — the alignment matters because lfstack
packing requires 512-byte-aligned nodes (first attempt with PtrSize
alignment tripped `taggedPointerPack` inside make.bash itself).

### Gates and results

- Build note: clean make.bash builds require
  `GOROOT_BOOTSTRAP=$PWD/toolchain/go-g1-1266-src`; the installed
  /usr/local/go (a 1.26.6 fork build) trips a version-stamp mismatch on
  full rebuilds that incremental builds masked.
- `just verify` green end-to-end (runtime gates, SSA, project, race,
  format) with that bootstrap.
- frag + `g1evac=4,g1trace=1`: **10/10 clean** (previously 6/6 threw on
  drift alone, plus bounded misses and the SIGSEGV); windows engage and
  copy productively (~68-108 source spans per 10s run).
- pointer64 / pointer256 / alloc + `g1evac=4,g1trace=1`: 3/3 each.
- Production config `g1evac=1` on frag: 5/5 clean; g1gc-demo OK.
- Labeled repeat `p0-fix-frag` (n=3 alternating, frag + `g1evac=1` vs
  official go1.27.0): tp median **1.035x** (min 1.008), stw_total 0.977,
  stw_p99 1.099, gc_cpu 1.007, heap_sys 0.28x — the first frag+evac runs
  that engage and beat upstream on throughput. Small n; re-measure at n=7
  in the next session's matrix.

### Next session

Resume Phase 1 (window pause floor: prewarmed destinations, copied-source
list, chunk presence bitmap, quickselect, gated generation stores) on top of
the restored correctness baseline, and re-run the full b1270-style matrix as
the reference for that work. Success bar unchanged.

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
  `iter-fix2-live128k-pointer64`, `p0b-*`, `b1270-*`, and `p0-fix-*` under
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
