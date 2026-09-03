# I02 范围划分

ID: I02。上层：无。下游：S01, S04。

## 三块范围（由原 README/NOTE/REBASE 迁移）

- 根模拟包（`collect.go, heap.go, mark.go, policy.go, region.go, types.go, validate.go`）：算法验证与行为回归，可 O(heap)。入口 `README.md:21-26,61-65`（`go test` + `cmd/g1gc-demo`）。
- 真实 fork（`toolchain/go-g1-1270-src`，go1.27.0 基；另存 `go-g1-1266-src`/`go-g1-1261-src` 备查）：17 个改动文件 + `g1gc.go` / `g1gc_evacuate.go` 两个新建文件。清单见 M02，过程由 `REBASE-1.27.md:1-56` 与 `REBASE-1.26.md:1-18,64-116` 迁移。
- `bench/`：唯一裁决器。单次配对 `run.sh` + 重复聚合 `repeat.sh` + 正确性 `stress.sh`。用法由 `bench/README.md:1-57` 与 `README.md:43-59,67-96`（benchmark environment 四步）迁移，详见 S04/M03。

## 边界

- 生成物（`toolchain/*/bin,pkg`、`bench/results` JSON）不入库，用 `just build-toolchain` 与 bench recipes 重建（`NOTE.md:708-709`）。
