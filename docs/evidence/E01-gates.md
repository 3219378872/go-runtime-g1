# E01 门禁证据

ID: E01。覆盖：[M01](../impl/M01-sim-map.md#sim)、[M02](../impl/M02-fork-map.md#fork)、S01/S04。

<a id="verify"></a>

## 当前门序列（以 justfile recipe 为准）

`check-tools -> check-format -> build-toolchain -> test-runtime -> test-ssa -> test-project -> test-race`。

| 门 | 命令 | 记录位置 |
|---|---|---|
| check-tools | `go/gofmt/jq/taskset + $GOROOT_BOOTSTRAP/bin/go` | `just check-tools` |
| check-format | `gofmt -l` 为空（project + fork 文件） | `just check-format` |
| build-toolchain | `candidate_root/src + GOROOT_BOOTSTRAP=... ./make.bash` | `just build-toolchain` |
| test-runtime | `go test runtime -run 'TestUnsafePoint\|TestGcSys'` | `just test-runtime` |
| test-ssa | `go test cmd/compile/internal/ssa -run 'Test'` | `just test-ssa` |
| <a id="test-project"></a>test-project | `candidate_go test . ./bench/... ./cmd/...` | `just test-project` |
| test-race | `candidate_go test -race .` | `just test-race` |
| <a id="preflight"></a>preflight | `bench/env-check.sh`（不存在/offline 核、steal 超限硬失败） | `just bench-preflight` |

<a id="verify-runtime"></a>

`just verify-runtime` 组合 `check-tools/build-toolchain/test-runtime/test-ssa`；`just verify` 另含格式、项目和 race 门。preflight 是独立的性能前置门，未包含在 `verify` 中。

## 填写要求

每次贴结果时注明：日期、被验证源码提交、fork（`go-g1-1270-src`）、bootstrap、pass/fail。失败贴首个 failing test + 日志路径。列出命令或引用旧迭代不等于当前提交通过；纯文档任务按 `AGENTS.md` 检查路径、命令和 diff，Runtime/性能门可记 N/A 并说明理由。

## 迁移来源与历史坑

- 门序列来自 `justfile`；默认 recipe 为 `verify`。
- bootstrap 历史：`NOTE.md` 2026-08-25 记录当时 `/usr/local/go` 的 version-stamp mismatch，改用已构建的 `go-g1-1266-src` 通过。当前 `justfile` 默认 `GOROOT_BOOTSTRAP=/usr/local/go`，可覆盖为有效 bootstrap 根目录；旧树不保证带有生成的 `bin/go`，不能把当时的 workaround 写成通用前提。根模块当前声明 `go 1.26`。
- `fmt/check-format` 使用 `justfile` 中显式列出的 `project_go_files + fork_go_files`；`check-format` 只读不改。具体覆盖以该列表为准。

## 文档校准记录（2026-09-19）

- 核对源码基线：`be8f113a`，fork 为 `go-g1-1270-src`（go1.27.0）。本次只修改 Markdown，bootstrap 不适用。
- 通过：`./bench/check-trace.sh`、`git diff --check`、`just --list`、文档中引用 recipe 的 `just --dry-run` 和 shell 示例的 `bash -n` 解析。
- 通过：只读检查本地 Markdown 链接/显式锚点及 `文件#符号` 引用；M02 九个核心文件、E03 的 21 个测试名称、18 个知识 ID 与当前源码/矩阵一致。区域大小、疏散上限和选择目标由源码常量核对。
- Runtime 构建/测试、stress、性能门：N/A。按 `AGENTS.md` 的纯文档例外，本次未改变 Go、脚本或构建配置的执行路径；此记录不提供新的 Runtime 正确性或性能证据。原始变更说明追加在 `NOTE.md` 2026-09-19。
