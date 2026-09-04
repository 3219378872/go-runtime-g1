# M02 Fork 实现索引

ID: M02。覆盖 D01-D04。证据：E01#verify-runtime, E03#stress, E02#matrix。

## 改动清单（由 REBASE-1.27/1.26 迁移，17 + 2 新）

- 1.27 三方合并零冲突 10 个（`REBASE-1.27.md:30-46`）：`plive.go(+22/-1, NewBulk+stackmap 检查)`、`_builtin/runtime.go(+16/-15, *any→unsafe.Pointer)`、`builtin.go(+83/-84, 机械签名现代化)`、`extern.go(+4/-3, setuid 文档)`、`mbitmap.go(+3/-1)`、`mgc.go(+39/-25)`、`mgcmark.go(+4, xRegScan 块)`、`mgcmark_greenteagc.go(+1/-1)`、`mheap.go(+1/-9)`、`runtime1.go(+62/-16, GODEBUG infra 重构)`；新到文件 `preempt_xreg.go/preempt_noxreg.go` 走既有 hook（`REBASE-1.27.md:48-51`）。
- 逐字拷贝 7 个：`ssa/writebarrier.go`、`arena.go`、`atomic_pointer.go`、`mcache.go`、`mgcmark_nogreenteagc.go`、`mgcsweep.go`、`mwbbuf.go`。
- Fork-new 9 个（2026-09-04 纯搬运拆分，72 func 零增减，见 NOTE 2026-09-04；1.27 rebase 无源码改动直接带过（`REBASE-1.27.md:3-11`））：`g1gc.go`（类型/全局表+`BuildCollectionSet/NextCandidate/Trace`，5 func）、`g1gc_cycle.go`（周期开关，5）、`g1gc_inbound.go`（inbound 索引+WB 排空，12）、`g1gc_account.go`（脏区会计+live 计数，16）、`g1gc_alloc_rank.go`（D05 密区排名，3）、`g1gc_evacuate.go`（仅常量/全局表，0）、`g1gc_evac_select.go`（选择+窗口驱动，8）、`g1gc_evac_copy.go`（拷贝+终结，9）、`g1gc_evac_rewrite.go`（重写+重扫+校验，14）。锚点一律按 `文件#func` 引用，行号漂移只告警。
- 1.26 背景（`REBASE-1.26.md:20-56`）：GreenTea 默认开、inline mark 双布局、scanObjectSmall 批路径同记 inbound、`writeBarrier.g1Evac` 占 offset 4 pad 槽、g1Edges 迁出 p struct 尾（8KB 数组致 cache 抖动，迁全局 per-P 表）、`g1EvacIndexActive` 改 plain uint32（STW 写、mark 热路径 plain 读，同 gcphase 协议）。

## 关键函数锚点（行号以 go1.27.0 fork 为准，漂移只告警）

- accounting: `g1gc_account.go#g1gcMarkRegionDirty/g1gcForEachWindowSpan/g1gcInitializeUsed/g1gcPublishStickyRegions/g1gcRefreshDirtyRegions`。
- inbound/sticky: `g1gc_inbound.go#g1gcResetInbound/g1gcRecordInbound/g1gcDrainPendingWBSlots`、`g1gc.go#g1gcBuildCollectionSet`。
- engagement: `g1gc_evac_select.go#g1gcEvacThreshold/g1gcNoteIdleWindow/g1gcCommitProductiveWindow/g1gcTagLowLiveRegions/g1gcEvacuate`。
- rewrite/roots: `g1gc_evac_rewrite.go#g1gcVerifyFullRewrite/g1gcUpdateRoots/g1gcRescanStacks/g1gcRewriteSpan`、`g1gc_evac_copy.go#g1gcClearSourceBits/g1gcFinalizeEvacuation`。
- worker 别名坑：`mgc.go gcBgMarkWorkerNode` 须 `persistentalloc+tagAlign`（lfstack 512B 对齐），见 NOTE 2026-08-25 Fix 3。
- dense-refill: `mcentral.go cacheSpan` partialSwept 首 pop 经 `g1gc_alloc_rank.go#g1PreferDenseSpan`（`g1AllocChoiceSpans=8` 有界，`g1SpanAllocRank/g1AllocRankBetter` 三档），`debug.g1gc` 门控；见 D05，NOTE 2026-09-03b。

证据：E01 `build-toolchain/test-runtime/test-ssa`；E03 frag stress；E02 各 label 矩阵。
