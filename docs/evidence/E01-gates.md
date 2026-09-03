# E01 门禁证据

ID: E01。覆盖：M01#sim, M02#fork, S01。

## 门序列（`justfile:88,129`）

`check-tools -> check-format -> build-toolchain -> test-runtime -> test-ssa -> test-project -> test-race`。

| 门 | 命令 | 记录位置 |
|---|---|---|
| check-tools | `go/gofmt/jq/taskset + $GOROOT_BOOTSTRAP/bin/go` | `just check-tools` |
| check-format | `gofmt -l` 为空（project + fork 文件） | `just check-format` |
| build-toolchain | `candidate_root/src + GOROOT_BOOTSTRAP=... ./make.bash` | `just build-toolchain` |
| test-runtime | `go test runtime -run 'TestUnsafePoint\|TestGcSys'` | `just test-runtime` |
| test-ssa | `go test cmd/compile/internal/ssa -run 'Test'` | `just test-ssa` |
| test-project | `candidate_go test . ./bench/... ./cmd/...` | `just test-project` |
| test-race | `candidate_go test -race .` | `just test-race` |
| preflight | `bench/env-check.sh`（offline 核/steal 硬失败） | `just bench-preflight` |

## 填写要求

每次贴结果时注明：日期、fork（`go-g1-1270-src`）、bootstrap、pass/fail。失败贴首个 failing test + 日志路径。

## 迁移来源与历史坑

- 门序列由 `justfile:88,129` 与 `README.md:28-41` 迁移；`default: verify`（`justfile:22`）。
- bootstrap 坑：干净 make.bash 需 `GOROOT_BOOTSTRAP=$PWD/toolchain/go-g1-1266-src`，`/usr/local/go`（1.26.6 fork 构建）会触发 version-stamp mismatch（`NOTE.md:267-271`）；`go.mod` 已到 go 1.26（`NOTE.md:639-640`）。
- `fmt` 覆盖 `project_go_files + fork_go_files`（`justfile:19-20`），`check-format` 只读不改。
