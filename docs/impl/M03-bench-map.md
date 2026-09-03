# M03 Bench 实现索引

ID: M03。覆盖 S04。证据：E02 全系。

| 组件 | 文件 | 职责 |
|---|---|---|
| 单次配对 | `bench/run.sh` | official/candidate 各跑一次，`taskset` 绑核，输出 `official.json/candidate.json+gctrace`，调 `compare` 打印 |
| 重复聚合 | `bench/repeat.sh` | `REPEATS/ORDER=alternate` 循环，`jq` 求 min/median/q1/q3/iqr/p95/max + 配对 ratio，写 `<label>.summary.json` |
| 正确性压测 | `bench/stress.sh` | 仅 candidate，扫 `bad pointer/rewrite missed/accounting drifted/SIGSEGV/throw(` 计 clean 数 |
| 比较器 | `bench/compare/main.go` | 校验 scenario 一致，打印 tp/alloc/GC/STW max/p99/GC_CPU/heap_sys/rss 三列 + ratio |
| 负载 | `bench/workload/main.go` | `pointer64/pointer256/alloc/frag` 四场景；frag 混 24B/264B/3KB/40KB + 每 50k ops 迁 1/4 live set（`NOTE.md:477-481`）；`/proc/self/statm` 50ms RSS 采样（`NOTE.md:48-53`） |
| 门禁 | `justfile:25-60,90-129` | `bench-preflight/matrix/smoke/bench-g1gc(set/evac/trace)/summary/stress`；`bench-matrix` 15s/n=7/官方在树（`README.md:88-93`）；`CANDIDATE_ROOT` 须绝对路径（`NOTE.md:502-504` ENOENT 教训）；`run.sh` 构建重试 backoff（`NOTE.md:525-527` WSL flake） |

证据：E02 `<label>.summary.json`（`bench/results/repeated/` 共 295 个，裁决子集见 E02）；生成物 gitignored，重建见 `NOTE.md:708-709`。

## 迁移来源

- 本页由 `bench/README.md:1-57`、`README.md:43-59,67-96`、`justfile:1-129`、`NOTE.md:477-527` 合并；行为细节以脚本为准，本页只做索引。
