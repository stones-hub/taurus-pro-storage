// Command gate 是严格单机验收门禁的 Go 调用方：只通过公开 API 使用打包后的候选模块。
//
// 子命令：
//
//	run       运行场景（私有 Redis 容器、逐ID账本、严格断言），任一失败则退出码非 0
//	verify    从磁盘上的账本 + expect 文件独立重新校验
//	mutate    对账本注入负例（未知ID/错误载荷/漏完成/查询失败/重复），用于证明 verify 会失败
//	negative  bad-addr | label-mismatch：门禁负例（坏地址、资源标签不符）
//	cleanup   按 manifest 的显式清单（带归属校验）清理资源
//	consumer  内部使用：独立消费者子进程
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"taurus-readiness/dockerx"
	"taurus-readiness/ledger"
)

func parseArgs(a []string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(a); i++ {
		if !strings.HasPrefix(a[i], "--") {
			m["_"+strconv.Itoa(len(m))] = a[i]
			continue
		}
		k := strings.TrimPrefix(a[i], "--")
		if i+1 < len(a) && !strings.HasPrefix(a[i+1], "--") {
			m[k] = a[i+1]
			i++
		} else {
			m[k] = "1"
		}
	}
	return m
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gate run|verify|mutate|negative|cleanup")
		os.Exit(2)
	}
	cmd, args := os.Args[1], parseArgs(os.Args[2:])
	if lf := args["log"]; lf != "" { // 被测库的 log 输出单独落盘，保持终端输出干净
		f, err := os.OpenFile(lf, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		log.SetOutput(f)
	}
	switch cmd {
	case "run":
		os.Exit(cmdRun(args))
	case "verify":
		os.Exit(cmdVerify(args))
	case "mutate":
		os.Exit(cmdMutate(args))
	case "negative":
		os.Exit(cmdNegative(args))
	case "cleanup":
		os.Exit(cmdCleanup(args))
	case "consumer":
		os.Exit(runConsumer(args))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}

const decoyOwner = ownerValue + "-decoy"

func newRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("s%d-%s", time.Now().Unix(), hex.EncodeToString(b))
}

func mkEnv(args map[string]string) (*Env, *dockerx.Manifest, error) {
	out := args["out"]
	if out == "" {
		return nil, nil, errors.New("--out is required")
	}
	if err := ensureDir(out); err != nil {
		return nil, nil, err
	}
	runID := args["run-id"]
	if runID == "" {
		runID = newRunID()
	}
	var man *dockerx.Manifest
	var err error
	mp := out + "/manifest.json"
	if _, serr := os.Stat(mp); serr == nil {
		man, err = dockerx.LoadManifest(mp)
	} else {
		man, err = dockerx.NewManifest(mp)
	}
	if err != nil {
		return nil, nil, err
	}
	// --owner 只用于清理诱饵：必须显式给出，且只接受固定的 decoy owner；默认永远是本轮 owner
	owner := ownerValue
	if o := args["owner"]; o != "" {
		if o != decoyOwner {
			return nil, nil, fmt.Errorf("--owner %q not allowed (only %q)", o, decoyOwner)
		}
		owner = o
	}
	env := &Env{RunID: runID, Dir: out}
	env.D = &dockerx.Docker{R: dockerx.ExecRunner{}, Owner: owner, Run: runID, Manifest: man}
	return env, man, nil
}

// ---------------------------------------------------------------------------

// 默认完整门禁的场景集合与 N；与之不同的运行一律标记 PARTIAL
const defaultN = 1000

var defaultScenarios = []string{"normal", "backlog", "consumer-kill", "contract", "redis-sigkill", "oom", "backup-restore"}

// expectedResults 完整门禁必须出现且全部为 true 的结果名（对账：缺一个即失败）
var expectedResults = []string{
	"normal-256b-0ms", "normal-256b-5ms", "normal-4096b-0ms", "normal-4096b-5ms",
	"backlog", "consumer-kill", "contract", "redis-sigkill", "oom", "backup-restore",
}

func cmdRun(args map[string]string) (rc int) {
	env, _, err := mkEnv(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate run:", err)
		return 2
	}
	n := defaultN
	if v := args["n"]; v != "" {
		if n, err = strconv.Atoi(v); err != nil || n <= 0 || n > 3000 {
			fmt.Fprintln(os.Stderr, "gate run: --n must be 1..3000")
			return 2
		}
	}
	want := map[string]bool{}
	list := args["scenarios"]
	if list == "" {
		list = strings.Join(defaultScenarios, ",")
	}
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if s == "soak" { // soak 由 --soak 控制；列表里写 soak 表示“只跑 soak”（不追加默认场景）
			if _, ok := args["soak"]; !ok {
				fmt.Fprintln(os.Stderr, "gate run: scenario soak needs --soak SECONDS")
				return 2
			}
			continue
		}
		if s != "sustained" && !slices.Contains(defaultScenarios, s) {
			fmt.Fprintf(os.Stderr, "gate run: unknown scenario %q\n", s)
			return 2
		}
		want[s] = true
	}
	if args["sustained"] == "1" {
		want["sustained"] = true
	}
	// --soak SECONDS：显式可选的有界长稳态。只有恰好 1800 秒才记为结果 "soak"；其它时长是工具自测 "soak-smoke"，
	// 带 --soak-limit-redis-bytes（阈值负例）的是 "soak-limit-test"，两者都会使本轮成为 PARTIAL。
	var soak *soakOpts
	if v, ok := args["soak"]; ok {
		secs, err := strconv.Atoi(v)
		if err != nil || !(secs == soakFullSeconds || (secs >= 30 && secs <= 600)) {
			fmt.Fprintln(os.Stderr, "gate run: --soak must be 1800 (full) or 30..600 (smoke)")
			return 2
		}
		o := soakOpts{Dur: time.Duration(secs) * time.Second, Window: 2 * time.Minute, Sample: 10 * time.Second, Limits: defaultSoakLimits(),
			Name: "soak", MaxSubs: soakMaxSubmits, Rate: soakRate, Workers: soakWorkers, Lease: 30 * time.Second, Poll: time.Second,
			Replay: 15 * time.Second, DrainMax: soakDrainMax}
		if secs != soakFullSeconds {
			o.Name, o.Window, o.Sample, o.Replay = "soak-smoke", time.Duration(secs)*time.Second/6, 5*time.Second, 5*time.Second
			o.MaxSubs = int(o.Rate) * secs
		}
		if lv := args["soak-limit-redis-bytes"]; lv != "" {
			b, err := strconv.ParseInt(lv, 10, 64)
			if err != nil || b <= 0 {
				fmt.Fprintln(os.Stderr, "gate run: --soak-limit-redis-bytes must be a positive integer")
				return 2
			}
			o.Limits.RedisUsedBytes, o.Name = b, "soak-limit-test"
		}
		soak = &o
		want[o.Name] = true
	}
	// FULL：默认集合一个不少，且 N 不低于默认值；否则是 PARTIAL（含 sustained 只算附加项）
	var missing []string
	for _, d := range defaultScenarios {
		if !want[d] {
			missing = append(missing, d)
		}
	}
	if n != defaultN {
		missing = append(missing, fmt.Sprintf("n=%d(default %d)", n, defaultN))
	}
	if soak != nil && soak.Name != "soak" {
		missing = append(missing, soak.Name+"(not the 30-minute soak)")
	}
	full := len(missing) == 0

	// INT/TERM：取消场景 ctx，仍执行显式清单清理，并使整轮失败
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	overall := 12 * time.Minute
	if soak != nil { // 长稳态自带硬截止；整轮上限为其上再加余量
		overall += soak.Dur + soak.DrainMax + soak.Window + 8*time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, overall)
	defer cancel()
	fmt.Printf("gate run id=%s out=%s image=%s\n", env.RunID, env.Dir, redisImage)
	if err := env.preflight(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "preflight FAILED:", err)
		return 1
	}
	passAll := true
	results := map[string]bool{}
	record := func(name string, ok bool) {
		results[name] = ok
		passAll = passAll && ok
	}
	t0 := time.Now()

	// 收尾总在 defer 中：正常、panic、信号中断三种路径都会回收子进程、精确清理清单并写 summary
	finish := func() int {
		if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			fmt.Println("gate run interrupted by signal:", err)
			record("interrupted", false)
		} else if err != nil {
			fmt.Println("gate run hit its overall deadline:", err)
			record("overall-deadline", false)
		}
		for _, e := range env.reapKids() {
			fmt.Println("child cleanup:", e)
			record("child-processes", false)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		clog, cerr := env.D.CleanupAll(cctx)
		_ = os.WriteFile(env.path("cleanup.log"), []byte(strings.Join(clog, "\n")+"\n"), 0o600)
		for _, l := range clog {
			fmt.Println("  cleanup:", l)
		}
		if cerr != nil {
			fmt.Println("cleanup FAILED:", cerr)
			record("cleanup", false)
		}
		if full { // 对账：默认集合的每个结果必须存在
			for _, name := range expectedResults {
				if _, ok := results[name]; !ok {
					fmt.Println("missing result for default scenario:", name)
					record("missing-"+name, false)
				}
			}
		}
		mode := "FULL"
		if !full {
			mode = "PARTIAL"
		}
		sum := map[string]any{"run_id": env.RunID, "pass": passAll, "mode": mode, "partial_reasons": missing, "scenarios": sortedKeys(want),
			"n": n, "results": results, "seconds": time.Since(t0).Seconds(), "image": redisImage}
		if err := writeJSON(env.path("summary.json"), sum); err != nil {
			fmt.Fprintln(os.Stderr, "cannot write summary:", err)
			return 1
		}
		if !passAll {
			fmt.Printf("GATE RESULT: FAIL (%.1fs)\n", time.Since(t0).Seconds())
			return 1
		}
		if !full {
			fmt.Printf("GATE RESULT: PASS-PARTIAL (%.1fs) — NOT a full gate; skipped/reduced: %s\n", time.Since(t0).Seconds(), strings.Join(missing, ", "))
			return 0
		}
		fmt.Printf("GATE RESULT: PASS FULL (%.1fs)\n", time.Since(t0).Seconds())
		return 0
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("gate run PANIC: %v\n", r)
			record("gate-panic", false)
			rc = finish()
			if rc == 0 {
				rc = 1
			}
		}
	}()
	runAll := func() {
		var main *Inst
		needMain := want["normal"] || want["backlog"] || want["consumer-kill"] || want["sustained"] || want["contract"]
		if needMain {
			main, err = env.newRedis(ctx, redisOpts{Tag: "main", AppendOnly: true, AofAlways: true})
			if err != nil {
				fmt.Fprintln(os.Stderr, "start main redis FAILED:", err)
				record("main-redis", false)
				needMain = false
			}
		}
		if needMain {
			fmt.Printf("main redis %s at %s\n", main.C.Name, main.Addr)
			if want["normal"] {
				for _, size := range []int{256, 4096} {
					for _, work := range []time.Duration{0, 5 * time.Millisecond} {
						tag := fmt.Sprintf("normal-%db-%dms", size, work.Milliseconds())
						record(tag, runGuardedInst(ctx, env, tag, main, func(c context.Context) bool {
							return scNormal(c, env, main, normalOpts{Tag: tag, Size: size, Work: work, N: n, Within: 15 * time.Second})
						}))
					}
				}
			}
			if want["backlog"] {
				record("backlog", runGuardedInst(ctx, env, "backlog", main, func(c context.Context) bool { return scBacklog(c, env, main, n) }))
			}
			if want["consumer-kill"] {
				record("consumer-kill", runGuardedInst(ctx, env, "consumer-kill", main, func(c context.Context) bool { return scConsumerKill(c, env, main) }))
			}
			if want["sustained"] {
				// 整个场景（提交、确认、停止、对账）总硬截止 sustainedTotal：2400 条 @80/s ≈ 30s 生产，余量用于排空/收尾
				record("sustained", runGuardedInst(ctx, env, "sustained", main, func(c context.Context) bool {
					return scNormal(c, env, main, normalOpts{Tag: "sustained", Size: 1024, Work: 5 * time.Millisecond, N: sustainedN, Rate: sustainedRate, Within: 15 * time.Second, Total: sustainedTotal})
				}))
			}
			if want["contract"] {
				record("contract", runGuardedInst(ctx, env, "contract", main, func(c context.Context) bool { return scContract(c, env, main) }))
			}
			removeInst(env, main)
		}
		for _, sc := range []struct {
			name string
			fn   func(context.Context, *Env) bool
		}{{"redis-sigkill", scRedisCrash}, {"oom", scOOM}, {"backup-restore", scBackup}} {
			if want[sc.name] {
				record(sc.name, runGuarded(ctx, env, sc.name, sc.fn))
			}
		}
		if soak != nil { // 最后运行，独立私有 Redis，避免前面场景的墓碑干扰趋势
			o := *soak
			record(o.Name, runGuarded(ctx, env, o.Name, func(c context.Context, e *Env) bool { return scSoak(c, e, o) }))
		}
	}
	runAll()
	return finish()
}

const (
	sustainedN     = 2400
	sustainedRate  = 80.0
	sustainedTotal = 60 * time.Second
)

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runGuardedInst 与 runGuarded 相同，但场景函数不需要 env 参数（共享的 main 实例由调用方负责清理）
func runGuardedInst(ctx context.Context, env *Env, name string, _ *Inst, fn func(context.Context) bool) bool {
	return runGuarded(ctx, env, name, func(c context.Context, _ *Env) bool { return fn(c) })
}

// runGuarded 运行场景并把 panic 转为失败；场景结束后精确删除本场景新登记的容器/卷（按清单ID，逆序）。
func runGuarded(ctx context.Context, env *Env, name string, fn func(context.Context, *Env) bool) (ok bool) {
	existing := map[string]bool{}
	for _, r := range env.D.Manifest.Snapshot() {
		existing[r.ID] = true
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  [%s] PANIC: %v\n", name, r)
			ok = false
		}
		for _, r := range reverse(env.D.Manifest.Snapshot()) {
			if existing[r.ID] {
				continue
			}
			if err := env.D.Remove(ctx, r); err != nil {
				fmt.Printf("  [%s] cleanup of %s failed: %v\n", name, r.Name, err)
				ok = false
			}
		}
	}()
	return fn(ctx, env)
}

func reverse(rs []dockerx.Res) []dockerx.Res {
	out := make([]dockerx.Res, len(rs))
	for i, r := range rs {
		out[len(rs)-1-i] = r
	}
	return out
}

func removeInst(env *Env, in *Inst) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, r := range []dockerx.Res{in.C, in.Vol} {
		if r.ID == "" {
			continue
		}
		if err := env.D.Remove(ctx, r); err != nil {
			fmt.Printf("  cleanup of %s failed: %v\n", r.Name, err)
		}
	}
}

// ---------------------------------------------------------------------------

// verify 退出码：0 无违规；1 有违规（给了 --expect-violation CODE 时，仅当违规里包含该码才返回 1）；
// 2 用法/期望文件错误；4 账本不可读（与违规区分）；5 有违规但不含预期的违规码。
const (
	exitViolations = 1
	exitUnreadable = 4
	exitWrongCode  = 5
)

func cmdVerify(args map[string]string) int {
	ef := args["expect"]
	if ef == "" {
		fmt.Fprintln(os.Stderr, "verify: --expect FILE required")
		return 2
	}
	b, err := os.ReadFile(ef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 2
	}
	var x expectFile
	if err := json.Unmarshal(b, &x); err != nil {
		fmt.Fprintln(os.Stderr, "verify: bad expect file:", err)
		return 2
	}
	files := x.Ledgers
	if l := args["ledgers"]; l != "" {
		files = strings.Split(l, ",")
	}
	recs, err := ledger.ReadFiles(files...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: ledger unreadable:", err)
		return exitUnreadable
	}
	rep := ledger.Validate(recs, x.Expect)
	fmt.Printf("verify %s: records=%d submitted=%d accepted=%d calls=%d unique=%d dup=%d confirmed=%d violations=%d\n",
		ef, len(recs), rep.UniqueSubmitted, rep.Accepted, rep.ProcCalls, rep.UniqueExecuted, rep.Duplicates, rep.Confirmed, len(rep.Violations))
	if rep.Ok() {
		return 0
	}
	fmt.Print(rep.Summary(10))
	if want := args["expect-violation"]; want != "" {
		for _, v := range rep.Violations {
			if v.Code == want {
				fmt.Printf("verify: expected violation %q present\n", want)
				return exitViolations
			}
		}
		fmt.Printf("verify: violations present but NOT the expected code %q\n", want)
		return exitWrongCode
	}
	return exitViolations
}

func cmdMutate(args map[string]string) int {
	in, out, kind := args["in"], args["out"], args["kind"]
	recs, err := ledger.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutate:", err)
		return 2
	}
	w, err := ledger.Create(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutate:", err)
		return 2
	}
	var first *ledger.Rec
	for i := range recs {
		if recs[i].K == ledger.KProcStart {
			first = &recs[i]
			break
		}
	}
	if first == nil {
		fmt.Fprintln(os.Stderr, "mutate: no proc_start in source ledger")
		return 2
	}
	dropFinal := true
	// drop_last_call：删掉调用次数最多的ID的最后一次调用（start+end），用于证明脚本化调用次数被逐ID核对
	var dropCall string
	if kind == "drop_last_call" {
		n, last := map[string]int{}, map[string]string{}
		best := ""
		for _, r := range recs {
			if r.K == ledger.KProcStart {
				n[r.ID]++
				last[r.ID] = r.Call
				if n[r.ID] > n[best] || (n[r.ID] == n[best] && r.ID < best) {
					best = r.ID
				}
			}
		}
		if n[best] < 2 {
			fmt.Fprintln(os.Stderr, "mutate: drop_last_call needs an id with >= 2 calls")
			return 2
		}
		dropCall = last[best]
	}
	var res []ledger.Rec
	for _, r := range recs {
		switch kind {
		case "bad_payload":
			if r.K == ledger.KProcStart && r.ID == first.ID {
				r.Sha = ledger.SHA256Hex([]byte("tampered"))
			}
		case "missing_final":
			if r.K == ledger.KFinal && dropFinal {
				dropFinal = false
				continue
			}
		case "drop_required":
			if r.K == ledger.KCheck && r.Name == "per_id_reconcile_all" {
				continue
			}
		case "empty":
			continue
		case "drop_last_call":
			if (r.K == ledger.KProcStart || r.K == ledger.KProcEnd) && r.Call == dropCall {
				continue
			}
		}
		res = append(res, r)
	}
	last := recs[len(recs)-1].T + 1
	switch kind {
	case "unknown_id":
		res = append(res, ledger.Rec{K: ledger.KProcStart, T: last, ID: "ghost-99999", Sha: first.Sha, Len: first.Len, Pid: 1, Call: "9:1", POK: ledger.Bool(true)},
			ledger.Rec{K: ledger.KProcEnd, T: last + 1, ID: "ghost-99999", Pid: 1, Call: "9:1", Ret: "nil"})
	case "query_error":
		res = append(res, ledger.Rec{K: ledger.KCheck, T: last, Name: "injected_query", Err: "dial tcp 127.0.0.1:1: connect: connection refused"})
	case "duplicate":
		res = append(res, ledger.Rec{K: ledger.KProcStart, T: last, ID: first.ID, Sha: first.Sha, Len: first.Len, Pid: 1, Call: "9:1", POK: ledger.Bool(true)},
			ledger.Rec{K: ledger.KProcEnd, T: last + 1, ID: first.ID, Pid: 1, Call: "9:1", Ret: "nil"})
	case "bad_payload", "missing_final", "drop_required", "empty", "drop_last_call":
	default:
		fmt.Fprintln(os.Stderr, "mutate: unknown kind", kind)
		return 2
	}
	for _, r := range res {
		if err := w.Append(r); err != nil {
			fmt.Fprintln(os.Stderr, "mutate:", err)
			return 2
		}
	}
	if err := w.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "mutate:", err)
		return 2
	}
	return 0
}

// ---------------------------------------------------------------------------

func cmdNegative(args map[string]string) int {
	switch args["_0"] {
	case "bad-addr":
		// 真实代码路径：用与场景相同的 queueClient 连接一个无人监听的地址
		addr := "127.0.0.1:1"
		if _, err := queueClient(addr); err != nil {
			fmt.Printf("negative bad-addr: connect to %s refused as expected: %v\n", addr, err)
			return 1
		}
		fmt.Println("negative bad-addr: UNEXPECTEDLY connected")
		return 3
	case "label-mismatch":
		return negLabelMismatch(args)
	}
	fmt.Fprintln(os.Stderr, "negative: bad-addr | label-mismatch")
	return 2
}

// negLabelMismatch 创建一个“归属标签不符”的诱饵容器（自己创建、自己登记、自己清理），
// 用多种串用/篡改上下文去 kill/exec/cp/rm 它：必须全部被拒绝，且诱饵仍在运行、未被改动。
// 诱饵只能用独立显式的 decoy owner 上下文（且同一 run）清理。
// 全部拒绝（预期）返回 1；若有任何一步越权成功返回 3。
func negLabelMismatch(args map[string]string) int {
	env, man, err := mkEnv(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "negative:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := env.preflight(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "negative preflight:", err)
		return 2
	}
	dd := &dockerx.Docker{R: env.D.R, Owner: decoyOwner, Run: env.RunID, Manifest: man}
	res, err := dd.CreateContainer(ctx, env.name("decoy"), "decoy",
		"--cpus", "0.2", "--memory", "64m", "--memory-swap", "64m", redisImage, "redis-server", "--save", "", "--appendonly", "no")
	if err != nil {
		fmt.Fprintln(os.Stderr, "negative: cannot create decoy:", err)
		return 2
	}
	if err := dd.Start(ctx, res); err != nil {
		fmt.Fprintln(os.Stderr, "negative: cannot start decoy (registered; cleanup will remove it):", err)
		return 2
	}
	// 各种越权上下文。每一种都不得改变诱饵。
	forged := res
	forged.ExpectOwner = ownerValue // 篡改 manifest：声称是本轮 owner（与真实标签不符）
	otherRun := res
	otherRun.ExpectRun = env.RunID + "-other"                                                                  // 篡改 run
	otherRunDocker := &dockerx.Docker{R: env.D.R, Owner: decoyOwner, Run: env.RunID + "-other", Manifest: man} // run 不同的上下文
	type ctxCase struct {
		name string
		d    *dockerx.Docker
		r    dockerx.Res
	}
	cases := []ctxCase{
		{"main-owner-context-with-decoy-manifest-entry", env.D, res}, // 串用：本轮 owner 上下文 + decoy 登记
		{"forged-owner-in-manifest", env.D, forged},
		{"forged-owner-in-manifest-decoy-context", dd, forged},
		{"forged-run-in-manifest", dd, otherRun},
		{"different-run-context", otherRunDocker, res},
	}
	refused, total := 0, 0
	for _, c := range cases {
		ops := []struct {
			name string
			fn   func() error
		}{
			{"kill", func() error { return c.d.Kill(ctx, c.r, "KILL") }},
			{"exec", func() error { _, err := c.d.Exec(ctx, c.r, "redis-cli", "ping"); return err }},
			{"cp", func() error { return c.d.CopyFrom(ctx, c.r, "/data", env.path("decoy-cp")) }},
			{"remove", func() error { return c.d.Remove(ctx, c.r) }},
		}
		for _, op := range ops {
			total++
			err := op.fn()
			if errors.Is(err, dockerx.ErrNotOwned) {
				fmt.Printf("negative label-mismatch: [%s] %s refused: %v\n", c.name, op.name, err)
				refused++
			} else {
				fmt.Printf("negative label-mismatch: [%s] %s was NOT refused (err=%v)\n", c.name, op.name, err)
			}
		}
	}
	st, err := dd.Inspect(ctx, res)
	if err != nil || !st.Running {
		fmt.Printf("negative label-mismatch: decoy was touched (err=%v state=%+v)\n", err, st)
		return 3
	}
	if refused != total {
		return 3
	}
	if _, serr := os.Stat(env.path("decoy-cp")); serr == nil {
		fmt.Println("negative label-mismatch: cp wrote data")
		return 3
	}
	if len(man.Snapshot()) != 1 {
		fmt.Println("negative label-mismatch: manifest changed")
		return 3
	}
	fmt.Printf("negative label-mismatch: all %d operations in %d foreign contexts refused; decoy untouched and still running\n", total, len(cases))
	return 1
}

func cmdCleanup(args map[string]string) int {
	env, man, err := mkEnv(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cleanup:", err)
		return 2
	}
	if len(man.Snapshot()) == 0 {
		fmt.Println("cleanup: manifest is empty, nothing to do")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	logs, err := env.D.CleanupAll(ctx)
	for _, l := range logs {
		fmt.Println("cleanup:", l)
	}
	if err != nil {
		fmt.Println("cleanup FAILED:", err)
		return cleanupExitCode(err)
	}
	return 0
}

// exitNotOwned 是 cleanup 因归属不符（ErrNotOwned）而全部拒绝时的专用退出码，
// 与 docker 守护进程故障等其它清理错误（1）区分。
const exitNotOwned = 6

// cleanupExitCode：仅当 err 的每一个叶子错误都是 ErrNotOwned 才返回 exitNotOwned，否则为 1。
func cleanupExitCode(err error) int {
	if err == nil {
		return 0
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if cleanupExitCode(e) != exitNotOwned {
				return 1
			}
		}
		return exitNotOwned
	}
	if errors.Is(err, dockerx.ErrNotOwned) {
		return exitNotOwned
	}
	return 1
}
