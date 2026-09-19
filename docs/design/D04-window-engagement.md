# D04 窗口 engagement（生产力门 + 自适应 arming）

ID: D04。上游：S03。实现：[M02 engagement](../impl/M02-fork-map.md#engagement)。证据：[E02 p1b](../evidence/E02-bench-matrix.md#p1b) 的 frag-evac 行。

## 设计

- 仅生产力窗口 commit `g1EvacLastAlloc`（`g1gcCommitProductiveWindow`）；非生产力窗口不消耗 allocation credit，但下次尝试仍须满足至少 32 cycles 的窗口间隔和 arming 条件，不保证紧接的下一个 cycle 重试。
- 选择门改为 reclaim 投影（`spanBytes-liveBytes` 达 `copyBudget/8=32KiB`，live floor 1 KiB），替代旧 live-bytes 门。
- 自适应 arming：连续 3 空闲窗口 suspend，跳过 activation（零记录税），`heapLive` 涨 25% 重 arm；trace 增 `evac-idle/evac-suspended`。

## 拒绝项（由 NOTE 迁移）

- Phase 1 暂停地板项（prewarmed dest、chunk bitmap、quickselect、gated gen store）：`NOTE.md` 2026-08-25b 当次窗口成本 frag 47us / pointer64 12us（select 占 80%/57%），未复现历史约 1ms 固定成本，因此 defer；这些数值不是当前测量。
- `g1gcUsedStatsActive` 门控常规 alloc/sweep hook：`NOTE.md` 2026-08-25c 三配置归因 default 0.988/1.041、g1gc-only 0.992/1.007、evac 1.032/0.991，稳态税不可分，Rejected。当前常规 dirty hook 仍由 `debug.g1gc` 门控。
- 每窗建 CSet 扫 unconditional 消费实验：当时无吞吐收益且增方差，已 revert（`NOTE.md` 2026-08-24）。

## 背景数值（由 NOTE 迁移）

- `NOTE.md` 2026-08-24 的阈值史为 4GiB/16x → 2GiB/8x；当时按 used 覆 8 MiB 的 tagging 实验使 selection 从 724us 降至 40us。当前 tagging 已按 reclaim 排序，以 64 MiB 为停止目标，见 S03/D03。
- 比较器修复：`NOTE.md` 2026-08-25b 记录当时 `/usr/local/go` 实为 1.26.6 fork，因此旧 p0-fix-frag 的上游性能幅度作废。当前脚本优先选择 `toolchain/official-go-1270/go/bin/go`，其回退行为及基线核验要求见 S04。

## 来源

- 当前实现：`g1gc_cycle.go#g1gcStartCycle`、`g1gc_evac_select.go#g1gcNoteIdleWindow/g1gcCommitProductiveWindow`。历史：`NOTE.md` 2026-08-24、2026-08-25b/c。
