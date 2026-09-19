# I02 范围划分

ID: I02。上层：无。下游：S01, S04。

## 三块范围（由原 README/NOTE/REBASE 迁移）

- 根模拟包：算法验证与行为回归，可 O(heap)。`Heap` 组合 `allocator`、`rsetIndex`、`marker`，分别由 `pool.go`、`rset.go`、`marker.go` 管理内部状态；`guards.go`/`cycle.go` 收敛锁与 STW 编排。完整文件地图见 `doc.go` 和 [M01](../impl/M01-sim-map.md)，运行入口为根 `README.md` 的项目测试与 `cmd/g1gc-demo`。
- 真实 fork：`toolchain/go-g1-1270-src`，go1.27.0 基；旧 `go-g1-1266-src`/`go-g1-1261-src` 留作参考。当前 G1 核心已拆为 9 个 `g1gc*.go` 文件，并在分配器、标记器、写屏障、sweep 及编译器中接入 hook，见 [M02](../impl/M02-fork-map.md)。`REBASE-1.27.md` 中“17 个改动文件 + 2 个新文件”描述的是迁移时状态；拆分记录见 `NOTE.md` 2026-09-04。
- 验证工具：`bench/` 提供真实 Runtime 的单次配对 `bench/run.sh`、重复聚合 `bench/repeat.sh`、正确性 `bench/stress.sh`；根模拟器的性能门使用 `bench_sim_test.go`。协议见 S04，源码入口见 [M03](../impl/M03-bench-map.md)。

## 边界

- 生成物（各 toolchain 的 `bin/`、`pkg/`，`bench/bin/`、`bench/results/` 等）不入库，以 `.gitignore` 为准。候选 fork 用 `just build-toolchain` 重建；官方对照工具链需另行准备，benchmark recipes 生成负载二进制与测量结果。
