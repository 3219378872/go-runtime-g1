# M02 Fork 实现索引

ID: M02。覆盖 D01-D05。证据：[E01 Runtime 门](../evidence/E01-gates.md#verify-runtime)、[E03 stress](../evidence/E03-correctness-stress.md#stress)、[E02 矩阵](../evidence/E02-bench-matrix.md#matrix)。

当前 fork 为 `toolchain/go-g1-1270-src`（`VERSION` 为 go1.27.0）。下文 Runtime 文件相对其 `src/runtime/`；源码按 `文件#符号` 定位。

<a id="fork"></a>

## 当前模块与接入点

| G1 核心文件 | 职责 |
|---|---|
| `g1gc.go` | 类型、全局表、`g1gcBuildCollectionSet/g1gcNextCandidate/g1gcTrace` |
| `g1gc_cycle.go` | 周期启停、功能激活、统计收尾 |
| `g1gc_account.go` | dirty/used/live 会计、页分配器枚举、sticky 发布 |
| `g1gc_inbound.go` | inbound 索引、窗口分配区、WB 槽位排空 |
| `g1gc_alloc_rank.go` | D05 密区优先 refill 排名 |
| `g1gc_evacuate.go` | 疏散常量和全局状态 |
| `g1gc_evac_select.go` | 窗口条件、候选选择、疏散主流程 |
| `g1gc_evac_copy.go` | pin 排除、目标分配、复制、源清理与收尾 |
| `g1gc_evac_rewrite.go` | 指针转发、堆/根重写、栈重扫、诊断校验 |

上游文件接入面：`mgc.go` 管周期，`mgcmark.go`、`mgcmark_greenteagc.go`、`mgcmark_nogreenteagc.go` 管标记与扫描；`malloc.go`、`mcache.go`、`mcentral.go` 管分配；`mheap.go`、`arena.go`、`mgcsweep.go` 管 span 状态与回收；`mwbbuf.go`、`mbitmap.go`、`atomic_pointer.go` 管写屏障槽位；`runtime1.go`、`extern.go` 管 GODEBUG 配置与说明。

编译器接入位于 fork 的 `src/cmd/compile/internal/ssa/writebarrier.go`、`src/cmd/compile/internal/liveness/plive.go`，Runtime 声明及生成表位于 `src/cmd/compile/internal/typecheck/_builtin/runtime.go`、`src/cmd/compile/internal/typecheck/builtin.go`。

证据：E01 的构建、定向 Runtime/SSA 门，E03 的压力测试；历史模块拆分见 `NOTE.md` 2026-09-04。

<a id="cycle"></a>

## 周期

`mgc.go#gcStart/gcMarkTermination` 接入 `g1gc_cycle.go#g1gcCycleActive/g1gcStartCycle/g1gcEndCycle`。窗口标志由 `g1gcSetEvacIndexActive` 同时发布到 Runtime 和编译器生成代码读取的 `writeBarrier.g1Evac`；STW 写、并发标记路径读。

证据：E01 `test-runtime/test-ssa`，E03 stress。

<a id="accounting"></a>

## 区域会计

`g1gc_account.go#g1gcMarkRegionDirty/g1gcForEachWindowSpan/g1gcInitializeUsed/g1gcRefreshDirtyRegions` 对应 D01。1 MiB 地址窗口、65,536 项哈希表的常量在 `g1gc.go`；窗口中新分配对象的染黑与 live 记账边界见 `mgcmark.go#gcmarknewobject`。

证据：E03 诊断 stress 的 `g1gcValidateIncremental`，E02 p0-fix-frag 的历史修复记录。

<a id="sticky"></a>

## 入边与 sticky 快照

`g1gc_inbound.go#g1gcResetInbound/g1gcRecordInbound/g1gcDrainPendingWBSlots` 维护入边；`g1gc_account.go#g1gcPublishStickyRegions` 必须在 `g1gcEvacuate` 后执行，供下一窗口使用，见 D02。

证据：E03 frag stress，E02 p0-fix-frag；历史根因见 `NOTE.md` 2026-08-25 Fix 2。

<a id="engagement"></a>

## 窗口与选择

`g1gc_evac_select.go#g1gcEvacThreshold/g1gcNoteIdleWindow/g1gcCommitProductiveWindow/g1gcTagLowLiveRegions/g1gcEvacuate` 对应 D04。当前预算与上限见 S03 和 `g1gc_evacuate.go`，不能以旧迭代数值替代。

证据：E02 的 b1270/p1b/p2/p2s 历史矩阵，E03 stress。

<a id="roots"></a>
<a id="evacuate-rewrite"></a>

## 根排除、复制与引用重写

`g1gc.go#g1GlobalRootRegions` 配合选择排除非栈根目标；实际复制后由 `g1gc_evac_rewrite.go#g1gcRescanStacks/g1gcRewriteSpan` 修正引用，诊断模式调用 `g1gcUpdateRoots/g1gcVerifyFullRewrite`。`g1gc_evac_copy.go#g1gcClearSourceBits/g1gcFinalizeEvacuation` 负责源状态清理和目标 span 发布，见 D03/S02。

worker 节点存储的历史修复位于 `mgc.go` 的 `persistentalloc+tagAlign` 路径，见 `NOTE.md` 2026-08-25 Fix 3。

证据：E03 frag stress，E02 b1270 的历史矩阵。

<a id="dense-refill"></a>

## 密区优先 refill

`mcentral.go#cacheSpan` 的 partialSwept 首 pop 经 `g1gc_alloc_rank.go#g1PreferDenseSpan`，最多检查 8 个 span；`g1SpanAllocRank/g1AllocRankBetter` 做三档排名，`debug.g1gc` 门控，见 D05。

证据：E02 ra1 和 ab/abr，`NOTE.md` 2026-09-03b/c；这些历史测量不表示当前提交已通过性能门。

## 迁移档案

`REBASE-1.27.md` 的“17 个改动文件 + 2 个新文件”及 diff 统计描述 2026-08-24e 的迁移现场。此后 D05 增加分配接入，2026-09-04 将两个核心文件拆成上述九个；不能把迁移清单当成当前源码地图。

`REBASE-1.26.md` 保留 GreenTea、inline mark 双布局、写屏障槽位和 per-P 入边表适配过程。后续实现与拆分以当前源码和上述索引为准。

证据：E01/E03 的历史执行记录回查对应 `NOTE.md` 迭代；当前门禁需重新运行。
