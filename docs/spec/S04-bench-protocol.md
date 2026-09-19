# S04 基准测量协议

ID: S04。上游：I02, I03。下游：无（协议）。实现：M03。证据：[E01 preflight](../evidence/E01-gates.md#preflight)、E02。

## 协议

- 配对：同一 workload 源码，两端的 `GOMAXPROCS/GOGC/GOMEMLIMIT/CPU亲和/duration/scenario/workers/live/batch/size` 一致。上游比较为 official/candidate；任务回归按 `AGENTS.md` 使用 fresh main/task，设置 A=main、B=task，summary 的 candidate/official ratio 此时表示 task/main。
- 重复：`bench/repeat.sh` 默认交替顺序，汇总 min/median/q1/q3/iqr/p95/max + 配对 ratio，写 `<label>.summary.json`。脚本默认 `REPEATS=5`、`DURATION=5s`，标准比较须显式设为 7 和 15s。
- 标准上游矩阵：`just bench-matrix` 默认 15s、n=7、四场景各 default/evac 两行，要求在树 official go1.27.0；recipe 用 `DURATION_VALUE` 覆盖时长，直接调用脚本用 `DURATION`。该矩阵不替代任务 main/task 门。
- 前置门：`just bench-preflight`（`bench/env-check.sh`）硬失败 offline 核 / steal 超预算。
- 单次 run 只做 smoke；性能 claim 必须报 median + spread。`bench/run.sh` 在指定 official 不可执行时回退 `/usr/local/go/bin/go`，运行前须核对工具链来源与版本；`bench-matrix` 对缺失的在树 official 硬失败。

## 度量纪律（由 NOTE 25e + README environment 迁移）

- 内存报 `rss_avg_mb` + `rss_final_mb`；`rss_max_mb` 仅诊断（`NOTE.md` 2026-08-25e）。`bench/workload/main.go#sampleRSS` 以 50ms 间隔采样 `/proc/self/statm`（2026-08-25d 引入）。
- 同 session 内符号翻转或 spread 无法分离的效应不能宣称改善。`NOTE.md` 2026-08-25c/e、2026-09-03c 的 3–5% 漂移是历史宿主观测；15s/n=7 和 preflight 通过均不能单独证明当前宿主可裁决小效应。
- bare-metal 隔离要求（`isolcpus/nohz_full`、`performance` governor、IRQ 避让）见根 `README.md`「Benchmark environment」。
- 任务回归门的影响矩阵、5% 阈值、越界后反向复跑和无法裁决时停止规则以 `AGENTS.md` 为准；模拟器使用 `bench_sim_test.go` 的 main/task 配对 benchmark。

## 迁移来源

- 当前接口取自 `bench/run.sh`、`bench/repeat.sh`、`justfile` 和 `AGENTS.md`；历史度量纪律见 `NOTE.md` 2026-08-25c/e、2026-09-03c。
