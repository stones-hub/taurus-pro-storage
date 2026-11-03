#!/usr/bin/env bash
# 严格单机验收门禁（一条命令，失败非零）。
#
# 用法: scripts/strict-readiness.sh [--sustained] [--soak | --soak-smoke] [--scenarios a,b,...] [--n N] [基础证据目录]
#
#   默认基础目录 /tmp/taurus-pro-storage-strict，本轮证据写入其下 <run-id>/。
#   默认场景（总负载 < 3 分钟，每组 N=1000 条）：
#     normal(256B/4KiB × 0/5ms) backlog consumer-kill contract redis-sigkill oom backup-restore
#     contract = 经打包安装的公开 API 契约（错误/panic/重试/失败留存/ListFailed/ReplayFailed/去重冲突/
#                Stop 成功与超时/单次生命周期/ctx 边界/内存引擎），见 docs/release-readiness.md 覆盖表。
#   --sustained 额外跑有界短稳态：2400 条 @80/s（≈30s 生产），整个场景（提交+确认+停止+对账）
#               总硬截止 60 秒，超时即失败；不是 60 秒的纯生产窗口，也不是长稳态。
#   --soak      显式可选的 30 分钟有界长稳态（结果名 soak，默认不跑）：≤10 条/秒、≤18000 次提交、载荷 256B–4KiB、
#               8 Worker、独立私有 Redis；部分重试/panic/失败留存/重放、短 DedupWindow(2m) 墓碑到期检查；
#               10 秒连续采样（<run>/main/soak.samples.jsonl/.csv），安全阈值低于资源上限，越过即失败并停止。
#               生产窗口 30 分钟 + 有界收尾（排空 ≤3 分钟 + 2 分钟墓碑到期等待 + 停止对账）。
#               同时会先跑一次阈值负例（极低 Redis 内存阈值，必须失败并立即停止）。
#   --soak-smoke 同一 soak 代码的 180 秒工具自测（结果名 soak-smoke，窗口 30s），整轮只能是 PARTIAL，不得当作长稳态。
#   AOF ENOSPC 破坏性模式未实现，默认也不会运行。
#
#   完整 vs 部分: 只有“默认场景集合一个不少且 N=1000”才可能输出 `STRICT GATE: PASS`（退出码 0）。
#   使用 --scenarios 子集、--n 降低条数或 --soak-smoke 时，即使全部通过也只输出 `STRICT GATE: PARTIAL`（退出码 3），
#   且不得当作完整门禁结论；FULL 模式下 summary.json 必须逐项对账 10 个默认结果，缺一即失败。
#   给了 --soak 时另外要求 soak 结果为真、采样连续且生产窗口达到 1800 秒，否则整轮失败。
#   退出码: 0 完整通过 / 1 失败（含 docker 不可达、镜像缺失、清理失败）/ 2 用法错误或缺少工具 / 3 仅部分通过。
#   参数只接受上述形式（`--n N`、`--scenarios a,b`，不支持 `--n=N`）；未知选项、缺失/非法参数、
#   多个基础目录一律退出 2，不会运行门禁。
#   资源创建与登记之间若进程被 SIGKILL（或 shell 自身被 KILL）无法完全自动补救：此时按
#   <基础目录>/<run-id>/ 下 manifest.json(.history) 的精确容器ID/卷名，并核对 taurus.strict.owner/run
#   标签后手工清理；不要按前缀枚举或 prune。
#   离线账本 `gate verify` 不是抗篡改工具：账本与 expect 同在本地磁盘，有写权限者可同时改写；
#   它只证明“账本内部自洽 + 常见损坏会被发现”。
#
# 做什么:
#   1. 计算候选源码 SHA256 清单，打成 module zip，布置成本地 file:// module proxy
#      （含未跟踪源码；合成版本；局部 GOMODCACHE；不用 replace；不改根 go.mod/go.sum）
#   2. 在临时工作副本里构建独立调用模块 scripts/readiness（只用打包后的公开 API）
#   3. 单元测试：账本校验器负例、docker 归属守卫
#   4. 门禁负例（真实命令）：坏 Redis 地址、归属标签不符、注入坏账本
#   5. 真实隔离场景：私有 Redis 8.10.2 容器（固定摘要，1CPU/384MiB，127.0.0.1 随机端口）
#   6. 从磁盘账本独立重新校验；显式清单清理并逐个核验已不存在
#
# 约束: 只操作本轮自己创建的、带 owner/run 标签的容器与卷（精确完整ID）；不 prune、
# 不按前缀枚举删除、不使用默认 Redis（localhost:6379）；不 commit/push。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE_PIN="sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0"
MODULE="github.com/stones-hub/taurus-pro-storage"
DECOY_OWNER="taurus-pro-storage-strict-decoy"
DEFAULT_RESULTS="normal-256b-0ms normal-256b-5ms normal-4096b-0ms normal-4096b-5ms backlog consumer-kill contract redis-sigkill oom backup-restore"

usage_err() { echo "strict-readiness: $* (see --help)" >&2; exit 2; }
VALID_SCENARIOS=" normal backlog consumer-kill contract redis-sigkill oom backup-restore sustained soak "
SUSTAINED=""; SCENARIOS=""; NPER=""; BASE=""; SOAK=""; SOAK_NAME=""
while [ $# -gt 0 ]; do
  case "$1" in
    --sustained) SUSTAINED="--sustained 1" ;;
    --soak|--soak-smoke)
      [ -z "$SOAK" ] || usage_err "--soak and --soak-smoke are mutually exclusive and may be given once"
      if [ "$1" = "--soak" ]; then SOAK="--soak 1800"; SOAK_NAME="soak"; else SOAK="--soak 180"; SOAK_NAME="soak-smoke"; fi ;;
    --scenarios)
      [ $# -ge 2 ] && [ -n "$2" ] && [ "${2#-}" = "$2" ] || usage_err "--scenarios needs a value"
      case "$2" in ,*|*,|*,,*) usage_err "--scenarios has an empty element: '$2'" ;; esac
      IFS=, read -r -a _sc <<< "$2"
      for x in "${_sc[@]}"; do
        case "$VALID_SCENARIOS" in *" $x "*) ;; *) usage_err "unknown scenario '$x'" ;; esac
      done
      SCENARIOS="--scenarios $2"; shift ;;
    --n)
      [ $# -ge 2 ] || usage_err "--n needs a value"
      case "$2" in ''|*[!0-9]*) usage_err "--n must be an integer 1..3000, got '$2'" ;; esac
      [ "$((10#$2))" -ge 1 ] && [ "$((10#$2))" -le 3000 ] || usage_err "--n must be 1..3000, got '$2'"
      NPER="--n $((10#$2))"; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    -*) usage_err "unknown option '$1'" ;;
    *) [ -z "$BASE" ] || usage_err "more than one base directory ('$BASE', '$1')"; BASE="$1" ;;
  esac
  shift
done
case ",${SCENARIOS#--scenarios }," in *,soak,*) [ -n "$SOAK" ] || usage_err "scenario 'soak' needs --soak or --soak-smoke" ;; esac
BASE="${BASE:-/tmp/taurus-pro-storage-strict}"
RUN_ID="s$(date +%s)-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
EV="$BASE/$RUN_ID"
mkdir -p "$EV" || { echo "cannot create $EV" >&2; exit 2; }
STEPS="$EV/steps.tsv"
: > "$STEPS"
FAILED=0
GATE=""
OUTS=()          # 含 manifest 的目录，退出时逐个显式清理

log() { printf '%s\n' "$*" | tee -a "$EV/gate.log"; }
CUR_PID=""       # 当前正在执行的步骤子进程的精确 PID（只管理自己启动的这一个）
# step <name> <expected-exit> <cmd...>: 执行并核对退出码；不符则记失败（不中断，继续收集），清理仍会执行
step() {
  local name="$1" want="$2"; shift 2
  local t0=$SECONDS rc
  log "=== step: $name"
  "$@" >"$EV/step-$name.log" 2>&1 &
  CUR_PID=$!
  wait "$CUR_PID"; rc=$?
  CUR_PID=""
  tail -n 12 "$EV/step-$name.log" | sed 's/^/    /' | tee -a "$EV/gate.log" >/dev/null
  if [ "$rc" -eq "$want" ]; then
    printf '%s\tPASS\t%s\texit=%s\n' "$name" "$((SECONDS - t0))" "$rc" >> "$STEPS"
    log "    -> PASS (exit $rc, expected $want)"
  else
    printf '%s\tFAIL\t%s\texit=%s want=%s\n' "$name" "$((SECONDS - t0))" "$rc" "$want" >> "$STEPS"
    log "    -> FAIL (exit $rc, expected $want); see $EV/step-$name.log"
    FAILED=1
  fi
  return 0
}

# INT/TERM: 对当前步骤的精确 PID 先 TERM（gate 会取消场景并走显式清单清理、回收消费者子进程），
# 超时再 KILL；随后退出（EXIT trap 里的 final_cleanup 仍会执行）。不使用 pkill/killall。
on_signal() {
  local sig="$1"
  log "received SIG$sig: stopping step pid=${CUR_PID:-none} then cleaning up"
  FAILED=1
  printf 'interrupted\tFAIL\t0\tSIG%s\n' "$sig" >> "$STEPS"
  if [ -n "$CUR_PID" ] && kill -0 "$CUR_PID" 2>/dev/null; then
    kill -TERM "$CUR_PID" 2>/dev/null
    local i=0
    while kill -0 "$CUR_PID" 2>/dev/null && [ "$i" -lt 150 ]; do sleep 1; i=$((i + 1)); done
    kill -0 "$CUR_PID" 2>/dev/null && { log "step pid $CUR_PID ignored TERM for ${i}s: KILL"; kill -KILL "$CUR_PID" 2>/dev/null; }
    wait "$CUR_PID" 2>/dev/null
  fi
  exit 130
}
trap 'on_signal INT' INT
trap 'on_signal TERM' TERM

final_cleanup() {
  local rc=$? cfail=0
  # 清理期间忽略再次的 INT/TERM，保证显式清单清理跑完
  trap '' INT TERM
  # 对每个含 manifest 的目录执行显式清单清理（带归属校验），不按前缀/标签枚举
  if [ -n "$GATE" ] && [ -x "$GATE" ]; then
    for d in "${OUTS[@]:-}"; do
      [ -n "$d" ] && [ -f "$d/manifest.json" ] || continue
      local own=()
      [ "$d" = "${NEG:-}" ] && own=(--owner "$DECOY_OWNER")   # 诱饵只能用显式的 decoy owner 上下文清理
      if ! "$GATE" cleanup --out "$d" --run-id "$RUN_ID" ${own[@]+"${own[@]}"} >>"$EV/final-cleanup.log" 2>&1; then
        cfail=1
        echo "FINAL CLEANUP FAILED for $d (see $EV/final-cleanup.log); clean up by exact ids in $d/manifest.json" | tee -a "$EV/gate.log" >&2
      fi
    done
  fi
  # 清理失败要让退出码可见（不覆盖已有的非零退出码）
  if [ "$cfail" -ne 0 ] && { [ "$rc" -eq 0 ] || [ "$rc" -eq 3 ]; }; then exit 1; fi
}
trap final_cleanup EXIT

log "run id: $RUN_ID  evidence: $EV"
for tool in docker python3 go git; do
  command -v "$tool" >/dev/null 2>&1 || { log "missing tool: $tool"; exit 2; }
done
step docker-reachable 0 docker version --format '{{.Server.Version}}'
if [ "$FAILED" -ne 0 ]; then log "docker not reachable"; exit 1; fi

# ---- 1. 候选身份 ----
git -C "$ROOT" rev-parse HEAD > "$EV/git-head.txt" 2>&1
git -C "$ROOT" status --porcelain > "$EV/git-status-before.txt" 2>&1
sha256sum_() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
sha256sum_ "$ROOT/go.mod" "$ROOT/go.sum" > "$EV/root-gomod-sha256-before.txt"
VERSION="v0.0.0-strict.${RUN_ID}"
PROXY="$EV/proxy"
mkdir -p "$PROXY"
step pack-candidate 0 python3 -I "$ROOT/scripts/readiness/pack.py" "$ROOT" "$PROXY" "$VERSION" "$EV/candidate-source-sha256.txt"
sha256sum_ "$EV/candidate-source-sha256.txt" "$PROXY/$MODULE/@v/$VERSION.zip" > "$EV/candidate-identity.txt" 2>&1
log "candidate identity:"; sed 's/^/    /' "$EV/candidate-identity.txt" | tee -a "$EV/gate.log" >/dev/null
echo "version=$VERSION  files=$(wc -l < "$EV/candidate-source-sha256.txt" | tr -d ' ')" >> "$EV/candidate-identity.txt"
[ "$FAILED" -ne 0 ] && { log "packing failed"; exit 1; }

# ---- 2. 构建独立调用模块（临时副本；局部 GOMODCACHE；不污染全局缓存/根模块） ----
WORK="$EV/work"
mkdir -p "$WORK" "$EV/bin"
cp -R "$ROOT/scripts/readiness/." "$WORK/"
rm -f "$WORK/pack.py"
GLOBAL_MC="$(go env GOMODCACHE)"
export GOMODCACHE="$EV/gomodcache" GOFLAGS="-mod=mod -modcacherw" GOSUMDB=off GOTOOLCHAIN=local GONOSUMDB='*' GOWORK=off
export GOPROXY="file://$PROXY,file://$GLOBAL_MC/cache/download"
export GOMAXPROCS=2
unset REDIS_ADDR TAURUS_TEST_REDIS_ADDR TAURUS_TEST_REDIS_CONTAINER
(cd "$WORK" && go mod edit -require="$MODULE@$VERSION") >>"$EV/build.log" 2>&1
# 信任基准：tidy 前先复制候选根 go.sum，使第三方依赖哈希以候选自己的 go.sum 为准（GOSUMDB=off）
cp "$ROOT/go.sum" "$WORK/go.sum"
step go-mod-tidy 0 bash -c "cd '$WORK' && exec go mod tidy"
step go-vet 0 bash -c "cd '$WORK' && exec go vet ./..."
step go-build 0 bash -c "cd '$WORK' && exec go build -o '$EV/bin/gate' ./cmd/gate"
[ -x "$EV/bin/gate" ] || { log "gate binary not built"; exit 1; }
GATE="$EV/bin/gate"
# 记录实际解析到的候选来源
step go-list-candidate 0 bash -c "cd '$WORK' && exec go list -m -json '$MODULE' > '$EV/resolved-candidate.json'"
step resolved-candidate-identity 0 python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
assert d.get("Path")==sys.argv[2], "Path %r" % d.get("Path")
assert d.get("Version")==sys.argv[3], "Version %r" % d.get("Version")
assert not d.get("Replace"), "Replace present: %r" % d.get("Replace")
print("resolved", d["Path"], d["Version"], "no replace")
' "$EV/resolved-candidate.json" "$MODULE" "$VERSION"
# go.sum 基准：work 的 go.sum 中凡与根 go.sum 同一 模块+版本 的行，哈希必须一致（tidy 不得改写已有信任）
step go-sum-baseline-consistent 0 python3 -I -c '
import sys
def load(p):
    d={}
    for l in open(p):
        f=l.split()
        if len(f)==3: d.setdefault((f[0],f[1]),set()).add(f[2])
    return d
root,work=load(sys.argv[1]),load(sys.argv[2])
bad=[k for k in work if k in root and work[k]!=root[k]]
new=sorted(k for k in work if k not in root and k[0]!=sys.argv[3])
print("go.sum entries not in candidate root go.sum (informational):", len(new))
for k in new[:20]: print("  ", *k)
if bad: print("MISMATCH:", bad); sys.exit(1)
' "$ROOT/go.sum" "$WORK/go.sum" "$MODULE"
step go-mod-verify 0 bash -c "cd '$WORK' && exec go mod verify"

# ---- 3. 单元测试：校验器负例、docker 归属守卫 ----
step pack-unit-tests 0 python3 -I "$ROOT/scripts/readiness/test_pack.py"
step unit-tests 0 bash -c "cd '$WORK' && exec go test -count=1 -race -v ./ledger/... ./dockerx/... ./scanx/... ./cmd/..."
for t in TestNegativeUnknownID TestNegativePayloadMismatch TestNegativeMissingCompletion TestNegativeQueryFailureIsNotZero TestNegativeEmptyLedgerFails TestNegativeMissingRequiredCheckCannotFalsePass TestMismatchedLabelsAreRefusedWithoutTouching TestResExpectationMustEqualCurrentContext TestCleanupRequiresMatchingRunAndExplicitDecoyContext TestStartFailureKeepsRegisteredContainerForCleanup TestRegisterFailureRemovesExactCreatedContainer TestCountUniqueDeduplicatesRepeatedKeys TestExecRunnerForbidsPrune TestAdoptContainerSurvivesCanceledRunContext TestRegisterFailureRemovalSurvivesCanceledRunContext TestCreateVolumeErrorAdoptsOnlyExactOwnedVolume TestCleanupExitCodeSeparatesRefusalFromInfraFailure TestExpectCallsAllowsScriptedRepeatsOnly TestNegativeExpectCallsMismatch TestSoakBreachesTripBelowResourceCaps TestSoakClassesDisjointAndPayloadBounded TestSoakTrendSlope; do
  if grep -q -- "--- PASS: $t" "$EV/step-unit-tests.log"; then printf '%s\tPASS\t0\tnegative-test\n' "$t" >> "$STEPS"; else printf '%s\tFAIL\t0\tnot found/passed\n' "$t" >> "$STEPS"; log "FAIL: negative unit test $t did not pass"; FAILED=1; fi
done

# ---- 4. 门禁负例（真实命令） ----
step negative-bad-addr 1 "$GATE" negative bad-addr
NEG="$EV/neg-label"; OUTS+=("$NEG")
step negative-label-mismatch 1 "$GATE" negative label-mismatch --out "$NEG" --run-id "$RUN_ID"
DECOY_ID=""
check_decoy() {   # check_decoy <label>: 诱饵必须仍在运行且标签未变（独立于 gate 自身判断）
  local what="$1" running
  running="$(docker inspect --type container -f '{{.State.Running}} {{index .Config.Labels "taurus.strict.owner"}}' "$DECOY_ID" 2>&1)"
  if [ "$running" = "true $DECOY_OWNER" ]; then
    printf 'decoy-untouched-%s\tPASS\t0\t%s\n' "$what" "$running" >> "$STEPS"; log "    decoy still running with foreign label ($what): untouched"
  else
    printf 'decoy-untouched-%s\tFAIL\t0\t%s\n' "$what" "$running" >> "$STEPS"; log "FAIL: decoy state ($what): $running"; FAILED=1
  fi
}
if [ -f "$NEG/manifest.json" ]; then
  DECOY_ID="$(python3 -I - "$NEG/manifest.json" <<'PYIN'
import json,sys
rs=json.load(open(sys.argv[1]))
print(next((r["id"] for r in rs if r.get("purpose")=="decoy"),""))
PYIN
)"
  if [ ${#DECOY_ID} -eq 64 ]; then
    check_decoy after-negative
    # 串用 manifest（本轮 owner 上下文清 decoy 登记）、改 run、非法 owner：都必须被拒绝且不改变诱饵
    step negative-cleanup-main-owner-context 6 "$GATE" cleanup --out "$NEG" --run-id "$RUN_ID"
    check_decoy after-main-owner-cleanup
    step negative-cleanup-wrong-run 6 "$GATE" cleanup --out "$NEG" --run-id "${RUN_ID}-other" --owner "$DECOY_OWNER"
    check_decoy after-wrong-run-cleanup
    step negative-cleanup-unlisted-owner 2 "$GATE" cleanup --out "$NEG" --run-id "$RUN_ID" --owner someone-else
    check_decoy after-bad-owner-cleanup
    # 唯一合法路径：独立显式 decoy owner + 同一 run
    step negative-decoy-cleanup 0 "$GATE" cleanup --out "$NEG" --run-id "$RUN_ID" --owner "$DECOY_OWNER"
  else
    printf 'decoy-untouched\tFAIL\t0\tno decoy id\n' >> "$STEPS"; FAILED=1
    step negative-decoy-cleanup 0 "$GATE" cleanup --out "$NEG" --run-id "$RUN_ID" --owner "$DECOY_OWNER"
  fi
else
  printf 'decoy-untouched\tFAIL\t0\tno manifest\n' >> "$STEPS"; FAILED=1
fi

# ---- 4b. soak 阈值负例（仅在请求 soak 时）：极低 Redis 内存阈值，必须失败、首个采样即中止、并精确清理 ----
if [ -n "$SOAK" ]; then
  NSOAK="$EV/neg-soak"; OUTS+=("$NSOAK")
  step negative-soak-threshold 1 "$GATE" run --out "$NSOAK" --run-id "$RUN_ID" --log "$EV/queue-lib-negsoak.log" --scenarios soak --soak 60 --soak-limit-redis-bytes 1
  step negative-soak-threshold-stopped-early 0 python3 -I -c '
import json,sys
r=json.load(open(sys.argv[1]))
v=[x for x in r["violations"] if x.get("code")=="check_failed" and "soak_safety_thresholds" in x.get("detail","")]
assert r["pass"] is False, "limit-test soak must fail"
assert v, "no soak_safety_thresholds violation: %r" % r["violations"][:3]
assert r["metrics"].get("production_s", 999) < 20, "production did not stop promptly: %r" % r["metrics"].get("production_s")
print("threshold breach detected and production stopped after %.1fs" % r["metrics"]["production_s"])
' "$NSOAK/soak-limit-test.result.json"
fi

# ---- 5. 真实隔离场景 ----
MAIN="$EV/main"; OUTS+=("$MAIN")
# shellcheck disable=SC2086
step gate-run 0 "$GATE" run --out "$MAIN" --run-id "$RUN_ID" --log "$EV/queue-lib.log" $SUSTAINED $SCENARIOS $NPER $SOAK
cp "$EV/step-gate-run.log" "$EV/gate-run-summary.txt" 2>/dev/null || true

# ---- 6. 运行模式（FULL/PARTIAL）与 summary 对账 ----
MODE="$(python3 -I - "$MAIN/summary.json" $DEFAULT_RESULTS <<'PYIN'
import json,sys
try:
    s=json.load(open(sys.argv[1]))
except Exception:
    print("UNREADABLE"); sys.exit(0)
if s.get("mode")=="FULL":
    res=s.get("results",{})
    bad=[n for n in sys.argv[2:] if res.get(n) is not True]
    print("FULL" if not bad else "FULL-INCOMPLETE:"+",".join(bad))
else:
    print(s.get("mode","UNKNOWN"))
PYIN
)"
log "run mode: $MODE"
# FULL 对账不只信 summary：每个默认结果必须在磁盘上有 result/expect/ledger，且 result.pass==true
if [ "$MODE" = "FULL" ]; then
  for n in $DEFAULT_RESULTS; do
    if ! [ -s "$MAIN/$n.result.json" ] || ! [ -s "$MAIN/$n.expect.json" ] || ! [ -s "$MAIN/$n.ledger.jsonl" ] \
       || ! python3 -I -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get("pass") is True else 1)' "$MAIN/$n.result.json"; then
      MODE="FULL-INCOMPLETE-DISK:$n"; break
    fi
  done
fi
case "$MODE" in
  FULL) printf 'summary-reconciled-full\tPASS\t0\tall 10 default results in summary and on disk (result pass=true, expect, ledger)\n' >> "$STEPS" ;;
  PARTIAL) printf 'summary-mode\tPARTIAL\t0\tscenario subset or reduced N\n' >> "$STEPS" ;;
  *) printf 'summary-reconciled-full\tFAIL\t0\t%s\n' "$MODE" >> "$STEPS"; log "FAIL: summary mode/reconcile: $MODE"; FAILED=1 ;;
esac

# ---- 6a. soak 独立核对（不只信 gate）：结果为真、采样连续、生产窗口达到计划、阶段完整 ----
if [ -n "$SOAK" ]; then
  step "soak-independent-check" 0 python3 -I -c '
import json,sys
d,name=sys.argv[1],sys.argv[2]
r=json.load(open("%s/%s.result.json"%(d,name)))
s=json.load(open("%s/summary.json"%d))
assert s["results"].get(name) is True, "summary result %s=%r" % (name, s["results"].get(name))
assert r["pass"] is True, "result pass=%r" % r["pass"]
m=r["metrics"]; planned=m["planned_production_s"]
xs=[json.loads(l) for l in open("%s/%s.samples.jsonl"%(d,name))]
gaps=[x["gap_ms"] for x in xs[1:]]
phases={x["phase"] for x in xs}
prod=[x for x in xs if x["phase"]=="produce"]
assert m["production_s"] >= planned - 1, "production %.1fs < planned %.0fs" % (m["production_s"], planned)
assert len(prod) >= planned/ (10 if planned>=1800 else 5) - 3, "only %d produce-phase samples" % len(prod)
assert max(gaps) <= 30000, "sample gap %dms" % max(gaps)
assert {"produce","drain","expiry-wait","final"} <= phases, "phases %r" % phases
assert not any(x.get("breaches") for x in xs), "breach recorded"
last=xs[-1]
print("%s: production %.1fs (planned %.0fs), total %.1fs, samples %d (max gap %dms), submit_calls %d, processor_calls %d, final queue %r" % (
  name, m["production_s"], planned, m["total_s"], len(xs), max(gaps), m["submit_calls"], m["processor_calls"], last["queue"]))
' "$MAIN" "$SOAK_NAME"
fi

# ---- 6b. 从磁盘账本独立重新校验 ----
shopt -s nullglob
VERIFIED=0
for ex in "$MAIN"/*.expect.json; do
  n="$(basename "$ex" .expect.json)"
  step "verify-$n" 0 "$GATE" verify --expect "$ex"
  VERIFIED=$((VERIFIED + 1))
done
[ "$VERIFIED" -gt 0 ] || { log "FAIL: no scenario produced an expect file"; printf 'verify-any\tFAIL\t0\tnone\n' >> "$STEPS"; FAILED=1; }
for res in "$MAIN"/*.result.json; do
  if ! python3 -I -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["pass"] is True else 1)' "$res"; then
    log "FAIL: $res pass != true"; printf 'result-%s\tFAIL\t0\t\n' "$(basename "$res")" >> "$STEPS"; FAILED=1
  fi
done

# ---- 7. 注入坏账本：verify 必须以“预期的违规码”失败（用真实场景账本做底） ----
# gate verify --expect-violation CODE: 含该码 -> 1；有违规但不含该码 -> 5；账本不可读 -> 4；无违规 -> 0。
# 因此只有退出码恰为 1 才算负例有效，不可读/错码都不会被当成“按预期失败”。
BASE_EX="$MAIN/normal-256b-0ms.expect.json"; BASE_LG="$MAIN/normal-256b-0ms.ledger.jsonl"
if [ -f "$BASE_EX" ] && [ -f "$BASE_LG" ]; then
  mkdir -p "$EV/mutations"
  for pair in unknown_id:unknown_id bad_payload:payload_mismatch missing_final:accepted_not_confirmed query_error:query_error duplicate:unexpected_duplicate empty:empty_ledger drop_required:missing_required_check; do
    kind="${pair%%:*}"; code="${pair##*:}"
    "$GATE" mutate --in "$BASE_LG" --out "$EV/mutations/$kind.jsonl" --kind "$kind" >"$EV/mutations/$kind.mutate.log" 2>&1 || { log "mutate $kind failed"; printf 'mutate-%s\tFAIL\t0\t\n' "$kind" >> "$STEPS"; FAILED=1; continue; }
    step "mutation-$kind-must-fail-with-$code" 1 "$GATE" verify --expect "$BASE_EX" --ledgers "$EV/mutations/$kind.jsonl" --expect-violation "$code"
  done
  # 不可读账本必须与“违规”区分（退出码 4）
  printf 'not json\n' > "$EV/mutations/garbage.jsonl"
  step "mutation-unreadable-ledger-is-exit-4" 4 "$GATE" verify --expect "$BASE_EX" --ledgers "$EV/mutations/garbage.jsonl" --expect-violation unknown_id
elif [ "$MODE" = "PARTIAL" ]; then
  log "SKIP mutations (PARTIAL run without the baseline normal-256b-0ms scenario)"
  printf 'mutations\tSKIP\t0\tPARTIAL run: baseline scenario not present\n' >> "$STEPS"
else
  log "FAIL: baseline scenario files for mutations are missing in a non-PARTIAL run"
  printf 'mutations\tFAIL\t0\tbaseline missing\n' >> "$STEPS"; FAILED=1
fi
# 脚本化调用次数：删掉 contract 账本里调用次数最多的ID的最后一次调用，必须以 call_count_mismatch 失败
mkdir -p "$EV/mutations"
if [ -f "$MAIN/contract.expect.json" ]; then
  "$GATE" mutate --in "$MAIN/contract.ledger.jsonl" --out "$EV/mutations/drop_last_call.jsonl" --kind drop_last_call >"$EV/mutations/drop_last_call.mutate.log" 2>&1 || { log "mutate drop_last_call failed"; printf 'mutate-drop_last_call\tFAIL\t0\t\n' >> "$STEPS"; FAILED=1; }
  step "mutation-drop_last_call-must-fail-with-call_count_mismatch" 1 "$GATE" verify --expect "$MAIN/contract.expect.json" --ledgers "$EV/mutations/drop_last_call.jsonl" --expect-violation call_count_mismatch
elif [ "$MODE" != "PARTIAL" ]; then
  log "FAIL: contract ledger missing in a non-PARTIAL run"; printf 'mutation-drop_last_call\tFAIL\t0\tcontract missing\n' >> "$STEPS"; FAILED=1
fi

# ---- 8. 清理与事后核验 ----
step cleanup-main 0 "$GATE" cleanup --out "$MAIN" --run-id "$RUN_ID"
leftover=0; inspfail=0
for h in "$MAIN/manifest.json.history" "$NEG/manifest.json.history" ${NSOAK:+"$NSOAK/manifest.json.history"}; do
  [ -f "$h" ] || continue
  while IFS=$'\t' read -r kind id name; do
    [ -n "$id" ] || continue
    if [ "$kind" = "container" ]; then cmd=(docker inspect --type container "$id"); else cmd=(docker volume inspect "$id"); fi
    insp="$("${cmd[@]}" 2>&1)"; irc=$?
    if [ "$irc" -eq 0 ]; then
      log "LEFTOVER: $kind $name"; leftover=$((leftover + 1))
    elif printf '%s' "$insp" | grep -qi "no such"; then
      :   # 确认不存在
    else
      log "INSPECT FAILED (cannot confirm removal) $kind $name: $insp"; inspfail=$((inspfail + 1))
    fi
    echo "$kind $name ${id:0:12}" >> "$EV/created-resources.txt"
  done < "$h"
done
if [ "$leftover" -eq 0 ] && [ "$inspfail" -eq 0 ]; then printf 'no-leftover-resources\tPASS\t0\t\n' >> "$STEPS"; else printf 'no-leftover-resources\tFAIL\t0\t%s left, %s inspect-failed\n' "$leftover" "$inspfail" >> "$STEPS"; FAILED=1; fi

# ---- 9. 候选源码与仓库状态未变 ----
python3 -I "$ROOT/scripts/readiness/pack.py" hash "$ROOT" "$EV/candidate-source-sha256-after.txt"
if cmp -s "$EV/candidate-source-sha256.txt" "$EV/candidate-source-sha256-after.txt"; then printf 'candidate-source-unchanged\tPASS\t0\t\n' >> "$STEPS"; else printf 'candidate-source-unchanged\tFAIL\t0\t\n' >> "$STEPS"; log "FAIL: candidate source changed during the run"; FAILED=1; fi
sha256sum_ "$ROOT/go.mod" "$ROOT/go.sum" > "$EV/root-gomod-sha256-after.txt"
if cmp -s "$EV/root-gomod-sha256-before.txt" "$EV/root-gomod-sha256-after.txt"; then printf 'root-go-mod-unchanged\tPASS\t0\t\n' >> "$STEPS"; else printf 'root-go-mod-unchanged\tFAIL\t0\t\n' >> "$STEPS"; FAILED=1; fi
git -C "$ROOT" status --porcelain > "$EV/git-status-after.txt" 2>&1
# scripts/ 与 docs/ 允许在本任务内变化，因此只比较 pkg/ 与根模块文件
if diff <(grep -E ' (pkg/|go\.mod|go\.sum)' "$EV/git-status-before.txt") <(grep -E ' (pkg/|go\.mod|go\.sum)' "$EV/git-status-after.txt") >/dev/null; then printf 'git-status-pkg-unchanged\tPASS\t0\t\n' >> "$STEPS"; else printf 'git-status-pkg-unchanged\tFAIL\t0\t\n' >> "$STEPS"; FAILED=1; fi

# 局部模块缓存可重建，清掉（-modcacherw 保证可删除）；证据（proxy zip、日志、账本）保留
rm -rf "$EV/gomodcache" "$WORK" 2>/dev/null || true

echo
echo "================ step summary ($STEPS) ================"
column -t -s$'\t' "$STEPS" || cat "$STEPS"
if [ "$FAILED" -eq 0 ] && ! grep -q $'\tFAIL\t' "$STEPS"; then
  if [ "$MODE" = "FULL" ]; then
    echo "STRICT GATE: PASS  (run $RUN_ID; evidence $EV)"
    [ "$SOAK_NAME" = "soak" ] && echo "SOAK (30-minute bounded): PASS — see $MAIN/soak.result.json and soak.samples.jsonl"
    echo "note: a pass on this gate is a bounded local acceptance result, not a production go-live approval."
    exit 0
  fi
  echo "STRICT GATE: PARTIAL  (run $RUN_ID; evidence $EV)"
  echo "note: PARTIAL = scenario subset and/or reduced --n. Everything that ran passed, but this is NOT a full gate pass (exit 3)."
  exit 3
fi
echo "STRICT GATE: FAIL  (run $RUN_ID; evidence $EV)"
exit 1
