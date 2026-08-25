# Rebase plan: G1 fork onto go1.27.0

Status: PORTED AND BUILDING (2026-08-24). The active fork is
`toolchain/go-g1-1270-src` (go1.27.0). The same overlay + three-way-merge
procedure from the 1.26 rebase applied cleanly: all ten drifted files merged
with zero conflicts, seven verbatim-compatible files copied as-is, and the
two fork-new runtime files (`g1gc.go`, `g1gc_evacuate.go`) carried over
without source edits — the first rebase that needed no manual hook fixes.
Gates green: TestUnsafePoint/TestGcSys, SSA tests, project tests, race
tests, g1gc-demo, pointer64 `g1evac=1` smoke (12 productive windows), frag
stress behavior identical to the go1.26.6 fork (see correctness note).

## Procedure

1. Pristine trees: go1.26.6 and go1.27.0 source tarballs; official go1.27.0
   binary installed at `toolchain/official-go-1270/` as the benchmark
   comparator (gitignored).
2. Fork-modified file list derived by diffing the whole `src/` of pristine
   go1.26.6 against the go-g1-1266-src fork: 17 files.
3. Files with zero upstream drift 1.26.6 -> 1.27.0 were copied verbatim from
   the fork: ssa/writebarrier.go, arena.go, atomic_pointer.go, mcache.go,
   mgcmark_nogreenteagc.go, mgcsweep.go, mwbbuf.go (+ g1gc.go,
   g1gc_evacuate.go).
4. Drifted files three-way merged with `git merge-file`
   (base=pristine 1.26.6, ours=pristine 1.27.0, theirs=fork): plive.go,
   _builtin/runtime.go, builtin.go, extern.go, mbitmap.go, mgc.go,
   mgcmark.go, mgcmark_greenteagc.go, mheap.go, runtime1.go — zero
   conflicts.

## Upstream delta 1.26.6 -> 1.27.0 in fork-modified files

Measured with `diff -u` added/removed line counts:

| file | + / - | notes |
|---|---|---|
| cmd/compile/internal/liveness/plive.go | 22 / 1 | NewBulk API change, stackmap overflow checks; away from the fork's wb walk |
| runtime/runtime1.go | 62 / 16 | GODEBUG infra rework (internal/godebugs), asynctimerchan removal; dbgvars table changed around the g1 entries |
| cmd/compile/internal/typecheck/builtin.go | 83 / 84 | mechanical builtin signature modernization |
| runtime/mgc.go | 39 / 25 | RuntimeSecret signal loop replaced by eraseSecretsSignalStk, endCycle loses userForced param, goroutine leak detection main-select handling; all regions distinct from the g1 hooks |
| cmd/compile/internal/typecheck/_builtin/runtime.go | 16 / 15 | `*any` -> `unsafe.Pointer` in builtin decls; writeBarrier struct region untouched |
| runtime/mgcmark.go | 4 / 0 | new additive xRegScan block in scanstack (conservative scan of extended register state at async safe points) |
| runtime/mheap.go | 1 / 9 | mspan.layout() removed (unused by fork), mProf_Free lost its size param |
| runtime/mbitmap.go | 3 / 1 | writeHeapBitsSmall scanSize computation reorder |
| runtime/extern.go | 4 / 3 | setuid doc rewrite only |
| runtime/mgcmark_greenteagc.go | 1 / 1 | comment typo fix |
| ssa/writebarrier.go, arena.go, atomic_pointer.go, mcache.go, mgcmark_nogreenteagc.go, mgcsweep.go, mwbbuf.go | 0 / 0 | verbatim |

New upstream files that arrive with the tree: preempt_xreg.go /
preempt_noxreg.go (async-preemption extended-register spill/scan). The
xRegScan root path routes through the existing scanblock/scanConservative
recording hooks unchanged.

Compiler surface check: writeBarrier still has the same four-field layout
(enabled/pad/cgo/alignme); the fork's `g1Evac uint32` slot-in-pad port is
unchanged from 1.26.

## Gates

- make.bash bootstraps fine with go1.26.6 (GOROOT_BOOTSTRAP=/usr/local/go).
- `just verify-runtime`, `test-project`, `test-race`, `check-format`: green.
- pointer64 `g1evac=1,g1trace=1` smoke vs official go1.27.0: 12 productive
  evacuation windows, no bad-pointer faults, no missed-rewrite diagnostics.
- frag stress A/B against the old fork (same binaries, GOMAXPROCS=2):
  identical pre-existing behavior on both forks — see below. Nothing in the
  frag failure set is a 1.27-port regression.

## Correctness carry-over (pre-existing, both forks)

- `frag` + `g1evac>=4` with per-cycle used-region validation
  (`g1trace=1`): "G1 incremental used-region accounting drifted" throws on
  go1.26.6 AND go1.27.0 forks alike (6/6 runs each) — the incremental
  ledger undercounts a region early on frag before any evacuation window;
  this validation combination was not part of previous gates.
- `frag` + `g1evac=4` without g1trace: bounded rewrite misses ("rewrite
  missed N heap slots", observed 2-67 slots) and one downstream SIGSEGV
  (nil gp through casgstatus in findRunnableGCWorker) reproduce on both
  forks. This is the documented open rewrite-miss issue from iteration
  2026-08-24d, unchanged by the rebase.
- pointer64 `g1evac=4,g1trace=1`: clean 3/3 on both forks.

Root-causing these remains deferred (next session), per plan.

## Baseline matrix after rebase

See NOTE.md iteration 2026-08-24e for the n=7 alternating medians versus
official go1.27.0 (labels b1270-*).
