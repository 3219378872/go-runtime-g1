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
- 首个 S04 标准矩阵 `matrix-0903`（15s n=7，`NOTE.md` 2026-09-03a）：pointer64 双行 ≥1.0（spread 宽，非 claim）；frag evac 与 default 持平；内存 parity；gc_cpu 残留 1.00-1.08。此为 region-aware 分配工作的对照组。
- dense-refill MVP（`NOTE.md` 2026-09-03b，D05）：门禁全绿 + stress 9/9；`ra1-*` 子集跨 session 无可裁决效应（噪声地板下），结构地基成立，待同 session A/B 裁决。
- 同 session old-vs-new A/B（`NOTE.md` 2026-09-03c）：active 行 +2.0%/-0.1%，gate-off 正反向自斥（+4.9% vs 1.0005）→ ±5% 带不可度量，特性中性，无回归可复现；sub-5% 需换宿主。
