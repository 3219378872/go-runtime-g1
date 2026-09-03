# S01 回收周期契约

ID: S01。上游：I01, I02。下游：D01。实现：M01#collect, M02#cycle。证据：E01, E03。

## Given/When/Then

- Given 空闲堆（`State()==PhaseIdle`，`types.go:399`），When `Collect(ctx, cause)`（`types.go:441`），Then 依次经历 initial-mark → concurrent-mark（可并发）→ remark → cleanup → evacuation（后三者 STW），`Completed=true`，`PauseDuration` 为四段 STW 之和（`types.go:542`）。
- Given `ctx` 已取消，Then 返回 `ErrContextCancelled` 且周期 abort 回 idle（`types.go:488-496`）。
- Given 已有周期在跑，Then 返回 `ErrCycleInProgress`（`types.go:459-463`）。
- Given `GC()`，Then 等价 `Collect(Background, CauseExplicit)`（`types.go:557-559`）。

## 不变式（由 validate.go + NOTE 迁移）

- `Validate() error`（`validate.go`）守 Free/Humongous-span/used-capacity 一致性。
- 模拟包 promotion/IHOP/SATB/pause-budget 行为由 `g1gc_test.go` 10 用例锁定（`TestPromotionAndIHOPPolicyAcrossCycles`、`TestSATBPreservesPreMutationValueForOneCycle`、`TestPauseBudgetLimitsCollectionSet` 等），结果记 E03。

## 迁移来源

- 周期编排由 `types.go:441-559` 提取；`README.md:3-13` 阶段清单为意图侧描述，本文件为可测契约。
