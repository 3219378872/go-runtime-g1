# Rebase plan: G1 fork onto go1.26.1

Status: scope assessment (2026-08-13). The fork is go1.25.0-based
(`toolchain/go-g1-src`). The objective compares against the official
go1.26.1 runtime; current measurements already meet or beat official
go1.26.1 on throughput, STW total, GC CPU, and overall STW p50-p999,
with the mark-term p99 tail (~94us vs ~60us) remaining. A rebase onto
go1.26.1 source would make the comparison base-version-clean.

## Upstream delta 1.25.0 -> 1.26.1 in fork-modified files

Measured with `diff` against go1.25.0/go1.26.1 source tarballs:

| file | upstream changed lines | notes |
|---|---|---|
| runtime/mgc.go | 459 | spanSPMC per-P span queues, goroutine leak detection, atomic markroot counters |
| runtime/mgcmark.go | 301 | span work queues, leak detection, marking internals |
| runtime/mheap.go | 237 | arena/span changes |
| cmd/compile/internal/typecheck/builtin.go | 669 | mechanical field renumbering |
| runtime/runtime1.go | 67 | debug vars |
| cmd/compile/internal/ssa/writebarrier.go | 56 | compiler barrier lowering |
| runtime/mbitmap.go | 48 | bulk barriers |
| typecheck/_builtin/runtime.go | 29 | writeBarrier struct mirror |
| runtime/arena.go | 20 | |
| runtime/extern.go | 16 | g1 debug docs |
| runtime/mgcsweep.go | 15 | |
| liveness/plive.go | 7 | wb decision-block analysis (fork has a custom rewrite) |
| mwbbuf.go | 2 | |
| atomic_pointer.go | 0 | |

## Fork-side change surface to port (17 files)

Runtime hooks: `g1gc.go`, `g1gc_evacuate.go` (new), `mgc.go`, `mgcmark.go`,
`mbitmap.go`, `mwbbuf.go`, `mheap.go`, `mgcsweep.go`, `arena.go`,
`atomic_pointer.go`, `extern.go`, `runtime1.go`, `runtime2.go` (p struct).
Compiler: `ssa/writebarrier.go`, `liveness/plive.go`,
`typecheck/_builtin/runtime.go`, `typecheck/builtin.go`,
`cmd/compile/internal/ir/symtab.go` (whitespace only).

## Highest-risk areas

1. `mgcmark.go` inbound-edge recording and rewrite hooks sit in the
   span-scanning path, which 1.26 replaced with spanSPMC per-P queues.
2. `mgc.go` gcStart/gcMarkTermination hook placement (cycle active flags,
   initialize/evacuate/finalize calls).
3. `writebarrier.go` slot-gating CFG (branch around metadata stores) must
   survive 1.26's lowered-barrier changes; `plive.go` custom wb decision
   walk needs the same adaptation.
4. `runtime2.go` p struct additions (g1Edges) and mspan fields.

## Suggested order

1. Port compiler files first (builtin.go mechanical, writebarrier.go,
   plive.go); build compiler only.
2. Port runtime data structures (mspan fields, p struct, wbBuf slots,
   debug vars).
3. Port mgc.go hooks, then mgcmark.go scan/rewrite hooks.
4. Port evacuation (g1gc*.go) and sweep integration.
5. Rebuild with a bootstrap that supports 1.26 (1.25.0 may work for the
   compiler bootstrap; verify `make.bash`).
6. Re-run all gates (TestUnsafePoint|TestGcSys, SSA Test, project, race)
   and the matched benchmark matrix vs official go1.26.1.
