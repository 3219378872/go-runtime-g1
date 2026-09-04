# S01 回收周期契约

ID: S01。上游：I01, I02。下游：D01。实现：M01#collect, M02#cycle。证据：E01, E03。

## Given/When/Then

- Given 空闲堆（`State()==PhaseIdle`，`heap.go#State`），When `Collect(ctx, cause)`（`cycle.go#Collect`），Then 依次经历 initial-mark → concurrent-mark（可并发）→ remark → cleanup → evacuation（后三者 STW），`Completed=true`，`PauseDuration` 为四段 STW 之和（`cycle.go#Collect`）。
- Given `ctx` 已取消，Then 返回 `ErrContextCancelled` 且周期 abort 回 idle（`cycle.go#Collect/abortCycle`）。
- Given 已有周期在跑，Then 返回 `ErrCycleInProgress`（`cycle.go#Collect`）。
- Given `GC()`，Then 等价 `Collect(Background, CauseExplicit)`（`cycle.go#GC`）。

## 不变式（由 validate.go + NOTE 迁移）

- `Validate() error`（`validate.go`）守 Free/Humongous-span/used-capacity 一致性。
- 模拟包 promotion/IHOP/SATB/pause-budget 行为由 14 用例锁定（`cycle/evac/rset/mark/policy/pool` 测试文件，`TestPromotionAndIHOPPolicyAcrossCycles`、`TestSATBPreservesPreMutationValueForOneCycle`、`TestPauseBudgetLimitsCollectionSet` 等），结果记 E03。

## 迁移来源

- 周期编排由 `cycle.go#Collect/GC` 提取；`README.md:3-13` 阶段清单为意图侧描述，本文件为可测契约。
