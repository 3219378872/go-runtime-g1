set shell := ["bash", "-euo", "pipefail", "-c"]

bootstrap_root := env_var_or_default("GOROOT_BOOTSTRAP", "/usr/local/go")
# Keep this absolute: a relative GOROOT breaks tool resolution once the go
# command changes working directory mid-build.
candidate_root := env_var_or_default("CANDIDATE_ROOT", justfile_directory() / "toolchain/go-g1-1270-src")
candidate_go := candidate_root + "/bin/go"
g1gc_godebug := env_var_or_default("GODEBUG_VALUE", env_var_or_default("G1GC_GODEBUG", "gctrace=1,g1gc=1"))
g1gcset_godebug := env_var_or_default("GODEBUG_VALUE", env_var_or_default("G1GCSET_GODEBUG", "gctrace=1,g1gc=1,g1gcset=1"))
g1evac_godebug := env_var_or_default("GODEBUG_VALUE", env_var_or_default("G1EVAC_GODEBUG", "gctrace=1,g1gc=1,g1evac=1"))
trace_godebug := env_var_or_default("GODEBUG_VALUE", env_var_or_default("G1TRACE_GODEBUG", "gctrace=1,g1gc=1,g1evac=1,g1trace=1"))
g1gc_label := env_var_or_default("LABEL", "g1gc")
g1gcset_label := env_var_or_default("LABEL", "collection-set")
g1evac_label := env_var_or_default("LABEL", "evacuation")
trace_label := env_var_or_default("LABEL", env_var_or_default("G1TRACE_LABEL", "g1-trace"))
summary_label := env_var_or_default("LABEL", "collection-set")
smoke_duration := env_var_or_default("SMOKE_DURATION", "1s")

project_go_files := "collect.go g1gc_test.go heap.go mark.go policy.go region.go types.go validate.go cmd/g1gc-demo/main.go bench/compare/main.go bench/workload/main.go"
fork_go_files := "toolchain/go-g1-1270-src/src/runtime/mheap.go toolchain/go-g1-1270-src/src/runtime/mgcmark.go toolchain/go-g1-1270-src/src/runtime/mgcmark_greenteagc.go toolchain/go-g1-1270-src/src/runtime/mwbbuf.go toolchain/go-g1-1270-src/src/runtime/mbitmap.go toolchain/go-g1-1270-src/src/runtime/atomic_pointer.go toolchain/go-g1-1270-src/src/runtime/g1gc.go toolchain/go-g1-1270-src/src/runtime/g1gc_evacuate.go toolchain/go-g1-1270-src/src/cmd/compile/internal/ssa/writebarrier.go toolchain/go-g1-1270-src/src/cmd/compile/internal/liveness/plive.go"

default: verify

# Check host tools and the bootstrap compiler before starting a long run.
check-tools:
    command -v go >/dev/null
    command -v gofmt >/dev/null
    command -v jq >/dev/null
    command -v taskset >/dev/null
    test -x "{{bootstrap_root}}/bin/go"

# Format the project and the runtime fork files touched by this work.
fmt:
    gofmt -w {{project_go_files}} {{fork_go_files}}

# Fail without changing files when a tracked Go source file needs formatting.
check-format:
    test -z "$(gofmt -l {{project_go_files}} {{fork_go_files}})"

# Rebuild the real runtime fork with the system Go bootstrap toolchain.
build-toolchain:
    cd {{candidate_root}}/src && env GOROOT_BOOTSTRAP="{{bootstrap_root}}" ./make.bash

# Keep the runtime gate narrow; the full runtime suite is not required here.
test-runtime:
    cd {{candidate_root}}/src && ../bin/go test runtime -run 'TestUnsafePoint|TestGcSys' -count=1

test-ssa:
    cd {{candidate_root}}/src && ../bin/go test cmd/compile/internal/ssa -run 'Test' -count=1

test-project:
    {{candidate_go}} test . ./bench/... ./cmd/...

test-race:
    {{candidate_go}} test -race .

# Build and run only the runtime and compiler integration gates.
verify-runtime: check-tools build-toolchain test-runtime test-ssa

# Run a short matched workload smoke test.
bench-smoke:
    CANDIDATE_ROOT="{{candidate_root}}" DURATION="{{smoke_duration}}" ./bench/run.sh

# Run one matched official-versus-candidate workload comparison.
bench:
    CANDIDATE_ROOT="{{candidate_root}}" ./bench/run.sh

# Run an alternating repeated comparison and write the aggregate summary.
bench-repeat:
    CANDIDATE_ROOT="{{candidate_root}}" ./bench/repeat.sh

# Run repeated collection accounting with the G1 collector enabled.
bench-g1gc:
    CANDIDATE_ROOT="{{candidate_root}}" GODEBUG_VALUE="{{g1gc_godebug}}" LABEL="{{g1gc_label}}" ./bench/repeat.sh

# Run repeated collection-set measurements and write median/spread data.
bench-g1gcset:
    CANDIDATE_ROOT="{{candidate_root}}" GODEBUG_VALUE="{{g1gcset_godebug}}" LABEL="{{g1gcset_label}}" ./bench/repeat.sh

# Run repeated evacuation measurements and write median/spread data.
bench-g1evac:
    CANDIDATE_ROOT="{{candidate_root}}" GODEBUG_VALUE="{{g1evac_godebug}}" LABEL="{{g1evac_label}}" ./bench/repeat.sh

# Run the diagnostic trace configuration used to inspect evacuation counters.
bench-trace:
    CANDIDATE_ROOT="{{candidate_root}}" GODEBUG_VALUE="{{trace_godebug}}" LABEL="{{trace_label}}" ./bench/repeat.sh

# Print the latest repeated comparison summary for LABEL (default: collection-set).
bench-summary:
    test -f "bench/results/repeated/{{summary_label}}.summary.json"
    jq . "bench/results/repeated/{{summary_label}}.summary.json"

# Rebuild, then run every validation gate used for the real runtime fork.
verify: check-tools check-format build-toolchain test-runtime test-ssa test-project test-race
