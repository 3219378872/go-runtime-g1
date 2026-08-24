#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
official_go=${OFFICIAL_GO:-/usr/local/go/bin/go}
# Keep the default in sync with CANDIDATE_ROOT in the justfile; CANDIDATE_GO
# still wins when a caller needs a specific binary.
candidate_root=${CANDIDATE_ROOT:-"$repo_dir/toolchain/go-g1-1266-src"}
candidate_go=${CANDIDATE_GO:-"$candidate_root/bin/go"}
duration=${DURATION:-5s}
gomaxprocs=${GOMAXPROCS_VALUE:-2}
gogc=${GOGC_VALUE:-100}
scenario=${SCENARIO:-pointer64}
workers=${WORKERS:-2}
live=${LIVE_ROOTS:-1024}
batch=${BATCH:-128}
size=${ALLOC_SIZE:-256}
cpu_list=${CPU_LIST:-0}
godebug=${GODEBUG_VALUE:-gctrace=1}
gomemlimit=${GOMEMLIMIT_VALUE:-}
run_order=${RUN_ORDER:-official-first}
skip_build=${SKIP_BUILD:-0}
build_only=${BUILD_ONLY:-0}
result_dir=${RESULT_DIR:-"$repo_dir/bench/results"}

case "$run_order" in
	official-first|candidate-first) ;;
	*)
		printf '%s\n' "RUN_ORDER must be official-first or candidate-first" >&2
		exit 2
		;;
esac

mkdir -p "$repo_dir/bench/bin" "$result_dir"

build() {
	local go_bin=$1
	local output=$2
	local attempt
	# A caller may enable candidate-only runtime diagnostics for the workload.
	# Keep those settings out of toolchain compilation, where the compiler itself
	# is a runtime workload and evacuation is not supported during the build.
	# Toolchain executables occasionally fail to spawn right after a fresh
	# make.bash install on some filesystems, so retry a few times.
	for attempt in 1 2 3 4; do
		if env -u GODEBUG GOROOT="$(dirname "$(dirname "$go_bin")")" "$go_bin" \
			build -trimpath -o "$output" "$repo_dir/bench/workload"; then
			return 0
		fi
		for tool in "$(dirname "$(dirname "$go_bin")")"/pkg/tool/linux_amd64/*; do
			cat "$tool" >/dev/null 2>&1 || true
		done
		sleep $((attempt * 5))
	done
	return 1
}

run_one() {
	local name=$1
	local binary=$2
	local output=$3
	local trace=$4
	local -a environment=("GOMAXPROCS=$gomaxprocs" "GOGC=$gogc" "GODEBUG=$godebug")
	if [[ -n "$gomemlimit" ]]; then
		environment+=("GOMEMLIMIT=$gomemlimit")
	fi
	env "${environment[@]}" taskset -c "$cpu_list" "$binary" \
			-scenario="$scenario" -duration="$duration" -workers="$workers" \
			-live="$live" -batch="$batch" -size="$size" \
			>"$output" 2>"$trace"
	}

if [[ "$skip_build" != 1 ]]; then
	build "$official_go" "$repo_dir/bench/bin/workload-official"
	if [[ -x "$candidate_go" ]]; then
		build "$candidate_go" "$repo_dir/bench/bin/workload-candidate"
	fi
fi

if [[ -x "$candidate_go" ]]; then
	if [[ "$build_only" == 1 ]]; then
		exit 0
	fi
	if [[ ! -x "$repo_dir/bench/bin/workload-official" || ! -x "$repo_dir/bench/bin/workload-candidate" ]]; then
		printf '%s\n' "benchmark binaries are missing; run without SKIP_BUILD=1 first" >&2
		exit 2
	fi
	case "$run_order" in
		official-first)
			run_one official "$repo_dir/bench/bin/workload-official" \
				"$result_dir/official.json" "$result_dir/official.gctrace"
			run_one candidate "$repo_dir/bench/bin/workload-candidate" \
				"$result_dir/candidate.json" "$result_dir/candidate.gctrace"
			;;
		candidate-first)
			run_one candidate "$repo_dir/bench/bin/workload-candidate" \
				"$result_dir/candidate.json" "$result_dir/candidate.gctrace"
			run_one official "$repo_dir/bench/bin/workload-official" \
				"$result_dir/official.json" "$result_dir/official.gctrace"
			;;
	 esac
	"$official_go" run "$repo_dir/bench/compare" \
		"$result_dir/official.json" "$result_dir/candidate.json"
	else
	printf '%s\n' "candidate toolchain not found: $candidate_go" >&2
	fi
