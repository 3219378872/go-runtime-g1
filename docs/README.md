# 五层知识库总览

`意图层 -> 规格层 -> 设计层 -> 实现层 <-> 证据层`，落盘于 `docs/`。

## 阅读路径

1. 意图 (`intent/`)：为什么做、成功标尺是什么。
2. 规格 (`spec/`)：可测行为契约，给定/当/则。
3. 设计 (`design/`)：算法与权衡，含已拒绝项。
4. 实现 (`impl/`)：`文件#符号` 索引，不复制代码正文。
5. 证据 (`evidence/`)：测试、门禁、bench、NOTE 摘录。

[TRACEABILITY.md](TRACEABILITY.md) 是唯一可信的跨层矩阵。各页保留上游、下游及实现/证据指针，完整覆盖关系集中在矩阵中。

## 编号规则

- 前缀固定：`I` 意图、`S` 规格、`D` 设计、`M` 实现索引、`E` 证据。
- 例：`I01`、`S02`、`D03`、`M02`、`E02`。ID 一旦分配不再复用。
- 源码按 `文件#符号` 定位；迭代记录按日期/节标题定位，不用会随增补漂移的固定行号。
- `bench/check-trace.sh` 检查 `docs/` 中形如 `I01/S01/D01/M01/E01` 的 ID 是否出现在矩阵，并抽查 `*.go:行号` 引用的文件是否存在（缺文件只告警）。它不检查行号、符号、章节锚点或行为一致性，不能替代源码核对。
- `NOTE.md` 保持追加式日志，本库只做结构化摘录，不双写正文。

## 度量纪律（S04 的摘要）

- 内存只认配对运行的 `rss_avg` / `rss_final` 中位数；`rss_max` 仅诊断。
- 标准性能比较采用 15s、n=7、同 session 交替配对；满足时长和重复数仍不能保证小效应可裁决。
- 历史宿主的噪声与撤回结论见 `NOTE.md` 2026-08-25e、2026-09-03c；当前任务的回归门以 `AGENTS.md` 和 S04 为准。

## 迁移对照（原有知识库 → 五层）

| 原文件 | 迁移去向 | 说明 |
|---|---|---|
| `README.md` 算法清单+句柄约束 | I01 | 模拟器边界 |
| `README.md` 测试/just/bench/demo | I02, E01, M01 | 命令以 justfile 为准 |
| `README.md`「Benchmark environment」 | S04 | 四步+隔离要求 |
| `bench/README.md` harness | S04, M03 | 脚本为准，本页索引 |
| `NOTE.md`「Current State」 | I02 | fork 基线声明 |
| `NOTE.md` 2026-09-04、09-04b/c/d | M01, M02, E03 | fork 九文件拆分；模拟器 allocator/rsetIndex/marker 与测试拆分 |
| `NOTE.md` 2026-09-03a/b/c | D05, I03, E02 | 标准矩阵、dense-refill 和同 session A/B |
| `NOTE.md` 2026-08-25e | I03, S04, E02 | RSS 撤回与度量纪律 |
| `NOTE.md` 2026-08-25d | I03, E02 p3 | 旧 0.28x 撤回 |
| `NOTE.md` 2026-08-25c | D04, I03, E02 p2 | Rejected 门控 |
| `NOTE.md` 2026-08-25b | D04, E02 p1b | engagement+比较器修复 |
| `NOTE.md` 2026-08-25 Fix 1–3 | D01/D02/D03, S02, E02/E03 | 当次正确性 caveat 解除 |
| `NOTE.md` 2026-08-24e | E02 b1270, M02 | 1.27 迁移时基线 |
| `NOTE.md` 2026-08-24d | S02/S03, D02, E02 p0b | WB 排空+窗口区+残留 miss |
| `NOTE.md` 2026-08-24c | D03 | root 拆分 |
| `NOTE.md` 2026-08-24b | D02, M03 | frag+sticky+epoch |
| `NOTE.md` 2026-08-24 | D04 | 频率/有界选择 |
| `NOTE.md` 2026-08-23 | D01 | 增量会计+暂停收缩 |
| `NOTE.md` 2026-08-21 | D01/M01/M02, E02 iter | 硬化+归因 |
| `NOTE.md`「Verified」「Known Limits」 | E01/E03, I01 | 历史门禁与限制；不表示当前提交已复验 |
| `REBASE-1.27.md` | M02 | 迁移时的 17 文件 drift 表与过程，非当前改动清单 |
| `REBASE-1.26.md` GreenTea 适配 | M02 | 1.26 迁移背景 |
| `justfile` | E01, M03 | 门与 bench 命令 |

## 目录

```text
docs/
  README.md
  TRACEABILITY.md
  intent/I01-why-g1.md, I02-scope.md, I03-success-bar.md
  spec/S01-collect-cycle.md, S02-evac-safety.md, S03-pause-budget.md, S04-bench-protocol.md
  design/D01-region-accounting.md, D02-inbound-sticky.md, D03-root-exclusion.md, D04-window-engagement.md, D05-region-alloc.md
  impl/M01-sim-map.md, M02-fork-map.md, M03-bench-map.md
  evidence/E01-gates.md, E02-bench-matrix.md, E03-correctness-stress.md
```
