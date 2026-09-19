# D02 粘性候选门（sticky gate）

ID: D02。上游：S02。实现：[M02 sticky](../impl/M02-fork-map.md#sticky)。证据：[E03 stress](../evidence/E03-correctness-stress.md#stress)、[E02 p0-fix-frag](../evidence/E02-bench-matrix.md#p0-fix-frag)。

## 设计

- marker 只记录 sticky target region 的边（`g1StickyRegions`）。初始 bitmap 为全 1；之后由上一窗口的 low-live 且无非栈根引用区域生成，记录成本跟随该候选快照。
- selection 只选 low-live ∩ sticky，保证选中源的边全索引；非 sticky 区观察一窗后加入。
- 关键时序：`g1gcPublishStickyRegions` 必须在 `g1gcEvacuate` 之后（mark termination），与 recorder 同一 bitmap；早先在选择前发布新快照导致新 low-live 区缺失索引边（`NOTE.md` 2026-08-25 Fix 2）。

## 拒绝项（由 NOTE 迁移）

- 全堆记录：成本不可接受；窗口分配区黑对象可能经无写屏障的 bulk copy 建立引用，改走 stop-the-world 覆盖（S03；`NOTE.md` 2026-08-24d Fix 1/2：`g1gcDrainPendingWBSlots` + `g1WindowAllocRegions/List`）。
- WB 排空背景：mark termination 会丢弃 per-P wbBuf，疏散仍需其中的（slot, pointer）对，故在 `g1gcEvacuate` 内先排空再判 overflow（`NOTE.md` 2026-08-24d）。
- 残留 miss 曾疑 GreenTea inline-mark 与 `g1gcCountLive` 不对称（`NOTE.md` 2026-08-24d），后证为快照时序问题（2026-08-25 Fix 2），该假设已关闭。

## 取证法（由 NOTE 迁移）

- `g1evac>=5` miss dump 含 owner `listed=`、谓词位 `abit/mbit/imc`、目标 `tbase/tac/tdbit`。2026-08-25 Fix 2 的关键观察：miss 槽 `tdbit=false` 但拷贝日志显示目标已搬，owner `listed=false winreg=false` 表明 bounded pass 未访；forced-recording（bypass sticky gate）使 miss 归零。

## 来源

- `NOTE.md` 2026-08-24b、2026-08-24d、2026-08-25 Fix 2；当前快照发布见 `g1gc_account.go#g1gcPublishStickyRegions`，入边记录见 `g1gc_inbound.go`。
