# E02 Bench 矩阵证据（裁决子集）

ID: E02。覆盖：M03, S03/S04。来源：`bench/results/repeated/<label>.summary.json`（全量 295 个，本页只收裁决集）。

## 裁决 label（由 NOTE 各迭代矩阵迁移）

| label | 对应迭代 | 结论摘要 |
|---|---|---|
| `b1270-*-default/evac` | 2026-08-24e rebase 基线（`NOTE.md:307-327`） | evac 保 tail（stw_max 0.35-0.89），tp 0.90-0.98x；frag+evac 崩溃（预存） |
| `p0b-*` | 2026-08-24d（`NOTE.md:371-383`） | rebase 后基线；frag+evac 崩溃（预存 bug，与 1261 同现） |
| `p0-fix-frag` | 2026-08-25（`NOTE.md:280-284`，n=3） | 首次 frag+evac 健康：tp 1.035x（min 1.008），stw_total 0.977；heap_sys 0.28x（后证伪，见 p3） |
| `p1b-*-evac` | 2026-08-25b（`NOTE.md:175-189`，n=7） | frag+evac tp 1.041 / stw_max 0.568；alloc 1.007 过 parity；pointer64 0.957 |
| `p2-*/p2s-*` | 2026-08-25c（`NOTE.md:110-126`，n=7@5s + 15s 重跑） | 稳态税噪声；alloc-evac 三 session parity-or-better（0.988/1.007/1.007）；余 0.96-1.04 跨 session 漂 |
| `p3-frag-mem` | 2026-08-25d（`NOTE.md:55-87`，n=5） | rss_max 1.22x 伪影；rss_final 0.84x（75→63MB）；evac peak ~0；旧 0.28x 撤回；遗留 BSS 29.6MB 待查（25e 证伪基线抬高，`NOTE.md:10-19`） |
| `iter-baseline-*, iter-e1-*, iter-e2-*, iter-live128k-*` | 2026-08-21/23（`NOTE.md:591-617,662-678`） | 暂停归因与增量会计验证；census 跳过消 mark-bit 份额（1.5-3.9ms→0.9-1.6ms） |

## 读数规则（S04）

- 只读配对 `median` + spread；跨 session 符号翻转判噪声。
- 内存读 `rss_avg/rss_final`；`rss_max` 诊断。
- 详情见 `NOTE.md` 各迭代节与 `bench/compare` 输出。
