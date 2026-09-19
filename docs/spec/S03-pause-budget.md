# S03 暂停预算契约

ID: S03。上游：I01, I03。下游：D04, D05。实现：[M01 集合选择](../impl/M01-sim-map.md#collect-select)、[M02 窗口](../impl/M02-fork-map.md#engagement)。证据：[E02](../evidence/E02-bench-matrix.md#matrix) 的 b1270/p1b/p2 历史矩阵。

## Given/When/Then

- Given Runtime 疏散窗口，When 选择源 span，Then 用每个候选的 `npages * pageSize` 扣减 256 KiB 预算，候选数组最多容纳 256 spans（`g1gc_evacuate.go#g1EvacuationCopyBudget/g1EvacCandidates`、`g1gc_evac_select.go#g1gcEvacuate`）。模拟包按 `cset.go#pauseEstimate` 估算暂停并选择 CSet。
- Given owner-span 投影集超 512 spans 或预计复制的 live 对象超 65,536，When 评估窗口，Then 整体 defer（`g1gc_evacuate.go#g1EvacuationMaxRewriteSpans/g1EvacuationMaxRewriteObject`）。
- Given inbound 索引溢出，When 疏散前检查，Then 整体 defer，不做无界全堆重写（`g1gc_evac_select.go#g1gcEvacuate`）。
- Given 窗口分配区（window-alloc regions），When STW 重写，Then 纳入覆盖；512 项 region list 溢出时设置 inbound overflow，进入保守 defer（`g1gc_inbound.go#g1gcNoteWindowAllocRegion`）。
- Given low-live region 集，When tagging，Then 最多收集 512 个 sticky 且无非栈根引用的候选 region，按可回收字节降序，累计达到 64 MiB 目标后停止 tagging；该目标不是复制预算（`g1gc_evac_select.go#g1gcTagLowLiveRegions`）。

## 当前频率界与历史来源

- 窗口分配量阈值 `max(2 GiB, 8 * heapLive)`，距上次窗口至少 32 个 GC cycles，并满足 D04 的自适应 arming（`g1gc_evac_select.go#g1gcEvacThreshold`、`g1gc_cycle.go#g1gcStartCycle`）。
- 历史阈值与预算调整见 `NOTE.md` 2026-08-21、2026-08-24、2026-08-24b/c；其中的旧数值和暂停测量只描述当时实现。

## 迁移来源

- 当前数值取自上述源码常量与选择逻辑；历史记录用于说明演化，不能覆盖当前代码。未证实的假设不作为行为契约。
