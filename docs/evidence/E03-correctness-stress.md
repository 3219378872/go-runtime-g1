# E03 正确性证据

ID: E03。覆盖：[M01 校验](../impl/M01-sim-map.md#validate)、[M02 重写](../impl/M02-fork-map.md#evacuate-rewrite)、S01/S02。

<a id="unit"></a>

## 单元（21 用例，按领域拆分）

- `cycle_test.go`：`TestG1CycleKeepsReachableGraphAndReclaimsGarbage`、`TestCancelledCycleRestoresIdleState`、`TestAllocationFailureTriggersAutomaticCollection`。
- `evac_test.go`：`TestEvacuationUpdatesOldHandlesAndCrossRegionReferences`、`TestHumongousSpanIsSweptAndRootedSpanSurvives`、`TestPinnedObjectReportsEvacuationFailureButRemainsLive`。
- `rset_test.go`：`TestRememberedSetRetainsAllCrossRegionEdges` + 纯索引单测 `TestRSetIndexTracksPairTransitions`（`rsetIndex#add/remove/clear` 首零边跳变）。
- `mark_test.go`：`TestSATBPreservesPreMutationValueForOneCycle`。
- `marker_test.go`：`TestMarkerBeginBumpsEpochAndReusesBacking`、`TestMarkerEpochWrapClearsObjects`、`TestMarkerMarkIsStickyPerEpoch`、`TestMarkerPushReportsEmptyTransition`、`TestMarkerPopBatchIsBoundedLIFO`、`TestMarkerAbortInvalidatesAndCancels`、`TestMarkerFinishKeepsMarks`。
- `policy_test.go`：`TestPromotionAndIHOPPolicyAcrossCycles`、`TestPauseBudgetLimitsCollectionSet`。
- `pool_test.go`：`TestFreePoolPushPopClaim`（LIFO/排除探测/claim 幂等）、`TestActiveCacheSetClearGet`、`TestAllocatorTakeActiveAndUsed`。

跑：`just test-project` / `just test-race`（结果记 E01）。以上是当前源码的用例清单；历史通过记录见 `NOTE.md` 2026-09-04d，不表示本次文档校准重新运行了测试。

<a id="stress"></a>

## Fork stress 与历史结果

`bench/stress.sh` 默认 candidate-only、诊断模式 `gctrace=1,g1gc=1,g1evac=4,g1trace=1`；通过 `STRESS_GODEBUG` 可选择常规 `g1evac=1` 配置。以下为 2026-08-25 当次记录：

- frag + `g1evac=4,g1trace=1`：10/10 clean（见 `NOTE.md` 2026-08-25「Gates and results」）。
- pointer64/256/alloc + `g1evac=4,g1trace=1`：各 3/3。
- 生产 `g1evac=1` frag：5/5 clean；`g1gc-demo` OK。
- 故障串：`bad pointer/found pointer/rewrite missed/accounting drifted/SIGSEGV/invalid free/throw(`。

## NOTE 迭代链（历史档案索引）

2026-09-04b/c/d（模拟器拆分与组件单测，最后增至 21 用例）→ 2026-09-04（fork 九文件拆分及当次门禁/stress）→ 2026-09-03a/b/c（标准矩阵、dense-refill、同 session A/B）→ 2026-08-25e（RSS 证伪+纪律）→ 2026-08-25d（RSS 插桩+旧 0.28x 撤回）→ 2026-08-25c（归因+Rejected 门控）→ 2026-08-25b（engagement+比较器修复）→ 2026-08-25（三修复+caveat 解除）→ 2026-08-24e（1.27 rebase+b1270 基线）→ 2026-08-24d（1.26.6 rebase+WB 排空/窗口分配区+残留 miss）→ 2026-08-24c（root 拆分+stack 可疏散）→ 2026-08-24b（frag 负载+sticky 门+epoch 限频）→ 2026-08-24（频率/有界选择）→ 2026-08-23（增量会计）→ 2026-08-21（硬化+归因）。

`NOTE.md`「Verified」「Known Limits」保留历史门禁和限制；它们不是随当前提交自动更新的状态。新验证必须记录对应源码提交与执行条件。

## 迁移说明

- NOTE.md 保留为追加式原始日志档案；本层只收敛结论与证据指针，细节回查 NOTE 行号；新增迭代先记 NOTE，再收敛到 D/E 层。
