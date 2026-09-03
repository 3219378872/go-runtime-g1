# D02 粘性候选门（sticky gate）

ID: D02。上游：S02。实现：M02#sticky。证据：E03#stress-frag, E02#p0-fix-frag。

## 设计

- marker 只记录上一窗口看似候选的 target region 边（`g1StickyRegions`），记录成本跟随候选集而非全堆。
- selection 只选 low-live ∩ sticky，保证选中源的边全索引；非 sticky 区观察一窗后加入。
- 关键时序：`g1gcPublishStickyRegions` 必须在 `g1gcEvacuate` 之后（mark termination），与 recorder 同一 bitmap；早先放在 `g1gcInitializeUsed` 前导致新 low-live 区零索引边（`NOTE.md:239-252` Fix 2）。

## 拒绝项（由 NOTE 迁移）

- 全堆记录：成本不可接受；窗口分配区黑对象无 barrier 边，改走 stop-the-world 覆盖（S03，`NOTE.md:394-408` Fix 1/2：`g1gcDrainPendingWBSlots` + `g1WindowAllocRegions/List`）。
- WB 排空背景：上游 `gcMarkTermination` 在 `gcMarkDone` 后弃 per-P wbBuf，疏散仍需该（slot, pointer）对，故在 `g1gcEvacuate` 内先排空再判 overflow（`NOTE.md:394-398`）。
- 残留 miss 曾疑 greenteagc inline-mark（`g1gcRewriteSpan` 双读缺失）与 `g1gcCountLive` 不对称（`NOTE.md:409-415`），后证为快照时序问题，本项关闭。

## 取证法（由 NOTE 迁移）

- `g1evac>=5` miss dump 含 owner `listed=`、谓词位 `abit/mbit/imc`、目标 `tbase/tac/tdbit`；决定性事实：miss 槽 `tdbit=false` 但拷贝日志显示目标已搬，owner `listed=false winreg=false` 即 bounded pass 未访（`NOTE.md:233-238`）；forced-recording（bypass sticky gate）miss 归零定罪 gate 本身（`NOTE.md:238`）。

## 来源

- `NOTE.md` 2026-08-24b sticky 门（`NOTE.md:487-492`）+ 2026-08-25 Fix 2（`NOTE.md:231-252`）+ 2026-08-24d WB 排空/窗口分配区（`NOTE.md:394-408`）。
