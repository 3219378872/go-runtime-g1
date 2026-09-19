# D01 增量 used-region 会计

ID: D01。上游：S01。实现：[M02 accounting](../impl/M02-fork-map.md#accounting)。证据：[E03 stress](../evidence/E03-correctness-stress.md#stress)、[E02 p0-fix-frag](../evidence/E02-bench-matrix.md#p0-fix-frag)。

## 设计

- 常规 mcache 分配批次与 sweep hook 标记 dirty（per-region flag + 有界 dirty list），mark termination 对 dirty regions 重计。直接 span 分配另有 `g1gcRecordSpanAllocation` 记账路径。
- 逻辑 region 为 1 MiB（`g1gc.go#g1RegionShift = 20`），区域表含 65,536 项（`g1RegionCount = 1 << 16`）；`g1gcRegionIndex` 将 span 基址右移后按表大小取模。大 span 整体记在基址所在 region，统计粒度仍为 span。
- Span 枚举直读页分配器 `pallocBits`（`g1gcForEachWindowSpan`）；先遍历 `mheap_.pages.inUse` 的 mapped chunks，再处理 dirty region 对应的地址窗口，先累加到 scratch 再 reconcile，以合并哈希碰撞窗口。枚举成本仍包含 mapped-chunk 扫描。
- 疏散窗口开始时 `g1gcResetLiveCounts` 重置增量 live 计数，灰对象标记路径通过 `g1gcRecordLiveObject` 记账。`gcmarknewobject` 对窗口内新分配对象染黑，但刻意不计入 region live 总量；候选 span 在复制前由 `g1gcCountLive` 精确读取 mark bits，包含这些新对象。

## 拒绝/已知坑（由 NOTE 迁移）

- cached-span 快分配曾绕过 hook（mcache fast path 多周期无 refill，`NOTE.md` 2026-08-25 Fix 1）：复现 `region N used-bytes undercount auth=X got=X-6048`（= size-class-288 单 span 21×288）；`g1evac>=5` allspans/windowscan 双枚举一致，证枚举无错、账本 stale。修复 `malloc.go nextFree` 慢路径 + `mcache.go refill` 按 allocCache batch（~64 allocs）补 dirty，hook 仍 gate 于 `debug.g1gc`。
- 增量 live 会计的引入史见 `NOTE.md` 2026-08-21；当前 allocate-black 处理以 `mgcmark.go#gcmarknewobject` 为准。窗口周期 gate 于 `g1EvacIndexActive`；非窗口会计刷新使用精确 census 路径。
- chunk-presence bitmap 为后续优化（`NOTE.md` 2026-08-23）；当前实现未采用该优化。
- bootstrap 首个 accounting 使能周期走 allspans 全量覆盖 GODEBUG 解析前创建的 span（`g1gc_account.go#g1gcBootstrapUsed`；背景见 `NOTE.md` 2026-08-23）。

## 历史暂停归因（2026-08-23，不代表当前测量）

- 三 sweep 合一 collect 单 pass ~50us → ~27us；`_Gscan` 轻协议替代全 suspend/resume；窗口总附加 ~35us（select 27 + rewrite 4 + copy 0 + init 0），此前 ~170us（`NOTE.md` 2026-08-23）。
- 当次 5-run 中值：stw_max 2.33 → 0.98，p99 1.53 → 0.99，gc_cpu 1.03 → 0.97..1.02；测量条件与局限回查该迭代和 E02。

## 来源

- 当前实现：`g1gc.go`、`g1gc_account.go`、`mgcmark.go`。历史：`NOTE.md` 2026-08-21、2026-08-23、2026-08-25 Fix 1；迁移过程见 `REBASE-1.27.md`。
