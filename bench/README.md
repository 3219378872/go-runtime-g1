# Runtime benchmark harness

> 迁移状态：协议与组件索引已迁移至 `docs/spec/S04-bench-protocol.md` 与 `docs/impl/M03-bench-map.md`。本文件保留 harness 说明，行为以脚本为准。

The workload is compiled twice from the same source: once with the official
Go toolchain and once with a candidate GOROOT. Both runs use the same
`GOMAXPROCS`, `GOGC`, `GOMEMLIMIT`, CPU affinity, duration, scenario, worker
count, live-set size, and allocation batch.

Build and run the official baseline:

```text
OFFICIAL_GO=/usr/local/go/bin/go DURATION=10s GOMAXPROCS_VALUE=2 \
  CPU_LIST=0,2 SCENARIO=pointer64 ./bench/run.sh
```

When a candidate toolchain exists at `toolchain/go-g1-src/bin/go`, the same command
also builds and runs it, writes both JSON measurements and `gctrace` logs under
`bench/results`, and prints ratios. `PauseTotalNs` comes from
`runtime.MemStats`; max and p99 pause values come from the recent GC pause
history. Set `GODEBUG_VALUE=gctrace=1,g1gc=1` for accounting, or add
`g1gcset=1` to enable collection-set sweep priority. Set `REPEATS` through a
repeatable batch command; a single run is not a performance claim, so report
median and spread. Use separate physical cores rather than SMT siblings; on
the reference host CPUs 0 and 1 share a core, while 0 and 2 do not.

Use `g1trace=1` when the experimental per-cycle G1 fields are needed in the
`gctrace` log; keeping it off avoids charging diagnostic output to throughput.

The repository `justfile` exposes the common checks and benchmark modes:

```text
just check-tools
just check-format
just verify-runtime
just verify

just bench-smoke
just bench
REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-g1gcset
REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-g1evac
REPEATS=5 GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s just bench-trace
LABEL=collection-set just bench-summary
```

`bench-g1gc`, `bench-g1gcset`, `bench-g1evac`, and `bench-trace` only provide
diagnostic defaults; `GODEBUG_VALUE`, `LABEL`, and the workload variables can
still be overridden from the environment.

For a reproducible batch, the underlying script can also be run directly:

```text
REPEATS=5 LABEL=collection-set GODEBUG_VALUE=gctrace=1,g1gc=1,g1gcset=1 \
  GOMAXPROCS_VALUE=2 CPU_LIST=0,2 DURATION=3s ./bench/repeat.sh
```

The summary is written to `bench/results/repeated/<label>.summary.json`. It
includes independent runtime distributions and per-run candidate/official
ratios so alternating run order can expose host drift.
