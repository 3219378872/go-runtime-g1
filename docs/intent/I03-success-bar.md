# I03 成功标尺

ID: I03。上层：无。下游：S02, S03, S04。

## 成功标尺

- `tp > 1.0` 稳定，`stw_max < 1.0`，`stw_p99 < 1.0`，`gc_cpu <= 1.0`，处处成立；frag 不差于 default。
- 以上 ratio 为 candidate/official，来源：`NOTE.md` 2026-08-24e 基线矩阵。任务的 main/task 回归门另见 `AGENTS.md`，通过回归门不等于已达到上述上游比较目标。
- 测量协议见 S04；违反协议的单次 run 数字不得作为 claim。

## 历史结果摘要（按迭代记录，不替代当前验证）

- frag+evac：`NOTE.md` 2026-08-25b 的 p1b 矩阵记录 tp 1.041、stw_max 0.568（n=7）；此前 `p0-fix-frag` 的对照被发现是 fork-vs-fork，不能作为官方性能比较。
- 稳态开销归因：`NOTE.md` 2026-08-25c 记录 default 0.988 / g1gc-only 0.992 / evac 1.032，同 session 内 evac 反超 default；当时未能分离稳定的小效应。
- 内存：`NOTE.md` 2026-08-25d/e 的配对测量记录 avg 接近 parity、部分 final 下降；旧 0.28x heap_sys 系 fork-vs-fork 伪影，已撤回。复制预算约束窗口内目标 span 分配，不是整个进程的 RSS 峰值上限。
- 基线锚点：`NOTE.md` 2026-08-24e 的 b1270 矩阵 evac 保 tail（stw_max 0.35-0.89）但 tp 0.90-0.98x。
- 首个 S04 标准矩阵 `matrix-0903`（15s n=7，`NOTE.md` 2026-09-03a）：pointer64 双行 ≥1.0（spread 宽，非 claim）；frag evac 与 default 持平；内存 parity；gc_cpu 残留 1.00-1.08。此为 region-aware 分配工作的对照组。
- dense-refill MVP（`NOTE.md` 2026-09-03b，D05）：当次门禁全绿 + stress 9/9；`ra1-*` 子集跨 session 无可裁决效应，后续同 session A/B 见下一条。
- 同 session old-vs-new A/B（`NOTE.md` 2026-09-03c）：active 行 +2.0%/-0.1%，gate-off 正反向自斥（+4.9% vs 1.0005），当时宿主无法裁决 ±5% 带内效果。该档案不能替代当前任务的性能门。
