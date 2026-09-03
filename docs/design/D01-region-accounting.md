# D01 增量 used-region 会计

ID: D01。上游：S01。实现：M02#accounting。证据：E03#trace, E02#p0-fix-frag。

## 设计

- 热路径 hook 只标 dirty（per-region flag + 有界 dirty list），mark termination 只走 dirty regions。
- Span 枚举直读页分配器 `pallocBits`（`g1gcForEachWindowSpan`）；逻辑 region 按 64 Ki 哈希，chunk 扫 `mheap_.pages.inUse`，先入 scratch 再 reconcile（防哈希碰撞 double-count）。
- 选择三 sweep 合一（collect 单 pass ~27us，下自 ~50us）。

## 拒绝/已知坑（由 NOTE 迁移）

- cached-span 快分配曾绕过 hook（mcache fast path 多周期无 refill，`NOTE.md:213-230` Fix 1）：复现 `region N used-bytes undercount auth=X got=X-6048`（= size-class-288 单 span 21×288）；`g1evac>=5` allspans/windowscan 双枚举一致，证枚举无错、账本 stale。修复 `malloc.go nextFree` 慢路径 + `mcache.go refill` 按 allocCache batch（~64 allocs）补 dirty，hook 仍 gate 于 `debug.g1gc`。
- 增量 live 会计（`NOTE.md:651-660` 26-08-21）：`greyobject`/`gcmarknewobject` 首 mark 经 `g1gcRecordLiveObject` 记账，仅窗口周期 gate 于 `g1EvacIndexActive`；窗口周期 `g1gcResetLiveCounts` rebase + 跳 census，generation tag 覆盖 stale；其余周期走原精确路径。
- 仍线性于 mapped chunks；chunk-presence bitmap 为后续优化（`NOTE.md:619-627`）。
- bootstrap 首个 accounting 使能周期走 allspans 全量覆盖 GODEBUG 解析前创建的 span（`NOTE.md:626-627`）。

## 暂停归因（由 NOTE 迁移）

- 三 sweep 合一 collect 单 pass ~50us → ~27us；`_Gscan` 轻协议替代全 suspend/resume；窗口总附加 ~35us（select 27 + rewrite 4 + copy 0 + init 0），此前 ~170us（`NOTE.md:574-589`）。
- 5-run 中值：stw_max 2.33 → 0.98，p99 1.53 → 0.99，gc_cpu 1.03 → 0.97..1.02（`NOTE.md:591-599`）。

## 来源

- `NOTE.md` 2026-08-23 全节（`NOTE.md:539-627`）+ 2026-08-21 增量 live（`NOTE.md:651-660`）+ 2026-08-25 Fix 1（`NOTE.md:213-230`）；`REBASE-1.27.md` 无冲突合并。
