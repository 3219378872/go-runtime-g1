# D03 根排除拆分（stack 可疏散）

ID: D03。上游：S02。实现：[M02 roots](../impl/M02-fork-map.md#roots)。证据：[E02 b1270](../evidence/E02-bench-matrix.md#b1270) 的 pointer64-evac 行。

## 设计

- `g1GlobalRootRegions` 记录非栈根目标（globals/finalizer/specials 等）；`scanblock` 仅在 `stk == nil` 时登记，`scanConservative` 不再登记栈派生目标。选择排除这些 region。
- 发生实际复制后，在 STW 中执行 `g1gcRescanStacks`（`_Gscan` 协议），重写指向已搬对象的栈槽；常规模式跳过非栈根重扫，`g1evac>=4` 时额外重扫自检（若发生转发则 throw）。
- `g1gcTagLowLiveRegions` 按可回收字节降序 tagging，当前累计目标为 64 MiB，最多收集 512 个候选 region；实际源 span 仍受 S03 的 256 KiB 预算限制。noscan 源 span 可参与疏散，其对象体无需指针重写。

## 边界与历史问题

- 非栈根引用覆盖大量 region 时，可疏散候选仍可能很少。`NOTE.md` 2026-08-24b 中“全栈 root 导致 sticky 交集为空”属于拆分前的历史边界；当前栈目标可由重扫修正。
- `NOTE.md` 2026-08-24c 曾记录约 1ms 窗口固定成本，并提出 pre-warmed dest、span 采样等方向；2026-08-25b 的重新归因已将这些方案 defer，详见 D04，不能继续作为当前待办或固定成本结论。
- 配套：worker 节点堆分配曾致 nil-gp SIGSEGV（`new(gcBgMarkWorkerNodePadded)` 仅 lfstack/pp 引用致 marker 不可见，疏散后清零旧内存）；修复 `persistentalloc + tagAlign`（lfstack 需 512B 对齐，PtrSize 对齐曾触发 `taggedPointerPack`），见 `NOTE.md` 2026-08-25 Fix 3。

## 历史度量（2026-08-24c）

- 当时记录 pointer64 evac 同窗 tp 1.021 / stw_max 0.889；frag 首次 engage（82 spans / 546 KB，stack rescan forward 零，经堆内 roots slice 可达）。该记录早于 2026-08-25b 的 official 比较器修复，不能用这些 ratio 宣称当前优于上游。

## 来源

- 当前实现：`g1gc.go#g1GlobalRootRegions`、`g1gc_evac_select.go#g1gcTagLowLiveRegions/g1gcEvacuate`、`g1gc_evac_rewrite.go#g1gcRescanStacks`。历史：`NOTE.md` 2026-08-24b/c、2026-08-25 Fix 3、2026-08-25b。
