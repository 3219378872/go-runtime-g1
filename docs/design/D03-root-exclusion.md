# D03 根排除拆分（stack 可疏散）

ID: D03。上游：S02。实现：M02#roots。证据：E02#b1270-pointer64-evac。

## 设计

- `g1RootRegions` 只记 `stk == nil` 的 root（globals/finalizer/specials）；`scanConservative` 去 hook（全栈派生）。
- 暂停期 `g1gcRescanStacks` 无条件跑（`_Gscan` 轻协议，~36us），重写指向已搬对象的栈槽；production 跳过 global rescan，`g1evac=4` 下全量 rescan 自证（forward 必须为零否则 throw）。
- 选择按可回收字节贪心降序，直至 dead 覆盖 4 MiB；noscan 源 span 重新eligible（零重写成本）。

## 边界（由 NOTE 迁移）

- 全栈引用 live-set 的堆（如旧 frag）在该设计下覆盖率低：root 槽引用多数 region 时 sticky 交集为空，疏散拒 engage（`NOTE.md:493-501`）；此为设计边界非回归，后续需 per-G RSet。
- frag 尾延迟边界：400 MB churn 堆上窗口固定成本 ~1ms（selection over top-reclaim + dest alloc + owner rewrite），与 256 KiB 预算无关；下步应做廉价窗口机制（pre-warmed dest、span 采样 selection）而非缩预算（`NOTE.md:469-474`）。
- 配套：worker 节点堆分配曾致 nil-gp SIGSEGV（`new(gcBgMarkWorkerNodePadded)` 仅 lfstack/pp 引用致 marker 不可见，疏散后 `ClearSourceBits` 清零旧内存）；修复 `persistentalloc + tagAlign`（lfstack 需 512B 对齐，PtrSize 对齐曾触发 `taggedPointerPack`）（`NOTE.md:254-265` Fix 3）。

## 度量（由 NOTE 迁移）

- pointer64 evac 同窗 tp 1.021 / stw_max 0.889（超 fork 自身 default 0.986）；frag 首次 engage（82 spans / 546 KB，stack rescan forward 零，经堆内 roots slice 可达）（`NOTE.md:460-468`）。

## 来源

- `NOTE.md` 2026-08-24c 全节（`NOTE.md:435-474`）+ 2026-08-25 Fix 3（`NOTE.md:254-265`）。
