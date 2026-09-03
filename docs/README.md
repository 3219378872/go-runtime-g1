# 五层知识库总览

`意图层 -> 规格层 -> 设计层 -> 实现层 <-> 证据层`，落盘于 `docs/`。

## 阅读路径

1. 意图 (`intent/`)：为什么做、成功标尺是什么。
2. 规格 (`spec/`)：可测行为契约，给定/当/则。
3. 设计 (`design/`)：算法与权衡，含已拒绝项。
4. 实现 (`impl/`)：`文件:行` 索引，不复制代码正文。
5. 证据 (`evidence/`)：测试、门禁、bench、NOTE 摘录。

`TRACEABILITY.md` 是唯一可信的跨层矩阵。单文档只链上下一层。

## 编号规则

- 前缀固定：`I` 意图、`S` 规格、`D` 设计、`M` 实现索引、`E` 证据。
- 例：`I01`、`S02`、`D03`、`M02`、`E02`。ID 一旦分配不再复用。
- `M` 允许行号漂移（只告警）；`I/S/D/E` 的 ID 缺失则校验失败。
- `NOTE.md` 保持追加式日志，本库只做结构化摘录，不双写正文。

## 度量纪律（S04 的摘要）

- 内存只认配对运行的 `rss_avg` / `rss_final` 中位数；`rss_max` 仅诊断。
- 吞吐子 3% 效应需 `DURATION>=15s, n>=7` 交替同 session 比较，否则判为噪声。
- 来源：`NOTE.md` 2026-08-25e 迭代。

## 迁移对照（原有知识库 → 五层）

| 原文件 | 迁移去向 | 说明 |
|---|---|---|
| `README.md:3-19` 算法清单+句柄约束 | I01 | 原文保留，新解读以 I01 为准 |
| `README.md:21-65` 测试/just/bench/demo | I02, E01, M01 | 命令以 justfile 为准 |
| `README.md:67-96` benchmark environment | S04 | 四步+隔离要求 |
| `bench/README.md:1-57` harness | S04, M03 | 脚本为准，本页索引 |
| `NOTE.md:1-9` Current State | I02 | fork 基线声明 |
| `NOTE.md:10-39` 25e + `27-33` 纪律 | I03, S04, E02 | 度量纪律权威来源 |
| `NOTE.md:41-96` 25d RSS | I03, E02 p3 | 旧 0.28x 撤回 |
| `NOTE.md:98-136` 25c | D04, I03, E02 p2 | Rejected 门控 |
| `NOTE.md:138-204` 25b | D04, E02 p1b | engagement+比较器修复 |
| `NOTE.md:206-291` 25 Fix1-3 | D01/D02/D03, S02, E02/E03 | caveat 解除 |
| `NOTE.md:293-349` 24e | E02 b1270, M02 | 1.27 基线 |
| `NOTE.md:350-433` 24d | S02/S03, D02, E02 p0b | WB 排空+窗口区+残留 miss |
| `NOTE.md:435-474` 24c | D03 | root 拆分 |
| `NOTE.md:476-504` 24b | D02, M03 | frag+sticky+epoch |
| `NOTE.md:506-537` 24 | D04 | 频率/有界选择 |
| `NOTE.md:539-627` 23 | D01 | 增量会计+暂停收缩 |
| `NOTE.md:629-678` 21 | D01/M01/M02, E02 iter | 硬化+归因 |
| `NOTE.md:680-709` Verified/Limits | E01/E03, I01 | 门状态 |
| `REBASE-1.27.md` | M02 | 17 文件 drift 表+过程 |
| `REBASE-1.26.md:20-56` GreenTea 适配 | M02 | 1.26 事实 |
| `justfile` | E01, M03 | 门与 bench 命令 |

## 目录

```text
docs/
  README.md
  TRACEABILITY.md
  intent/I01-why-g1.md, I02-scope.md, I03-success-bar.md
  spec/S01-collect-cycle.md, S02-evac-safety.md, S03-pause-budget.md, S04-bench-protocol.md
  design/D01-region-accounting.md, D02-inbound-sticky.md, D03-root-exclusion.md, D04-window-engagement.md
  impl/M01-sim-map.md, M02-fork-map.md, M03-bench-map.md
  evidence/E01-gates.md, E02-bench-matrix.md, E03-correctness-stress.md
```
