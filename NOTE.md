# Current State

This checkout contains a real Go 1.25 runtime fork under
`toolchain/go-g1-src`. The root `justfile` is the supported entry point for
format checks, runtime/SSA gates, project tests, race tests, and matched
official-versus-candidate benchmarks.

## Verified

- `just fmt`
- `just check-format`
- `just verify-runtime`
- `just test-project`
- `just test-race`
- Short `just bench-trace`, `just bench-g1gcset`, and `just bench-g1evac`
  smoke runs with `REPEATS=1`, `DURATION=1s`, `GOMAXPROCS_VALUE=2`, and
  `CPU_LIST=0,2`

The trace smoke run produced positive `inbound-edges`, `evac-spans`, and
`rewrite-spans` values with `inbound-overflow=0` and no runtime fault. Its
single-run ratios are diagnostic only: throughput was `0.978x`, STW total
`1.299x`, STW max `1.219x`, and STW p99 `1.370x` versus official Go.

## Known Limits

The runtime fork has not reached stable performance parity. Single-run smoke
results are not a completion claim; use alternating repeated runs and inspect
median and spread for throughput, STW total/max/p99, and GC CPU fraction.

Generated toolchain output and benchmark history are intentionally excluded
from Git. Recreate them with `just build-toolchain` and the benchmark recipes.
