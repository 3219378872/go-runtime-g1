# S01 回收周期契约

ID: S01。上游：I01, I02。下游：D01。实现：[M01 周期](../impl/M01-sim-map.md#collect)、[M02 周期](../impl/M02-fork-map.md#cycle)。证据：E01, E03。

## 模拟器 Given/When/Then

- Given 空闲堆（`State()==PhaseIdle`，`heap.go#State`），When `Collect(ctx, cause)`（`cycle.go#Collect`），Then 依次经历 initial-mark → concurrent-mark（可并发）→ remark → cleanup → evacuation（后三者 STW），`Completed=true`，`PauseDuration` 为四段 STW 之和（`cycle.go#Collect`）。
- Given `ctx` 已取消，Then 返回 `ErrContextCancelled` 且周期 abort 回 idle（`cycle.go#Collect/abortCycle`）。
- Given 已有 `Collect` 调用在跑，When 再调用 `Collect`，Then 先等待 `cycleMu`，取得锁后再检查上下文和堆状态；`ErrCycleInProgress` 是此时发现非 idle 状态的防御检查，普通并发调用不会立即返回该错误（`cycle.go#Collect`）。
- Given `GC()`，Then 等价 `Collect(Background, CauseExplicit)`（`cycle.go#GC`）。

## 不变式（由 validate.go + NOTE 迁移）

- `Validate() error`（`validate.go`）守 Free/Humongous-span/used-capacity 一致性。
- 模拟包有 21 个用例（`cycle/evac/rset/mark/marker/policy/pool` 七个测试文件），覆盖 promotion/IHOP/SATB/pause-budget、分配池及标记状态机；用例清单与历史结果见 E03，新运行结果另记 E01。

## Runtime fork 接入

- fork 沿用 Go 的 GC 周期，在 `mgc.go#gcStart` 按条件调用 `g1gcStartCycle`；在 `gcMarkTermination` 中刷新区域会计、执行疏散、发布下一窗口 sticky 快照，再经 `gcSweep` 和 `g1gcFinalizeEvacuation/g1gcEndCycle` 收尾。
- 上述 `Heap.Collect` 五阶段 API 契约仅适用于模拟器；fork 的窗口激活和暂停约束见 S03、M02。

## 迁移来源

- 模拟器契约取自 `cycle.go#Collect/GC`；Runtime 接入取自 `mgc.go#gcStart/gcMarkTermination` 和 `g1gc_cycle.go`。
