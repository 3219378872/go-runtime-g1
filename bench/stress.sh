#!/usr/bin/env bash
# Correctness stress: run the diagnostic-trace configuration repeatedly on
# several workload scenarios and report how many runs stayed clean. A run is
# clean when the workload exits 0 and its gctrace output contains no fault
# markers (bad pointer, thrown diagnostics, rewrite misses, SIGSEGV).
set -uo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
rounds=${STRESS_ROUNDS:-3}
duration=${STRESS_DURATION:-10s}
scenarios=${STRESS_SCENARIOS:-"frag pointer64 alloc"}
godebug=${STRESS_GODEBUG:-"gctrace=1,g1gc=1,g1evac=4,g1trace=1"}
result_dir=${STRESS_RESULT_DIR:-"$repo_dir/bench/results/stress"}
candidate_root=${CANDIDATE_ROOT:-"$repo_dir/toolchain/go-g1-1270-src"}
candidate_go=${CANDIDATE_GO:-"$candidate_root/bin/go"}
gomaxprocs=${GOMAXPROCS_VALUE:-2}
cpu_list=${CPU_LIST:-0,2}
workers=${WORKERS:-2}
live=${LIVE_ROOTS:-1024}

mkdir -p "$result_dir"
if [[ ! -x "$candidate_go" ]]; then
	printf '%s\n' "candidate toolchain not found: $candidate_go" >&2
	exit 2
fi

# Build the workload binary once with the candidate toolchain. The stress run
# exercises the candidate only; it is not a paired comparison.
workload_bin=$repo_dir/bench/bin/workload-stress
build_attempt=0
until env -u GODEBUG GOROOT="$candidate_root" "$candidate_go" \
		build -trimpath -o "$workload_bin" "$repo_dir/bench/workload"; do
	build_attempt=$((build_attempt + 1))
	if (( build_attempt >= 4 )); then
		printf '%s\n' "workload build failed after retries" >&2
		exit 2
	fi
	sleep $((build_attempt * 5))
done

# Strings whose presence in the trace output marks the run as unsafe. Keep in
# sync with the diagnostics thrown by g1gcValidateIncremental,
# g1gcVerifyFullRewrite, and the runtime's own heap-safety checks.
fault_re='bad pointer|found pointer|rewrite missed|accounting drifted|SIGSEGV|invalid free|throw\('

overall=0
for scenario in $scenarios; do
	pass=0
	for ((round = 1; round <= rounds; round++)); do
		run_dir="$result_dir/$scenario-round$round"
		mkdir -p "$run_dir"
		if env -u GODEBUG GOMAXPROCS="$gomaxprocs" GOGC=100 "GODEBUG=$godebug" \
			taskset -c "$cpu_list" "$workload_bin" \
				-scenario="$scenario" -duration="$duration" -workers="$workers" \
				-live="$live" -batch=128 -size=256 \
			>"$run_dir/stdout.log" 2>"$run_dir/trace.log"; then
			exit_code=0
		else
			exit_code=$?
		fi
		if grep -Eq "$fault_re" "$run_dir/trace.log" "$run_dir/stdout.log"; then
			status=FAULT
		elif (( exit_code != 0 )); then
			status=EXIT-$exit_code
		else
			status=clean
		fi
		printf '%-10s round %d/%d: %s\n' "$scenario" "$round" "$rounds" "$status"
		if [[ "$status" != clean ]]; then
			overall=1
		else
			pass=$((pass + 1))
		fi
	done
	printf '%-10s: %d/%d clean\n' "$scenario" "$pass" "$rounds"
done

exit $overall
