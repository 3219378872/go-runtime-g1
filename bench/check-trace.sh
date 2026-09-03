#!/usr/bin/env bash
# 检查 docs/ 五层 ID 在 TRACEABILITY.md 中可达（悬空即失败）。
# M 行号漂移只告警，不失败。用: just check-format 旁手动跑 ./bench/check-trace.sh
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
trace="$repo/docs/TRACEABILITY.md"
fail=0
for id in $(grep -rhoE '\b[ISDME]0[0-9]\b' "$repo/docs" | sort -u); do
  if ! grep -q "$id" "$trace"; then
    echo "MISSING in TRACEABILITY.md: $id" >&2
    fail=1
  fi
done
# M file:line 存在性抽查（只告警）
while read -r ref; do
  f="${ref%%:*}"
  candidates=("$repo/$f" "$repo/toolchain/go-g1-1270-src/src/runtime/$f" "$repo/$f")
  found=0
  for c in "${candidates[@]}"; do
    if [[ -e "$c" ]]; then found=1; break; fi
  done
  if [[ $found -eq 0 ]]; then
    echo "WARN missing file: $ref" >&2
  fi
done < <(grep -rhoE '[a-zA-Z0-9_./-]+\.go:[0-9]+' "$repo/docs" | sort -u)
exit $fail
