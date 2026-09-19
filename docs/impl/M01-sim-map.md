# M01 模拟包实现索引

ID: M01。覆盖 D01（部分）、S01/S03。证据：[E01 项目测试](../evidence/E01-gates.md#test-project)、[E03 单元](../evidence/E03-correctness-stress.md#unit)。

根模拟包按单一职责拆分为一 concern 一文件（`doc.go` 为模块地图与锁纪律）。
源码位置按 `文件#符号` 引用；下面的章节锚点用于跨层追溯。

<a id="sim"></a>

## 模块地图

| 模块 | 文件 | 职责 | 关键符号 |
|---|---|---|---|
| 模块地图 | `doc.go` | 包文档/文件分工/锁纪律 | `Package g1gc` |
| 身份 | `id.go` | 句柄/阶段/起因 | `ObjectID, RegionID, Phase#String, Cause#String` |
| 错误 | `errors.go` | 哨兵错误 | `ErrInvalidConfig/ErrOutOfMemory/ErrEvacuationFailure/...` |
| 配置 | `config.go` | 堆配置与归一化 | `Config#normalized, DefaultConfig` |
| 对象模型 | `object.go` | 内部对象/只读描述/拷贝 | `object, ObjectInfo, cloneIDs` |
| 区域模型 | `region.go` | RegionKind/内部记账/快照类型 | `region#reset/memberIDs, RegionInfo, numRegionKinds` |
| 统计 | `stats.go` | 周期统计与深拷贝 | `Stats, cloneStats` |
| 堆核心 | `heap.go` | 状态组合/New/Close/寻址 | `Heap, New, Close, resolveLocked, compressForwardPathLocked, allocIDLocked` |
| 分配 | `alloc.go` | 分配/空闲栈/active 缓存/记账 | `Allocate/AllocateObject/AllocateWithRefs, allocateOnce, allocNormalLocked, allocHumongousLocked, findNormalRegionLocked` |
| 分配池 | `pool.go` | 空闲池/active 缓存/used 记账（可单测） | `freePool#push/pop/claim, activeCache, allocator` |
| 引用 | `refs.go` | Roots/引用/存活/Pin | `AddRoot, SetReference, Resolve, ObjectInfo, Pin/Unpin, canonicalizeRootsLocked` |
| 锁守卫 | `guards.go` | 读锁获取顺序 | `withReader, withReaderErr` |
| 记忆集 | `rset.go` | 跨区引用计数索引 | `rsKey, rsAddEdgeForSlotLocked, rebuildRememberedSetsLocked` |
| 周期编排 | `cycle.go` | `Collect` 五阶段编排/STW 门/收尾/中断 | `Collect, GC, stw, finishCycleLocked, abortCycle` |
| 标记状态机 | `marker.go` | epoch/队列/SATB/取消（可单测，不加锁） | `marker#begin/abort/finish/mark/push/popBatch, isCancelled/cancel/hasQueuedWork/isQuiescent/trackStart/trackDone` |
| 并发标记 | `mark.go` | worker 编排/同步/barrier（持锁经过 `h.mark`） | `beginMarkingLocked, runConcurrentMark, markObjectLocked, finishMarkingLocked, takeMarkBatch, publishMarkRefs, cancelMarkIfDone` |
| 清扫 | `sweep.go` | 不可达回收/区域释放 | `cleanupLocked` |
| 集合选择 | `cset.go` | CSet/暂停预估 | `selectCollectionSetLocked, pauseEstimate` |
| 疏散 | `evac.go` | 拷贝/转发/引用重写 | `allocateEvacuationCopyLocked, evacuateLocked, rewriteForwardedRefsLocked, copyCollectionSetLocked, reclaimCollectionSetLocked, evacTargetKind, rewriteObjectRefsLocked` |
| 策略 | `policy.go` | 占用/IHOP/MaybeCollect | `OccupancyPercent, ShouldStartCycle, MaybeCollect` |
| 快照 | `snapshot.go` | 只读查询 | `UsedBytes, RegionSnapshot, RememberedSet, RegionCount, ObjectCount, regionSnapshotLocked` |
| 校验 | `validate.go` | 不变式诊断 | `(h *Heap) Validate` |
| 示例 | `cmd/g1gc-demo/main.go` | 最小可运行 | `main` 调 `New/DefaultConfig/Allocate/Collect` |

兼容层清理（2026-09-04 重构）：删除无调用者的别名
`Alloc/AllocObject/AllocWithRefs/SetRef/GetReference/NewHeap`，
删除死代码 `recordRememberedLocked/rebuildOneRegionLocked`、
`region#objectIDs/reset` 旧版（统一为原地复用的 `reset`），
`objectIDsUnsorted` 更名为 `memberIDs`。行为零改动，见符号覆盖审计。

证据：E01 `test-project`，E03 21 用例（`cycle/evac/rset/mark/marker/policy/pool` 七个测试文件，见 E03）。

<a id="collect"></a>

## 周期编排

`cycle.go#Collect/GC` 编排五阶段，`cycleMu` 串行化整个调用；`stw` 为 remark/cleanup/evacuation 统一获取 `world` 与 `mu`。`marker.go` 不自行加锁，`mark.go` 负责 worker、条件变量和对象寻址。

证据：E03 的 `cycle_test.go`、`mark_test.go`、`marker_test.go`；执行门见 E01。

<a id="collect-select"></a>

## 集合选择与疏散

`cset.go#selectCollectionSetLocked/pauseEstimate` 按暂停预估选择区域；`evac.go#evacuateLocked` 编排复制、引用重写与源区域回收，失败区域保留 live 对象。

证据：E03 的 `policy_test.go`、`evac_test.go`；性能入口为 `bench_sim_test.go`。

<a id="validate"></a>

## 不变式校验

`validate.go#Validate` 检查对象、转发链、区域与分配记账一致性。

证据：E03 的周期/疏散等用例通过 `Validate` 检查结果。

## 迁移来源

- 当前地图取自根 `*.go` 与 `doc.go`。一致性修复历史见 `NOTE.md` 2026-08-21；当前模块拆分与内部组件抽取见 2026-09-04b/c/d。旧记录中的 `types.go` 内容现已分散到模型、统计和配置文件。
