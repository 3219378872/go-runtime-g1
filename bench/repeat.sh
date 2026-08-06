#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
repeats=${REPEATS:-5}
label=${LABEL:-collection-set}
root_dir=${REPEAT_RESULT_DIR:-"$repo_dir/bench/results/repeated"}
godebug=${GODEBUG_VALUE:-gctrace=1}
order=${ORDER:-alternate}

if [[ "$repeats" -lt 1 ]]; then
	printf '%s\n' "REPEATS must be positive" >&2
	exit 2
fi

case "$order" in
	official-first|candidate-first|alternate) ;;
	*)
		printf '%s\n' "ORDER must be official-first, candidate-first, or alternate" >&2
		exit 2
		;;
esac

mkdir -p "$root_dir"
BUILD_ONLY=1 GODEBUG_VALUE="$godebug" "$repo_dir/bench/run.sh"
official_files=()
candidate_files=()
for ((run = 1; run <= repeats; run++)); do
	run_dir="$root_dir/$label-$run"
	mkdir -p "$run_dir"
	run_order="$order"
	if [[ "$order" == alternate ]]; then
		if (( run % 2 == 1 )); then
			run_order=official-first
		else
			run_order=candidate-first
		fi
	fi
	printf 'run_order=%s\n' "$run_order" >"$run_dir/order.txt"
	SKIP_BUILD=1 GODEBUG_VALUE="$godebug" RESULT_DIR="$run_dir" RUN_ORDER="$run_order" "$repo_dir/bench/run.sh" \
		>"$run_dir/run.log" 2>"$run_dir/run.error.log"
	official_files+=("$run_dir/official.json")
	candidate_files+=("$run_dir/candidate.json")
done

aggregate() {
	jq -s '
		def summarize($field):
			map(.[$field]) | sort as $values |
			{
				min: $values[0],
				median: $values[((($values | length) - 1) / 2) | floor],
				p95: $values[((($values | length) - 1) * 0.95) | ceil],
				max: $values[-1]
			};
		{
			throughput_ops_s: summarize("operations_per_second"),
			total_alloc_bytes: summarize("total_alloc_bytes"),
			gc_cycles: summarize("num_gc"),
			stw_total_ns: summarize("pause_total_ns"),
			stw_max_ns: summarize("max_pause_ns"),
			stw_p99_ns: summarize("pause_p99_ns"),
			gc_cpu_fraction: summarize("gc_cpu_fraction"),
			heap_sys_bytes: summarize("heap_sys_bytes")
		}
	' "$@"
}

official_summary=$(aggregate "${official_files[@]}")
candidate_summary=$(aggregate "${candidate_files[@]}")
paired_ratios=$(jq -s --argjson runs "$repeats" '
	def summarize:
		sort as $values |
		{
			min: $values[0],
			median: $values[((($values | length) - 1) / 2) | floor],
			p95: $values[((($values | length) - 1) * 0.95) | ceil],
			max: $values[-1]
		};
	def ratio($candidate; $official; $field):
		$candidate[$field] / $official[$field];
	.[:$runs] as $official |
	.[$runs:] as $candidate |
	[
		range(0; $runs) as $run |
		{
			run: ($run + 1),
			throughput_ops_s: ratio($candidate[$run]; $official[$run]; "operations_per_second"),
			total_alloc_bytes: ratio($candidate[$run]; $official[$run]; "total_alloc_bytes"),
			gc_cycles: ratio($candidate[$run]; $official[$run]; "num_gc"),
			stw_total_ns: ratio($candidate[$run]; $official[$run]; "pause_total_ns"),
			stw_max_ns: ratio($candidate[$run]; $official[$run]; "max_pause_ns"),
			stw_p99_ns: ratio($candidate[$run]; $official[$run]; "pause_p99_ns"),
			gc_cpu_fraction: ratio($candidate[$run]; $official[$run]; "gc_cpu_fraction"),
			heap_sys_bytes: ratio($candidate[$run]; $official[$run]; "heap_sys_bytes")
		}
	] as $per_run |
	{
		per_run: $per_run,
		summary: {
			throughput_ops_s: ([$per_run[].throughput_ops_s] | summarize),
			total_alloc_bytes: ([$per_run[].total_alloc_bytes] | summarize),
			gc_cycles: ([$per_run[].gc_cycles] | summarize),
			stw_total_ns: ([$per_run[].stw_total_ns] | summarize),
			stw_max_ns: ([$per_run[].stw_max_ns] | summarize),
			stw_p99_ns: ([$per_run[].stw_p99_ns] | summarize),
			gc_cpu_fraction: ([$per_run[].gc_cpu_fraction] | summarize),
			heap_sys_bytes: ([$per_run[].heap_sys_bytes] | summarize)
		}
	}
' "${official_files[@]}" "${candidate_files[@]}")
jq -n \
	--arg label "$label" \
	--argjson runs "$repeats" \
	--argjson official "$official_summary" \
	--argjson candidate "$candidate_summary" \
	--argjson paired_ratios "$paired_ratios" \
	'{label: $label, runs: $runs, official: $official, candidate: $candidate, paired_ratios: $paired_ratios}' \
	>"$root_dir/$label.summary.json"

printf '%s\n' "$root_dir/$label.summary.json"
