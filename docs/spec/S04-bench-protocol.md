# S04 基准测量协议

ID: S04。上游：I02, I03。下游：无（协议）。实现：M03。证据：E01#preflight, E02。

## 协议

- 配对：同一 workload 源码，official vs candidate 各跑一次；`GOMAXPROCS/CPU亲和/duration/scenario` 一致（`bench/README.md:3-7`）。
- 重复：`repeat.sh` 默认交替顺序，`REPEATS` 聚合 min/median/q1/q3/iqr/p95/max + 配对 ratio，写 `<label>.summary.json`（`bench/README.md:48-56`）。
- 标准矩阵：15s runs, n=7 alternating, in-tree official go1.27.0（`justfile:39-60` `bench-matrix`）。
- 前置门：`just bench-preflight`（`bench/env-check.sh`）硬失败 offline 核 / steal 超预算。
- 单次 run 只做 smoke；性能 claim 必须报 median + spread（`bench/README.md:20`）。

## 度量纪律（由 NOTE 25e + README environment 迁移）

- 内存报 `rss_avg` + `rss_final`；`rss_max` 仅诊断（`NOTE.md:27-33`）；`bench/workload` 以 50ms `/proc/self/statm` 采样输出 `rss_max/avg/final_mb`（`NOTE.md:48-53`）。
- 同 session 内符号翻转的单指标视为噪声（`NOTE.md:31-33`）；VM/WSL 上 ±3-5% drift，sub-3% 不可度量（`README.md:82-86`）。
- bare-metal 隔离要求（`isolcpus/nohz_full`、`performance` governor、IRQ 避让）见 `README.md:82-86`，本库不复述，只引用。

## 迁移来源

- 协议由 `bench/README.md:1-57`、`README.md:67-96`、`justfile:25-60`、`NOTE.md:10-39,88-136` 合并；`bench-matrix` 为标准实现。
