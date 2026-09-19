# M03 Bench 实现索引

ID: M03。覆盖 S04。证据：E02 全系。

<a id="bench-harness"></a>

## 组件地图

| 组件 | 文件 | 职责 |
|---|---|---|
| 单次配对 | `bench/run.sh` | official/candidate 各跑一次，`taskset` 绑核，输出 `official.json/candidate.json+gctrace`，调 `compare` 打印 |
| <a id="repeat-compare"></a>重复聚合 | `bench/repeat.sh` | `REPEATS/ORDER=alternate` 循环，`jq` 求 min/median/q1/q3/iqr/p95/max + 配对 ratio，写 `<label>.summary.json` |
| 正确性压测 | `bench/stress.sh` | 仅 candidate，扫 `bad pointer/rewrite missed/accounting drifted/SIGSEGV/throw(` 计 clean 数 |
| 比较器 | `bench/compare/main.go` | 校验 scenario 一致，打印 tp/alloc/GC/STW max/p99/GC_CPU/heap_sys/rss 三列 + ratio |
| 负载 | `bench/workload/main.go` | `pointer64/pointer256/alloc/frag` 四场景；frag payload 混 24B/264B/3KiB/40KiB，每 50k 次 churn 迁 1/4 live set；`sampleRSS` 每 50ms 采样 `/proc/self/statm` |
| 环境前置门 | `bench/env-check.sh` | CPU 不存在/不在线或 steal 超限硬失败；隔离、governor、runqueue、虚拟化给出诊断 |
| 命令入口 | `justfile` | `bench-preflight`、`bench-matrix`、`bench-smoke`、`bench-g1gc`、`bench-g1gcset`、`bench-g1evac`、`bench-trace`、`bench-summary`、`stress`；矩阵默认 15s/n=7/在树 official，参数区别见 S04 |
| 模拟器性能 | `bench_sim_test.go` | 根模拟包的分配、引用、查询、回收与疏散 benchmark；main/task 门按 `AGENTS.md` |

`CANDIDATE_ROOT` 须绝对路径。`bench/run.sh` 的 `build` 清除构建期 GODEBUG 并有限重试；运行期才应用传入诊断配置。`bench/repeat.sh` 默认五对、`bench/run.sh` 默认 5s；标准比较显式覆盖为七对 15s。A/B 工具链含义及 official 回退行为见 S04。

证据：E02 索引 `bench/results/repeated/<label>.summary.json`；结果与二进制均 gitignored，本地数量和是否存在不属于仓库保证。正确性压力测试见 E03，环境前置门见 E01。

## 迁移来源

- 当前入口以 `justfile`、`bench/run.sh`、`bench/repeat.sh`、`bench/stress.sh`、`bench/env-check.sh` 为准。frag、路径与构建重试历史见 `NOTE.md` 2026-08-24b/2026-08-24；RSS 插桩见 2026-08-25d。
