# D04 窗口 engagement（生产力门 + 自适应 arming）

ID: D04。上游：S03。实现：M02#engagement。证据：E02#p1b-frag-evac。

## 设计

- 仅生产力窗口 commit `g1EvacLastAlloc`（`g1gcCommitProductiveWindow`）；非生产力窗口 end-window + 下 cycle 重试，不烧 allocation credit。
- 选择门改为 reclaim 投影（`spanBytes-liveBytes` 达 `copyBudget/8=32KiB`，live floor 1 KiB），替代旧 live-bytes 门。
- 自适应 arming：连续 3 空闲窗口 suspend，跳过 activation（零记录税），`heapLive` 涨 25% 重 arm；trace 增 `evac-idle/evac-suspended`。

## 拒绝项（由 NOTE 迁移）

- Phase 1 暂停地板项（prewarmed dest、chunk bitmap、quickselect、gated gen store）：窗口成本已降至 frag 47us / pointer64 12us（select 占 80%/57%），历史 ~1ms 固定成本消失，归因到噪声，defer（`NOTE.md:140-149` 25b）。
- `g1gcUsedStatsActive` 门控 alloc/sweep hook：三配置归因 default 0.988/1.041、g1gc-only 0.992/1.007、evac 1.032/0.991，evac 反超 default，稳态税不可分，Rejected（`NOTE.md:100-108` 25c）。
- 每窗建 CSet 扫 unconditional 消费实验：无吞吐收益增方差，已 revert（`NOTE.md:519-524`）。

## 背景数值（由 NOTE 迁移）

- 触发阈值史 4GiB/16x → 2GiB/8x，compaction 率翻倍暂停形不变；`g1gcTagLowLiveRegions` 按 used 覆 8 MiB 有界，LIVE_ROOTS=131072 下 selection 724us → 40us，去 3.8x stw_max 回归（`NOTE.md:506-524`）。
- 比较器修复：默认 OFFICIAL_GO 改指 `toolchain/official-go-1270/go/bin/go`；`/usr/local/go` 实为 1.26.6 fork，旧 p0-fix-frag 系 fork-vs-fork，方向不变幅度作废（`NOTE.md:169-173`）。

## 来源

- `NOTE.md` 2026-08-25b（`NOTE.md:138-204`）、2026-08-25c（`NOTE.md:98-136`）、2026-08-24（`NOTE.md:506-537`）。
