# Go Runtime G1

> 知识库入口：结构化知识见 `docs/`（意图/规格/设计/实现/证据五层，对照见 `docs/README.md#迁移对照`）。本文件保留项目入口与命令说明。

This repository contains a standalone object-heap runtime that implements the
main algorithms used by a JVM G1 collector:

- fixed-size heap regions with Eden, Survivor, Old, and humongous regions;
- root discovery through object handles;
- SATB pre-write barriers and insertion barriers during concurrent marking;
- cross-region remembered sets;
- initial-mark, concurrent-mark, remark, cleanup, and evacuation phases;
- live-object copying, forwarding, root/reference fix-up, promotion, and
  evacuation-failure retention;
- pause-budget collection-set selection and collection statistics.

The package does not replace Go's compiler/runtime collector. Go pointers cannot
be intercepted by a normal Go package, so objects are represented by
`g1gc.ObjectID` handles and all references are changed through the heap API.
The Go runtime still manages the package's metadata; G1 manages the simulated
object heap and its object graph.

Run the tests with:

```text
go test ./...
go test -race ./...
```

Run the real Go runtime fork checks with `just`:

```text
just check-tools
just check-format
just verify-runtime
just verify
```

`just fmt` formats the project and the runtime fork files touched by this
work. `just verify-runtime` rebuilds the fork and runs only the targeted
runtime and SSA gates. `just verify` additionally runs project and race tests.
Override the bootstrap toolchain with
`GOROOT_BOOTSTRAP=/path/to/go just build-toolchain` when needed.

Use the prepared benchmark commands for matched comparisons:

```text
just bench-smoke
DURATION=10s GOMAXPROCS_VALUE=2 CPU_LIST=0,2 SCENARIO=pointer64 just bench

REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-g1gcset
REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-g1evac
REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-trace
LABEL=collection-set just bench-summary
```

`bench-g1gc`, `bench-g1gcset`, `bench-g1evac`, and `bench-trace` select the
corresponding diagnostic `GODEBUG` defaults and still accept an explicit
`GODEBUG_VALUE` or `LABEL`. All workload variables are passed through to the
existing scripts. Single-run results are useful for smoke checks; use a
repeated command and inspect its median and spread for performance comparisons.

Run the small workload demo with:

```text
go run ./cmd/g1gc-demo
```

## Benchmark environment

A matched comparison is only as trustworthy as the host it runs on. The
workflow below encodes what the NOTE.md baselines assume.

1. Gate the host before burning wall clock:

```text
just bench-preflight          # CPU_LIST=0,2 by default
```

Hard-fails on offline benchmark cores or hypervisor steal above budget
(MAX_STEAL_PCT); warns about missing CPU isolation, non-performance
governors, runqueue pressure, and virtualization.

2. On bare metal, isolate the benchmark pair from scheduler noise: boot
with `isolcpus=nohz_full=<cores> rcu_nocbs=<cores>`, set the cpufreq
governor to `performance`, and keep IRQs (irqbalance) off those cores.
On VMs/WSL none of this is available; expect ±3-5% session-to-session
throughput drift and treat sub-3% effects as unmeasurable there.

3. Produce the standing reference matrix under the measurement protocol
(15s runs, n=7 alternating, in-tree official go1.27.0):

```text
LABEL_PREFIX=mylabel just bench-matrix
```

4. Interpret memory via `rss_avg_mb` / `rss_final_mb` from paired runs;
`rss_max_mb` amplifies transient host events and is diagnostic only.
