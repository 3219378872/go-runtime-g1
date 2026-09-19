# E02 Bench 矩阵证据（裁决子集）

ID: E02。覆盖：M03, S03/S04。来源：`NOTE.md` 各日期迭代及其 `bench/results/repeated/<label>.summary.json` 指针；原始结果 gitignored，不保证在新 checkout 中存在，也不固定本地文件数量。

<a id="matrix"></a>

## 历史裁决 label（不代表当前提交的性能结论）

下列数字保留对应迭代的测量条件、噪声和撤回状态。重新裁决当前实现时按 S04 和 `AGENTS.md` 构建、配对、复跑，不能直接复用本表。

| label | 对应迭代 | 结论摘要 |
|---|---|---|
| <a id="b1270"></a>`b1270-*-default/evac` | `NOTE.md` 2026-08-24e rebase 基线 | 当时记录 evac 保 tail（stw_max 0.35-0.89），tp 0.90-0.98x；frag+evac 崩溃（预存）。早于 25b 比较器修复，对照 provenance 必须回查原记录 |
| `p0b-*` | `NOTE.md` 2026-08-24d | rebase 后基线；frag+evac 崩溃（预存 bug，与 1261 同现） |
| <a id="p0-fix-frag"></a>`p0-fix-frag` | `NOTE.md` 2026-08-25，n=3；25b/25d 修正解读 | 当次 frag+evac 正确性恢复；旧 tp 1.035x、stw_total 0.977、heap_sys 0.28x 来自 fork-vs-fork 对照，上游性能幅度作废，0.28x 内存结论撤回 |
| <a id="p1b"></a>`p1b-*-evac` | `NOTE.md` 2026-08-25b，n=7 | frag+evac tp 1.041 / stw_max 0.568；alloc 1.007 过 parity；pointer64 0.957 |
| <a id="p2"></a>`p2-*/p2s-*` | `NOTE.md` 2026-08-25c，n=7@5s + 15s 重跑 | 稳态税噪声；alloc-evac 三 session parity-or-better（0.988/1.007/1.007）；余 0.96-1.04 跨 session 漂 |
| <a id="p3-frag-mem"></a>`p3-frag-mem` | `NOTE.md` 2026-08-25d，n=5；25e 后续核查 | rss_max 1.22x 判为伪影；rss_final 0.84x（75→63MB）；旧 0.28x 撤回。25d 对 BSS 29.6MB 的疑问在 25e 后续核查中未证实为 RSS 基线抬高，不能继续列为已确认开销 |
| `iter-baseline-*, iter-e1-*, iter-e2-*, iter-live128k-*` | `NOTE.md` 2026-08-21/23 | 暂停归因与增量会计验证；census 跳过消 mark-bit 份额（1.5-3.9ms→0.9-1.6ms） |
| `matrix-0903-*-default/evac` | 2026-09-03a 首个 S04 标准矩阵（`NOTE.md` 2026-09-03a，15s n=7 在树 official） | pointer64 双行 parity-or-better（1.013/1.017，spread 0.93-1.10 非 claim）；frag evac 0.976 与 default 持平仍未 engagement；内存 parity（rss_final 翻转判噪声）；gc_cpu 1.00-1.08 残留未变 |
| <a id="ra1"></a>`ra1-pointer64-default/evac, ra1-frag-evac` | 2026-09-03b dense-refill 后子集（15s n=7，跨 session，无同 session A/B） | 无可裁决效应：符号混合（p64 tp -2~-5% 但区间重叠；frag stw_max 0.73 改善 vs p64 stw_p99 1.31；stw_max max 4-7x 皆单 run 尖峰）；gc_cpu 一致；效应低于宿主噪声地板 |
| <a id="ab"></a>`ab-p64evac/ab-fragevac/ab-p64default/abr-p64default` | 2026-09-03c 同 session old-vs-new 交替 A/B（15s n=7，ratio=new/old） | active 行 +2.0%/-0.1% 落在包络内；gate-off 正向 +4.9%（7/7≥0.999）vs 反向 10 分钟后 1.0005 互斥，±5% 带不可度量；gc_cpu 1.00-1.05。该历史记录不能作为当前任务门控通过或无回归证明 |

## 读数规则（S04）

- 只读配对 `median` + spread；跨 session 符号翻转判噪声。
- 内存读 `rss_avg/rss_final`；`rss_max` 诊断。
- 详情按 `NOTE.md` 迭代日期/标题和 label 定位；原始结果存在时再核对 `bench/compare` 输出。
