# E03 正确性证据

ID: E03。覆盖：M01#validate, M02#evacuate-rewrite, S02。

## 单元（`g1gc_test.go:521行`，10 用例）

`TestG1CycleKeepsReachableGraphAndReclaimsGarbage`、`TestEvacuationUpdatesOldHandlesAndCrossRegionReferences`、`TestRememberedSetRetainsAllCrossRegionEdges`、`TestHumongousSpanIsSweptAndRootedSpanSurvives`、`TestPinnedObjectReportsEvacuationFailureButRemainsLive`、`TestCancelledCycleRestoresIdleState`、`TestPromotionAndIHOPPolicyAcrossCycles`、`TestAllocationFailureTriggersAutomaticCollection`、`TestSATBPreservesPreMutationValueForOneCycle`、`TestPauseBudgetLimitsCollectionSet`。

跑：`just test-project` / `just test-race`（结果记 E01）。

## Fork stress（`bench/stress.sh`）

- frag + `g1evac=4,g1trace=1`：10/10 clean（2026-08-25，见 `NOTE.md:271-279`）。
- pointer64/256/alloc + `g1evac=4,g1trace=1`：各 3/3。
- 生产 `g1evac=1` frag：5/5 clean；`g1gc-demo` OK。
- 故障串：`bad pointer/found pointer/rewrite missed/accounting drifted/SIGSEGV/invalid free/throw(`。

## NOTE 迭代链（档案索引，由 NOTE.md 全量迁移，`NOTE.md:1-709`）

2026-08-25e（RSS 证伪+纪律）→ 2026-08-25d（RSS 插桩+旧 0.28x 撤回）→ 2026-08-25c（归因+Rejected 门控）→ 2026-08-25b（engagement+比较器修复）→ 2026-08-25（三修复+caveat 解除）→ 2026-08-24e（1.27 rebase+b1270 基线）→ 2026-08-24d（1.26.6 rebase+WB 排空/窗口分配区+残留 miss）→ 2026-08-24c（root 拆分+stack 可疏散）→ 2026-08-24b（frag 负载+sticky 门+epoch 限频）→ 2026-08-24（频率/有界选择）→ 2026-08-23（增量会计）→ 2026-08-21（硬化+归因）。尾部 `Verified`（`NOTE.md:680-692`：fmt/check-format/verify-runtime/test-project/test-race + smoke + labels `iter-*/p0b-*/b1270-*/p0-fix-*`）+ `Known Limits`（`NOTE.md:694-709`）为当前门状态。

## 迁移说明

- NOTE.md 保留为追加式原始日志档案；本层只收敛结论与证据指针，细节回查 NOTE 行号；新增迭代先记 NOTE，再收敛到 D/E 层。
