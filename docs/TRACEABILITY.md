# 追溯矩阵（唯一可信源）

格式：`I -> S -> D -> M(文件:行) <-> E(门禁/用例/label)`。

| I | S | D | M | E |
|---|---|---|---|---|
| I01 | S01 | D01 | M01#collect, M02#g1gcInitializeUsed | E01#verify, E03#unit |
| I01 | S02 | D02, D03 | M02#evacuate-rewrite | E03#stress-frag, E02#p0-fix-frag |
| I01 | S03 | D04 | M01#collect-select, M02#g1gcEvacThreshold | E02#b1270/p1b/p2/p2s |
| I02 | S01 | D01 | M01#sim, M02#fork17 | E01#verify-runtime |
| I02 | S04 | — | M03#bench-harness | E01#preflight, E02#matrix |
| I03 | S04 | — | M03#repeat-compare | E02#p3-frag-mem, E02#b1270 |
| I03 | S02 | D02 | M02#sticky-publish | E03#trace-g1evac4 |

## 校验规则

- 每个 `S` 至少链一个 `D` 或显式标 `—`（协议类 S04 无设计）。
- 每个 `M` 节尾必须有 `证据：` 回链；每个 `E` 条目必须有 `覆盖：` 回链。
- 校验脚本：`bench/check-trace.sh` 检查 `docs/**/I[0-9]*|S[0-9]*|D[0-9]*|M[0-9]*|E[0-9]*` ID 在矩阵中出现。
