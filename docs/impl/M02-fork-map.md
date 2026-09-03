# M02 Fork 实现索引

ID: M02。覆盖 D01-D04。证据：E01#verify-runtime, E03#stress, E02#matrix。

## 改动清单（由 REBASE-1.27/1.26 迁移，17 + 2 新）

- 1.27 三方合并零冲突 10 个（`REBASE-1.27.md:30-46`）：`plive.go(+22/-1, NewBulk+stackmap 检查)`、`_builtin/runtime.go(+16/-15, *any→unsafe.Pointer)`、`builtin.go(+83/-84, 机械签名现代化)`、`extern.go(+4/-3, setuid 文档)`、`mbitmap.go(+3/-1)`、`mgc.go(+39/-25)`、`mgcmark.go(+4, xRegScan 块)`、`mgcmark_greenteagc.go(+1/-1)`、`mheap.go(+1/-9)`、`runtime1.go(+62/-16, GODEBUG infra 重构)`；新到文件 `preempt_xreg.go/preempt_noxreg.go` 走既有 hook（`REBASE-1.27.md:48-51`）。
- 逐字拷贝 7 个：`ssa/writebarrier.go`、`arena.go`、`atomic_pointer.go`、`mcache.go`、`mgcmark_nogreenteagc.go`、`mgcsweep.go`、`mwbbuf.go`。
- Fork-new 2 个：`src/runtime/g1gc.go`（35 func）、`src/runtime/g1gc_evacuate.go`（32 func），1.27 rebase 无源码改动直接带过（`REBASE-1.27.md:3-11`）。
- 1.26 背景（`REBASE-1.26.md:20-56`）：GreenTea 默认开、inline mark 双布局、scanObjectSmall 批路径同记 inbound、`writeBarrier.g1Evac` 占 offset 4 pad 槽、g1Edges 迁出 p struct 尾（8KB 数组致 cache 抖动，迁全局 per-P 表）、`g1EvacIndexActive` 改 plain uint32（STW 写、mark 热路径 plain 读，同 gcphase 协议）。

## 关键函数锚点（行号以 go1.27.0 fork 为准，漂移只告警）

- accounting: `g1gc.go:583 g1gcMarkRegionDirty`、`602 g1gcForEachWindowSpan`、`732 g1gcInitializeUsed`、`778 g1gcPublishStickyRegions`、`794 g1gcRefreshDirtyRegions`。
- inbound/sticky: `g1gc.go:366 g1gcResetInbound`、`470 g1gcRecordInbound`、`1058 g1gcBuildCollectionSet`。
- engagement: `g1gc_evacuate.go:68 g1gcEvacThreshold`、`99 g1gcNoteIdleWindow`、`109 g1gcCommitProductiveWindow`、`461 g1gcTagLowLiveRegions`、`516 g1gcEvacuate`。
- rewrite/roots: `g1gc_evacuate.go:139 g1gcDrainPendingWBSlots`、`860 g1gcVerifyFullRewrite`、`939 g1gcUpdateRoots`、`960 g1gcRescanStacks`、`1119 g1gcRewriteSpan`、`1134 g1gcClearSourceBits`。
- worker 别名坑：`mgc.go gcBgMarkWorkerNode` 须 `persistentalloc+tagAlign`（lfstack 512B 对齐），见 NOTE 2026-08-25 Fix 3。
- dense-refill: `mcentral.go:114 cacheSpan` partialSwept 首 pop 经 `g1gc.go:562 g1PreferDenseSpan`（`g1AllocChoiceSpans=8` 有界，`g1SpanAllocRank/g1AllocRankBetter` 三档），`debug.g1gc` 门控；见 D05，NOTE 2026-09-03b。

证据：E01 `build-toolchain/test-runtime/test-ssa`；E03 frag stress；E02 各 label 矩阵。
