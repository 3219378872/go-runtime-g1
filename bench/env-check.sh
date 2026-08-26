#!/usr/bin/env bash
# Pre-flight gate for matched benchmarks.
#
# Verifies the conditions the measurement protocol depends on before a
# multi-minute comparison burns wall clock: benchmark cores online and (on
# bare metal) kernel-isolated, no hypervisor steal during a sampled window,
# tolerable runqueue pressure, and a performance governor where one exists.
# Hard failures (offline CPUs, steal above threshold) exit nonzero; the rest
# are warnings because virtualized hosts cannot satisfy them by construction.
#
# Environment:
#   CPU_LIST        cores the benchmark will pin to (default "0")
#   MAX_STEAL_PCT   fail above this steal percentage (default 1.0)
#   SAMPLE_SECONDS  steal sampling window (default 2)
set -euo pipefail

cpu_list=${CPU_LIST:-0}
max_steal_pct=${MAX_STEAL_PCT:-1.0}
sample_seconds=${SAMPLE_SECONDS:-2}

fail=0
note() { printf 'env-check: %s\n' "$*"; }
ok() { printf 'env-check: [ ok ] %s\n' "$*"; }
warn() { printf 'env-check: [ warn ] %s\n' "$*"; }
die() { printf 'env-check: [ FAIL ] %s\n' "$*"; fail=1; }

nproc_all=$(nproc)

# Benchmark cores must exist and be online.
IFS=',' read -ra cpus <<<"$cpu_list"
for cpu in "${cpus[@]}"; do
	if [[ ! -d "/sys/devices/system/cpu/cpu$cpu" ]]; then
		die "cpu $cpu does not exist (host has $nproc_all)"
		continue
	fi
	if [[ -e "/sys/devices/system/cpu/cpu$cpu/online" ]] &&
		[[ "$(cat "/sys/devices/system/cpu/cpu$cpu/online")" != 1 ]]; then
		die "cpu $cpu is offline"
	fi
done
[[ $fail -eq 0 ]] && ok "benchmark cores online: $cpu_list"

# Kernel-isolated cores keep unrelated migratory work off the benchmark
# pair; unisolated pins still help but share cores with whatever the
# scheduler colocates there.
isolated=$(cat /sys/devices/system/cpu/isolated 2>/dev/null || true)
if [[ -z "$isolated" ]]; then
	warn "no isolated CPUs (boot with isolcpus=nohz_full=<cores> on bare metal)"
elif [[ " $isolated " == *" ${cpu_list//,/ } "* || ","$"," == *",${cpu_list},"* ]] ||
	grep -qw "${cpus[0]}" <<<"$isolated"; then
	ok "benchmark cores inside isolated set: $isolated"
else
	warn "benchmark cores not in isolated set ($isolated)"
fi

# Scaledown governors change cycle counts mid-run; require performance mode
# where cpufreq exists at all (most VMs hide it).
gov="/sys/devices/system/cpu/cpu${cpus[0]}/cpufreq/scaling_governor"
if [[ -e "$gov" ]]; then
	current=$(cat "$gov")
	if [[ "$current" == performance ]]; then
		ok "cpufreq governor: performance"
	else
		warn "cpufreq governor is '$current' (want performance); try: cpupower frequency-set -g performance"
	fi
else
	note "no cpufreq exposed (virtualized host) — frequency scaling not verifiable here"
fi

# Hypervisor steal directly corrupts both sides of a paired comparison at
# unpredictable moments; sample the delta rather than trusting a snapshot.
steal_snap() {
	awk '/^cpu / {printf "%s %s\n", $9, $2+$3+$4+$5+$6+$7+$8+$9+$10+$11}' /proc/stat
}
read -r steal0 total0 <<<"$(steal_snap)"
sleep "$sample_seconds"
read -r steal1 total1 <<<"$(steal_snap)"
d_total=$((total1 - total0))
d_steal=$((steal1 - steal0))
if ((d_total <= 0)); then
	die "could not sample /proc/stat"
elif awk -v s="$d_steal" -v t="$d_total" -v m="$max_steal_pct" 'BEGIN {exit !(t > 0 && 100 * s / t > m)}'; then
	die "hypervisor steal above ${max_steal_pct}% during ${sample_seconds}s window ($(awk -v s="$d_steal" -v t="$d_total" 'BEGIN {printf "%.2f", 100 * s / t}')%)"
else
	ok "hypervisor steal $(awk -v s="$d_steal" -v t="$d_total" 'BEGIN {printf "%.2f", 100 * s / t}')% <= ${max_steal_pct}%"
fi

# Runqueue pressure from co-tenants. The guest cannot see host load, so this
# is a weak signal on VMs — advisory everywhere.
read -r l1 l5 _ </proc/loadavg
threshold=$(awk -v n="$nproc_all" 'BEGIN {printf "%.2f", n * 0.75}')
if awk -v l="$l5" -v t="$threshold" 'BEGIN {exit !(l > t)}'; then
	warn "loadavg 5s=$l5 above $threshold of $nproc_all CPUs — results may carry co-tenant noise"
else
	ok "loadavg 5s=$l5 within $threshold of $nproc_all CPUs"
fi

# Virtualization caps the achievable noise floor; say so once instead of
# letting every surprising median become a debugging session.
if grep -qi microsoft /proc/version 2>/dev/null ||
	grep -q hypervisor /proc/cpuinfo 2>/dev/null; then
	warn "virtualized host (WSL/VM): expect +/-3-5% session drift; sub-3% effects need longer durations or bare metal"
fi

if ((fail != 0)); then
	printf 'env-check: FAILED\n' >&2
	exit 1
fi
printf 'env-check: passed\n'
