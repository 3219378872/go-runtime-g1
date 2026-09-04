# AGENTS.md

本文件适用于整个仓库。所有 Agent 在开始任务前先读本文件，再按任务类型进入对应知识文档。不要把历史 benchmark、旧 fork 结果或一次 smoke 当成当前证据。

## 仓库介绍

本仓库研究 G1 思路在 Go 中的实现与验证，包含三个边界不同的部分：

- 根目录 Go 包是独立的对象堆模拟器。对象通过 `ObjectID` 句柄访问，用于验证 region、标记、remembered set、collection set 和 evacuation 等算法；它不替换 Go Runtime GC。
- `toolchain/go-g1-1270-src/` 是基于 Go 1.27.0 的真实 Runtime fork，是当前运行时实现。`go-g1-1266-src/`、`go-g1-1261-src/` 等旧树只用于追溯和 rebase 参考。
- `bench/` 是正确性压力测试和性能比较入口。性能结论必须来自同条件、重复、交替顺序的 main/task 或 official/candidate 配对运行。

根目录 `justfile` 是构建、测试和 benchmark 命令的真相源。代码、脚本和当前配置优先于说明文档；文档与实现不一致时，先查明差异并同步修正，不能凭旧结论猜测。

以下内容是生成物，不得提交：各 toolchain 的 `bin/`、`pkg/`，`bench/bin/`、`bench/results/`，覆盖率文件及本地编辑器配置。以 `.gitignore` 为准。

## 知识库索引

知识链为 `意图 -> 规格 -> 设计 -> 实现 <-> 证据`：

| 任务 | 入口 |
|---|---|
| 项目总览、阅读顺序、编号规则 | `README.md`、`docs/README.md` |
| 跨层关系和覆盖情况 | `docs/TRACEABILITY.md`，这是唯一可信的跨层矩阵 |
| 为什么做、边界、成功标尺 | `docs/intent/I01-why-g1.md`、`I02-scope.md`、`I03-success-bar.md` |
| GC 行为、安全、暂停预算、测量协议 | `docs/spec/S01-collect-cycle.md` 至 `S04-bench-protocol.md` |
| 算法与权衡 | `docs/design/D01-region-accounting.md` 至 `D05-region-alloc.md` |
| 模拟器、Runtime fork、benchmark 源码地图 | `docs/impl/M01-sim-map.md` 至 `M03-bench-map.md` |
| 门禁、性能矩阵、正确性压力证据 | `docs/evidence/E01-gates.md` 至 `E03-correctness-stress.md` |
| benchmark 使用和参数 | `bench/README.md`、`bench/run.sh`、`bench/repeat.sh`、`bench/stress.sh` |
| 当前迭代原始记录 | `NOTE.md`；仅追加，不以历史数字替代当前验证 |
| 上游 Go 版本迁移 | `REBASE-1.27.md`、`REBASE-1.26.md` |

改动行为契约、算法、源码位置或性能结论时，必须同步更新对应的 `S/D/M/E` 文档和 `docs/TRACEABILITY.md`。新增知识 ID 后运行 `./bench/check-trace.sh`；实现引用尚未接受的设计时，必须明确标注状态，不能把提案写成已验证事实。

## 强制工作流

### 1. 从最新 main 创建独立 worktree

禁止直接在 main 工作树实现任务。先确认 main 没有未提交改动；发现他人改动时停止，不得清理、覆盖或暂存它们。

```bash
git switch main
git status --short --branch
git fetch origin
git pull --ff-only origin main

main_tree="$(git rev-parse --show-toplevel)"
task_slug="short-task-name" # 替换为本任务的简短英文名
task_branch="task/$task_slug"
task_parent="$(dirname "$main_tree")/go-runtime-g1-worktrees"
task_tree="$task_parent/$task_slug"
mkdir -p "$task_parent"
git worktree add -b "$task_branch" "$task_tree" main
cd "$task_tree"
```

任务分支和目录必须是新建的。若同名分支或目录已存在，先检查其归属和状态，不得直接复用或删除。后续命令中的 `main_tree`、`task_tree` 和 `task_branch` 均指本步骤记录的值；进入新 shell 时先按实际绝对路径重新设置。

### 2. 在任务 worktree 完成改动

- 先阅读与任务对应的意图、规格、设计、实现和证据，再修改代码。
- 改动保持在任务边界内；不顺手重构无关代码，不改写他人的未提交内容。
- Runtime hook 必须保持 `g1gc` 等开关关闭时的官方路径语义；热路径避免新增逐对象全局原子操作和无界扫描。
- 代码行为或性能假设变化时，同一任务内更新知识链和证据。benchmark 原始结果留在 gitignored 目录，只提交可复核的摘要。

### 3. 先过正确性门禁

Go 源码改动先格式化，再运行仓库支持的门禁：

```bash
just fmt
just verify
git diff --check
```

`just verify` 依次覆盖工具检查、格式、fork 构建、定向 Runtime 测试、SSA 测试、项目测试和 race 测试。不要用 fork 内的宽泛 `go test ./...` 替代它，compiler 测试夹具包含预期的多包和失败样例。

知识库改动另跑：

```bash
./bench/check-trace.sh
```

纯文档任务应确认所有新增路径存在、示例命令可由当前 `justfile` 或脚本解析，并运行 `git diff --check`。纯文档、注释及不影响执行路径的配置变更可将性能门标为 `N/A`，说明理由后不运行无意义的 A/B。

### 4. 按影响面通过性能门控

先运行环境前置门；失败时不得继续用该主机结果裁决：

```bash
CPU_LIST=0,2 just bench-preflight
```

对 Runtime、collector、allocator、barrier 或 benchmark 执行路径的改动，分别构建 fresh main 和 task 的 `toolchain/go-g1-1270-src`，然后从任务 worktree 调用 `bench/repeat.sh`。A 端必须是 main 的 fork，B 端必须是 task 的 fork，使 summary 中 paired ratio 表示 `task/main`：

```bash
# 若进入了新 shell，先按步骤 1 记录的值设置 main_tree。
main_tree="/absolute/path/to/go-runtime-g1"
task_tree="$(git rev-parse --show-toplevel)"

(cd "$main_tree" && GOROOT_BOOTSTRAP=/usr/local/go just build-toolchain)
GOROOT_BOOTSTRAP=/usr/local/go just build-toolchain

OFFICIAL_GO="$main_tree/toolchain/go-g1-1270-src/bin/go" \
CANDIDATE_ROOT="$task_tree/toolchain/go-g1-1270-src" \
REPEATS=7 DURATION=15s GOMAXPROCS_VALUE=2 CPU_LIST=0,2 \
SCENARIO=pointer64 GODEBUG_VALUE='gctrace=0,g1gc=1,g1evac=1' \
LABEL="$task_slug-pointer64-evac" ./bench/repeat.sh
```

按改动影响面选择矩阵：

- 广泛 Runtime/collector/bench 改动运行 `pointer64`、`pointer256`、`alloc`、`frag` 四个场景，每个场景各跑 default（`gctrace=0`）和 evacuation（`gctrace=0,g1gc=1,g1evac=1`）。
- 局部改动运行所有直接相关场景，并至少包含一个 `g1gc` 关闭的 gate-off 对照。影响范围无法可靠界定时按广泛改动处理。
- 根模拟器的性能敏感改动使用 `bench_sim_test.go` 中相关 `Benchmark*`，在相同 Go、CPU 和 `GOMAXPROCS` 下交替运行 main/task 各 7 次；比较 task/main 的 `ns/op`、`B/op` 和 `allocs/op` 中位数。

Runtime 行的通过条件：

- `throughput_ops_s` paired median `>= 0.95`；
- `stw_max_ns`、`stw_p99_ns`、`gc_cpu_fraction` paired median 均 `<= 1.05`；
- `rss_avg_mb`、`rss_final_mb` 必须记录，`rss_max_mb` 仅用于诊断，不作硬门；
- 模拟器 benchmark 的 `ns/op`、`B/op`、`allocs/op` task/main 中位数均不得高于 `1.05`。

任一行越界时，交换 main/task 两端后以相同参数再跑一次，并把 ratio 归一化回 task/main。复跑仍退化，或两次结果无法裁决，都视为门控失败：不得提交、合并或宣称无回归。单次 run、短 smoke、跨 session 历史数字均不能替代此门。

### 5. 门禁通过后提交

提交前检查实际差异和未跟踪文件，只暂存本任务文件：

```bash
git status --short
git diff --check
git diff
git add path/to/changed-file # 逐项替换，只添加本任务文件
git diff --cached --check
git diff --cached
git commit -m '<scope>: <imperative summary>'
```

提交前缀沿用仓库惯例：`runtime:`、`sim:`、`bench:`、`docs:`、`chore:`。不得提交 benchmark 生成物、构建产物、临时日志或无关改动。

### 6. Rebase、快进 main 并推送

提交后先让 main 更新到远端最新状态，再把任务分支 rebase 到 main：

```bash
git -C "$main_tree" fetch origin
git -C "$main_tree" pull --ff-only origin main
git -C "$task_tree" rebase main
```

若 main 相比任务起点发生变化，必须在 rebase 后重新运行受影响的正确性和性能门控。全部通过后回到 main，只允许 fast-forward 集成：

```bash
git -C "$main_tree" merge --ff-only "$task_branch"
git -C "$main_tree" push origin main
```

禁止 force push。出现冲突、非快进、门控失败或 push 被拒绝时停止，保留现场并查明原因，不用 merge commit、强推或跳过门禁绕过问题。

### 7. 验证远端并清理

确认 `origin/main` 已指向本次提交且两个工作树都干净，再从 main 工作树清理：

```bash
cd "$main_tree"
git fetch origin
git status --short --branch
git worktree remove "$task_tree"
git branch -d "$task_branch"
git worktree prune
```

只有 push 成功且远端提交已确认后才能删除任务 worktree 和本地任务分支。
