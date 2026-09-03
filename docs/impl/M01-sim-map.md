# M01 模拟包实现索引

ID: M01。覆盖 D01（部分）、S01/S03。证据：E01#test-project, E03#unit。

| 模块 | 文件 | 职责 | 关键符号 |
|---|---|---|---|
| 类型/周期 | `types.go:441` | `Collect` 五阶段编排 | `Heap, Config:91, Stats:195, Collect:441, GC:557` |
| 变异门面 | `heap.go` | 分配/Roots/引用/RSet/快照 | `Allocate, AddRoot, SetReference, Resolve, RegionSnapshot` 等 24 个 `(*Heap)` 方法 |
| 并发标记 | `mark.go` | SATB/队列/worker/起止 | `beginMarkingLocked, runConcurrentMark, finishMarkingLocked, abortCycle` |
| 回收选择 | `collect.go:305行` | CSet/暂停预估/疏散/收尾 | `selectCollectionSetLocked, pauseEstimate, evacuateLocked, cleanupLocked, finishCycleLocked` |
| 策略 | `policy.go:67行` | 占用/IHOP/MaybeCollect | `OccupancyPercent, ShouldStartCycle, MaybeCollect` |
| 区域模型 | `region.go:66行` | RegionKind/内部记账 | `RegionFree/Eden/Survivor/Old/HumongousStart/Continue` |
| 校验 | `validate.go:138行` | 不变式诊断 | `(h *Heap) Validate` |
| 示例 | `cmd/g1gc-demo/main.go:58行` | 最小可运行 | `main` 调 `New/DefaultConfig/Allocate/Collect` |

证据：E01 `test-project`，E03 10 用例（`g1gc_test.go:521行`）。

## 迁移来源

- 本页由根 `*.go` 导出符号盘点 + `README.md:3-19` 算法清单迁移；一致性修复（`bench/run.sh` CANDIDATE_ROOT、`types.go AfterUsedBytes`、`heap.go RegionCount` 锁、`go.mod` go 1.26）见 `NOTE.md:629-640`。
