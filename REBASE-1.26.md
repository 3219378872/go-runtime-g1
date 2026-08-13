# Rebase plan: G1 fork onto go1.26.1

Status: PORTED AND BUILDING (2026-08-13). The G1 hooks now also live in
`toolchain/go-g1-1261-src` (gitignored, based on go1.26.1 source). The
port builds with `GOROOT_BOOTSTRAP=/usr/local/go ./make.bash` and passes
the runtime/SSA/project/race gates. It adapts the hooks to Go 1.26's
default Green Tea GC: inline mark bits for small spans (g1gcCountLive
and g1gcClearSourceBits handle both layouts), inbound-edge recording in
the green scanObject/scanObjectSmall/scanObjectsSmall paths, and the
compiler writeBarrier struct gained `g1Evac uint32` at offset 4.

Key 1.26 facts learned during the port:
- Go 1.26's default runtime IS the Green Tea GC (GOEXPERIMENT
  greenteagc on by default; official binaries contain
  spanInlineMarkBits.init and scanObjectSmall).
- Mark bits for small spans live inline in the span until
  moveInlineMarks merges them into gcmarkBits during sweep.
- mgcmark.go is split into mgcmark.go (shared), mgcmark_greenteagc.go
  (green), and mgcmark_nogreenteagc.go (classic); the G1 hooks need
  per-build helpers (g1gcCountInlineMarks, g1gcClearInlineMarks).
- scanObjectSmall/scanObjectsSmall are the span-batched scan paths and
  need the same inbound recording as scanObject.
- Compiler slot metadata: the gated CFG variant (bCheck/bSlots/bContinue
  on writeBarrier.g1Evac) cost ~3% throughput on 1.26 codegen; the
  final port writes slot metadata unconditionally in the flush batch
  (no extra branch) and gates only the runtime-side processing.
- g1Edges sits at the END of the p struct (after wbBuf) to avoid
  shifting hot fields' cache layout.

Remaining measured gap vs official go1.26.1 (pointer64): mark-term p99
~100us vs ~65us, dominated by candidate-specific non-evac mark-term
stalls (~2x official) whose source is not yet isolated (compiler vs
runtime struct footprint); throughput ~0.97-0.98x. pointer256 and alloc
are at parity or better on all metrics.


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
