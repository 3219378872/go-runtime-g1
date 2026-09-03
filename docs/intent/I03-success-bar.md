# I03 成功标尺

ID: I03。上层：无。下游：S02, S03, S04。

## 条形

- `tp > 1.0` 稳定，`stw_max < 1.0`，`stw_p99 < 1.0`，`gc_cpu <= 1.0`，处处成立；frag 不差于 default。
- 来源：`NOTE.md:326-327`（2026-08-24e 基线节）。
- 测量协议见 S04；违反协议的单次 run 数字不得作为 claim。

## 当前状态（由 NOTE 各迭代结论迁移，不替代证据）

- frag+evac 首次健康：tp 1.041、stw_max 0.568（`NOTE.md:175-183` p1b 矩阵，n=7）。
- 稳态开销在噪声内：三配置归因 default 0.988 / g1gc-only 0.992 / evac 1.032，同 session 内 evac 反超 default（`NOTE.md:98-108`）；sub-3% 需 DURATION>=15s n>=7（`NOTE.md:110-136`）。
- 内存：avg parity、final 最多 -24%、peak 由 copy budget 界定（`NOTE.md:10-39` 25e 纪律 + `NOTE.md:55-87` p3 表格）；旧 0.28x heap_sys 系 fork-vs-fork 伪影，已撤回（`NOTE.md:69-73`）。
- 基线锚点：b1270 矩阵 evac 保 tail（stw_max 0.35-0.89）但 tp 0.90-0.98x（`NOTE.md:307-327`）；成功条自 2026-08-24e 未变。
