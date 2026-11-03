#!/usr/bin/env bash
# strict-readiness.sh 参数负例：非法用法必须退出 2，且在任何 docker/构建动作之前（用空 PATH 的 docker 不可用也不影响）。
set -u
S="$(cd "$(dirname "$0")" && pwd)/strict-readiness.sh"
fail=0
while IFS= read -r line; do
  # shellcheck disable=SC2086
  out="$(bash "$S" $line </dev/null 2>&1)"; rc=$?
  if [ "$rc" -ne 2 ] || printf '%s' "$out" | grep -q 'run id:'; then echo "FAIL [$line] rc=$rc"; fail=1; else echo "ok   [$line] rc=2"; fi
done <<'CASES'
--sustain
--n=100
--n
--n abc
--n 0
--n 3001
--n -5
--scenarios
--scenarios bogus
--scenarios --n
--scenarios normal,
--scenarios normal,,oom
--sustained --bogus
dirA dirB
--soak --soak-smoke
--soak-smoke --soak
--soak --soak
--scenarios soak
--scenarios contract,soak
--scenarios contract,bogus
CASES
exit $fail
