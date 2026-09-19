# 追溯矩阵（唯一可信源）

格式：`I -> S -> D -> M(文件#符号) <-> E(门禁/用例/label)`。下表链接指向实际文档及显式章节锚点；E02/E03 的历史结果不表示当前提交已复验。

| I | S | D | M | E |
|---|---|---|---|---|
| [I01](intent/I01-why-g1.md) | [S01](spec/S01-collect-cycle.md) | [D01](design/D01-region-accounting.md) | [M01 周期](impl/M01-sim-map.md#collect)、[M02 会计](impl/M02-fork-map.md#accounting) | [E01 verify](evidence/E01-gates.md#verify)、[E03 单元](evidence/E03-correctness-stress.md#unit) |
| [I01](intent/I01-why-g1.md) | [S02](spec/S02-evac-safety.md) | [D02](design/D02-inbound-sticky.md)、[D03](design/D03-root-exclusion.md) | [M02 重写](impl/M02-fork-map.md#evacuate-rewrite) | [E03 stress](evidence/E03-correctness-stress.md#stress)、[E02 p0-fix-frag](evidence/E02-bench-matrix.md#p0-fix-frag) |
| [I01](intent/I01-why-g1.md) | [S03](spec/S03-pause-budget.md) | [D04](design/D04-window-engagement.md) | [M01 集合选择](impl/M01-sim-map.md#collect-select)、[M02 窗口](impl/M02-fork-map.md#engagement) | [E02 b1270](evidence/E02-bench-matrix.md#b1270)、[p1b](evidence/E02-bench-matrix.md#p1b)、[p2/p2s](evidence/E02-bench-matrix.md#p2) |
| [I02](intent/I02-scope.md) | [S01](spec/S01-collect-cycle.md) | [D01](design/D01-region-accounting.md) | [M01 模拟器](impl/M01-sim-map.md#sim)、[M02 fork](impl/M02-fork-map.md#fork) | [E01 verify-runtime](evidence/E01-gates.md#verify-runtime) |
| [I02](intent/I02-scope.md) | [S04](spec/S04-bench-protocol.md) | — | [M03 harness](impl/M03-bench-map.md#bench-harness) | [E01 preflight](evidence/E01-gates.md#preflight)、[E02 矩阵](evidence/E02-bench-matrix.md#matrix) |
| [I03](intent/I03-success-bar.md) | [S04](spec/S04-bench-protocol.md) | — | [M03 配对汇总](impl/M03-bench-map.md#repeat-compare) | [E02 p3-frag-mem](evidence/E02-bench-matrix.md#p3-frag-mem)、[b1270](evidence/E02-bench-matrix.md#b1270) |
| [I03](intent/I03-success-bar.md) | [S02](spec/S02-evac-safety.md) | [D02](design/D02-inbound-sticky.md) | [M02 sticky 发布](impl/M02-fork-map.md#sticky) | [E03 诊断 stress](evidence/E03-correctness-stress.md#stress) |
| [I03](intent/I03-success-bar.md) | [S03](spec/S03-pause-budget.md) | [D05](design/D05-region-alloc.md) | [M02 dense-refill](impl/M02-fork-map.md#dense-refill) | [E02 ra1](evidence/E02-bench-matrix.md#ra1)、[ab/abr](evidence/E02-bench-matrix.md#ab) |

## 校验规则

- 每个 `S` 至少链一个 `D` 或显式标 `—`（协议类 S04 无设计）。
- 每份 `M` 文档提供 `证据：` 回链；每份 `E` 文档声明 `覆盖：`，具体条目注明用例、门或迭代 label。
- `bench/check-trace.sh` 检查 `docs/` 中形如 `I01/S01/D01/M01/E01` 的 ID 是否出现在矩阵，另对 `*.go:行号` 引用抽查文件存在性并告警。它不校验章节锚点、函数、行号、实际跨层覆盖或行为一致性；上述关系仍须结合链接、源码和证据核对。
