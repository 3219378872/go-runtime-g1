# M01 模拟包实现索引

ID: M01。覆盖 D01（部分）、S01/S03。证据：E01#test-project, E03#unit。

根模拟包按单一职责拆分为一 concern 一文件（`doc.go` 为模块地图与锁纪律）。
锚点一律按 `文件#func` 引用，行号漂移只告警。

| 模块 | 文件 | 职责 | 关键符号 |
|---|---|---|---|
| 模块地图 | `doc.go` | 包文档/文件分工/锁纪律 | `Package g1gc` |
| 身份 | `id.go` | 句柄/阶段/起因 | `ObjectID, RegionID, Phase#String, Cause#String` |
| 错误 | `errors.go` | 哨兵错误 | `ErrInvalidConfig/ErrOutOfMemory/ErrEvacuationFailure/...` |
| 配置 | `config.go` | 堆配置与归一化 | `Config#normalized, DefaultConfig` |
| 对象模型 | `object.go` | 内部对象/只读描述/拷贝 | `object, ObjectInfo, cloneIDs` |
| 区域模型 | `region.go` | RegionKind/内部记账/快照类型 | `region#reset/memberIDs, RegionInfo` |
| 统计 | `stats.go` | 周期统计与深拷贝 | `Stats, cloneStats` |
| 堆核心 | `heap.go` | 状态组合/New/Close/寻址 | `Heap, New, Close, resolveLocked` |
| 分配 | `alloc.go` | 分配/空闲栈/active 缓存/记账 | `Allocate/AllocateObject/AllocateWithRefs, findNormalRegionLocked` |
| 分配池 | `pool.go` | 空闲池/active 缓存/used 记账（可单测） | `freePool#push/pop/claim, activeCache, allocator` |
| 引用 | `refs.go` | Roots/引用/存活/Pin | `AddRoot, SetReference, Resolve, ObjectInfo, Pin/Unpin` |
| 记忆集 | `rset.go` | 跨区引用计数索引 | `rsKey, rsAddEdgeForSlotLocked, rebuildRememberedSetsLocked` |
| 周期编排 | `cycle.go` | `Collect` 五阶段编排/收尾/中断 | `Collect, GC, finishCycleLocked, abortCycle` |
| 并发标记 | `mark.go` | SATB/队列/worker/起止 | `beginMarkingLocked, runConcurrentMark, finishMarkingLocked` |
| 清扫 | `sweep.go` | 不可达回收/区域释放 | `cleanupLocked` |
| 集合选择 | `cset.go` | CSet/暂停预估 | `selectCollectionSetLocked, pauseEstimate` |
| 疏散 | `evac.go` | 拷贝/转发/引用重写 | `allocateEvacuationCopyLocked, evacuateLocked, rewriteForwardedRefsLocked` |
| 策略 | `policy.go` | 占用/IHOP/MaybeCollect | `OccupancyPercent, ShouldStartCycle, MaybeCollect` |
| 快照 | `snapshot.go` | 只读查询 | `UsedBytes, RegionSnapshot, RememberedSet, RegionCount, ObjectCount` |
| 校验 | `validate.go` | 不变式诊断 | `(h *Heap) Validate` |
| 示例 | `cmd/g1gc-demo/main.go` | 最小可运行 | `main` 调 `New/DefaultConfig/Allocate/Collect` |

兼容层清理（2026-09-04 重构）：删除无调用者的别名
`Alloc/AllocObject/AllocWithRefs/SetRef/GetReference/NewHeap`，
删除死代码 `recordRememberedLocked/rebuildOneRegionLocked`、
`region#objectIDs/reset` 旧版（统一为原地复用的 `reset`），
`objectIDsUnsorted` 更名为 `memberIDs`。行为零改动，见符号覆盖审计。

证据：E01 `test-project`，E03 14 用例（`cycle/evac/rset/mark/policy/pool` 六个测试文件，见 E03）。

## 迁移来源

- 本页由根 `*.go` 导出符号盘点 + `README.md:3-19` 算法清单迁移；一致性修复（`bench/run.sh` CANDIDATE_ROOT、`types.go AfterUsedBytes`、`heap.go RegionCount` 锁、`go.mod` go 1.26）见 `NOTE.md:629-640`。
