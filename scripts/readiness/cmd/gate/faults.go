package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"

	"taurus-readiness/ledger"
)

// ---------------------------------------------------------------------------
// 独立消费者子进程：领取后被 SIGKILL，另一个进程恢复

// runConsumer 是 `gate consumer` 子命令：作为独立进程运行 Manager，账本写入独立文件。
func runConsumer(args map[string]string) int {
	addr, q, path := args["addr"], args["queue"], args["ledger"]
	hold := args["hold"] == "1"
	lease, _ := time.ParseDuration(args["lease"])
	workers, _ := strconv.Atoi(args["workers"])
	if addr == "" || q == "" || path == "" || lease <= 0 || workers <= 0 {
		fmt.Fprintln(os.Stderr, "consumer: missing/invalid args")
		return 2
	}
	l, err := ledger.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rc, err := queueClient(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "consumer: connect:", err)
		return 1
	}
	defer rc.Close()
	p := &recProc{L: l}
	if hold {
		p.Hold = make(chan struct{}) // 永不关闭：领取后一直占着，等待被杀
	}
	m, err := queue.NewManager(p, mgrCfg(q, rc, workers, lease))
	if err != nil {
		fmt.Fprintln(os.Stderr, "consumer:", err)
		return 1
	}
	if err := m.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "consumer:", err)
		return 1
	}
	fmt.Println("READY")
	// 父进程（gate）异常消失（被 SIGKILL 等）时自行退出，避免孤儿；只看自己的父 PID
	ppid := os.Getppid()
	go func() {
		for range time.Tick(500 * time.Millisecond) {
			if os.Getppid() != ppid {
				fmt.Fprintln(os.Stderr, "consumer: parent gone, exiting")
				os.Exit(3)
			}
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serr := m.Stop(ctx)
	rec := ledger.Rec{K: ledger.KEvent, Name: "consumer_stop", Detail: "ok"}
	if serr != nil {
		rec.Detail = "error: " + serr.Error()
	}
	_ = l.Append(rec)
	if err := l.Close(); err != nil || serr != nil {
		fmt.Fprintln(os.Stderr, "consumer: stop/close:", serr, err)
		return 1
	}
	return 0
}

type child struct {
	cmd    *exec.Cmd
	ledger string
	waited chan struct{} // Wait 返回后关闭
	werr   error         // 仅在 waited 关闭后读取
}

// exited 报告子进程是否已被 Wait 回收
func (c *child) exited() bool {
	select {
	case <-c.waited:
		return true
	default:
		return false
	}
}

// terminate 确定性终止本进程自己启动的这一个子进程（精确 *os.Process，绝不按名字/全局 kill）：
// 已退出则直接返回；否则 SIGTERM，grace 内未退出则 SIGKILL；最后一定 Wait 回收（有上限）。
func (c *child) terminate(grace time.Duration) error {
	if c == nil || c.exited() {
		return nil
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.waited:
		return nil
	case <-time.After(grace):
	}
	_ = c.cmd.Process.Kill()
	select {
	case <-c.waited:
		return fmt.Errorf("pid %d needed SIGKILL after %v", c.cmd.Process.Pid, grace)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("pid %d was not reaped 10s after SIGKILL", c.cmd.Process.Pid)
	}
}

// reapKids 终止并回收本轮登记的所有未退出的消费者子进程（场景 defer / panic / 信号路径都会调用）
func (e *Env) reapKids() []error {
	e.kidsMu.Lock()
	kids := append([]*child(nil), e.kids...)
	e.kidsMu.Unlock()
	var errs []error
	for _, c := range kids {
		if c.exited() {
			continue
		}
		if err := c.terminate(5 * time.Second); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (e *Env) spawnConsumer(addr, q, tag string, lease time.Duration, workers int, hold bool) (*child, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	c := &child{ledger: e.path("kill." + tag + ".ledger.jsonl"), waited: make(chan struct{})}
	args := []string{"consumer", "--addr", addr, "--queue", q, "--ledger", c.ledger, "--lease", lease.String(), "--workers", strconv.Itoa(workers), "--log", e.path("kill." + tag + ".log")}
	if hold {
		args = append(args, "--hold", "1")
	}
	c.cmd = exec.Command(self, args...)
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.cmd.Stderr = os.Stderr
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}
	e.kidsMu.Lock()
	e.kids = append(e.kids, c)
	e.kidsMu.Unlock()
	go func() { c.werr = c.cmd.Wait(); close(c.waited) }()
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			return c, errors.New("consumer exited before READY")
		}
	case <-time.After(15 * time.Second):
		_ = c.terminate(2 * time.Second)
		return c, errors.New("consumer READY timeout")
	}
	return c, nil
}

func countStarts(path string) (int, error) {
	rs, err := ledger.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		if r.K == ledger.KProcStart {
			n++
		}
	}
	return n, nil
}

func scConsumerKill(ctx context.Context, env *Env, in *Inst) bool {
	started := time.Now()
	defer env.reapKids() // 任何返回/panic 路径都回收本场景启动的子进程（精确 PID）
	s, err := env.newSc("consumer-kill")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	const lease = 3 * time.Second
	q := qname(env, "consumer-kill")
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	rr := rawClient(in.Addr)
	defer rr.Close()
	eng, err := engine.NewRedisStreamEngine(ctx, rc, engine.ReliableOptions{Name: q, LeaseDuration: lease, DedupWindow: dedupWindow})
	if !s.CheckErr("new_engine", err) {
		return s.Finish(started)
	}
	items, err := makeItems("k", 20, 512)
	if !s.CheckErr("make_items", err) {
		return s.Finish(started)
	}
	s.Exp = ledger.Expect{AllowRedelivery: true, RedeliveryNeedsUnfinished: true, AllowUnfinished: true}
	s.Require("all_submits_accepted", "layout_probe", "consumer1_claimed_4", "consumer1_died_by_sigkill", "killed_claims_still_pending", "redelivered_exactly_the_killed_claims", "consumer2_graceful_exit")
	counts := s.produce(ctx, engEnqueue(eng), items, 4, 0, 1)
	s.Check("all_submits_accepted", counts[ledger.ResAccepted] == len(items), "%s", countStr(counts))
	s.probeLayout(ctx, rr, q, items[0].ID)

	c1, err := env.spawnConsumer(in.Addr, q, "c1", lease, 4, true)
	if !s.CheckErr("spawn_consumer1", err) {
		return s.Finish(started)
	}
	s.Extra = append(s.Extra, c1.ledger)
	// 等 4 个 worker 都领取（proc_start 已落盘）
	deadline := time.Now().Add(15 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		if n, err = countStarts(c1.ledger); err == nil && n >= 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !s.Check("consumer1_claimed_4", n >= 4, "consumer1 ledger shows %d claimed (last read err: %v)", n, err) {
		_ = c1.terminate(time.Second)
		return s.Finish(started)
	}
	tKill := time.Now()
	s.Event("sigkill_consumer1", fmt.Sprintf("pid=%d", c1.cmd.Process.Pid))
	s.CheckErr("kill_consumer1", c1.cmd.Process.Signal(syscall.SIGKILL))
	<-c1.waited
	werr := c1.werr
	var ee *exec.ExitError
	killed := false
	if errors.As(werr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
			killed = true
		}
	}
	s.Check("consumer1_died_by_sigkill", killed, "wait result: %v", werr)
	// 被杀进程的账本可能在崩溃瞬间有半行吗？每次追加是单次 write，进程被杀不会截断已完成的 write。
	started1, err := ledger.ReadFile(c1.ledger)
	heldIDs := map[string]bool{}
	if s.CheckErr("read_consumer1_ledger", err) {
		for _, r := range started1 {
			if r.K == ledger.KProcStart {
				heldIDs[r.ID] = true
			}
		}
	}
	s.Metrics["held_by_killed_consumer"] = len(heldIDs)
	st, err := queueState(ctx, rr, q, false)
	if s.CheckErr("state_after_kill", err) {
		s.Metrics["state_after_kill"] = st
		s.Check("killed_claims_still_pending", st.Pending == int64(len(heldIDs)), "pending=%d held=%d", st.Pending, len(heldIDs))
	}

	c2, err := env.spawnConsumer(in.Addr, q, "c2", lease, 4, false)
	if !s.CheckErr("spawn_consumer2", err) {
		return s.Finish(started)
	}
	s.Extra = append(s.Extra, c2.ledger)
	pending, werr2 := s.waitFinal(ctx, rr, q, wantMap(items), nil, 30*time.Second)
	tAllDone := time.Now()
	s.Metrics["kill_to_all_confirmed_s"] = tAllDone.Sub(tKill).Seconds()
	if werr2 != nil {
		s.Check("all_confirmed_after_recovery", false, "%v; %d unconfirmed", werr2, len(pending))
		s.CheckErr("wait_final", errIfNotTimeout(werr2))
	}
	// 全部唯一任务确认后，再观察至少一个租期，捕获迟到的重复执行
	obs := lease + 1500*time.Millisecond
	s.Event("extra_observation", obs.String())
	time.Sleep(obs)
	_ = c2.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c2.waited:
		s.Check("consumer2_graceful_exit", c2.werr == nil, "consumer2 wait result: %v", c2.werr)
	case <-time.After(30 * time.Second):
		s.Check("consumer2_graceful_exit", false, "did not exit within 30s of SIGTERM")
		_ = c2.terminate(time.Second) // 精确 PID 强杀并回收
	}
	recs, err := ledger.ReadFiles(append([]string{s.L.Path()}, s.Extra...)...)
	if s.CheckErr("read_ledgers", err) {
		starts := map[string]int{}
		for _, r := range recs {
			if r.K == ledger.KProcStart {
				starts[r.ID]++
			}
		}
		dups := 0
		for _, c := range starts {
			dups += c - 1
		}
		s.Metrics["duplicate_calls"] = dups
		s.Check("redelivered_exactly_the_killed_claims", dups == len(heldIDs), "duplicates=%d held-by-killed=%d", dups, len(heldIDs))
		for id := range heldIDs {
			if starts[id] < 2 {
				s.Check("held_id_redelivered", false, "%s executed %d time(s)", id, starts[id])
			}
		}
	}
	s.reconcileQuiescent(ctx, rr, q, wantMap(items), dedupWindow, QState{Tombs: int64(len(items))})
	return s.Finish(started, "process crash (SIGKILL of consumer), not disk corruption")
}

// ---------------------------------------------------------------------------
// Redis 进程 SIGKILL，同卷恢复

func scRedisCrash(ctx context.Context, env *Env) bool {
	started := time.Now()
	s, err := env.newSc("redis-sigkill")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	in, err := env.newRedis(ctx, redisOpts{Tag: "crash", AppendOnly: true, AofAlways: true})
	if !s.CheckErr("redis_start", err) {
		return s.Finish(started)
	}
	const lease = 5 * time.Second
	q := qname(env, "redis-sigkill")
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	rr := rawClient(in.Addr)
	eng, err := engine.NewRedisStreamEngine(ctx, rc, engine.ReliableOptions{Name: q, LeaseDuration: lease, DedupWindow: dedupWindow})
	if !s.CheckErr("new_engine", err) {
		return s.Finish(started)
	}
	s.Require("seed_state_matches_plan", "selected_state_identical_after_restart", "tomb_ttl_survived", "redis_exit_137_not_oom", "redis_stopped",
		"failed_records_listed", "resubmit_done_different_payload_conflicts")
	roles, err := s.seed(ctx, eng, seedPlan{Prefix: "c", Done: 10, Delayed: 10, Failed: 5, Inflight: 10, Ready: 50, DelayFor: 6 * time.Second, Size: 512})
	if !s.CheckErr("seed", err) {
		return s.Finish(started)
	}
	rc.Close()
	pre, err := queueState(ctx, rr, q, false)
	if s.CheckErr("state_before_kill", err) {
		s.Metrics["state_before_kill"] = pre
		want := roles.seedState()
		s.Check("seed_state_matches_plan", pre == want, "got %+v want %+v", pre, want)
	}
	before, err := takeSnap(ctx, rr, q)
	if !s.CheckErr("snapshot_before", err) {
		return s.Finish(started)
	}
	ptBefore, err := tombPTTL(ctx, rr, q, roles.Done[0].ID)
	s.CheckErr("tomb_pttl_before", err)
	rr.Close()

	// ---- 杀 Redis ----
	tKill := time.Now()
	s.Event("redis_sigkill", in.C.Name)
	if !s.CheckErr("docker_kill", env.D.Kill(ctx, in.C, "KILL")) {
		return s.Finish(started)
	}
	stopped := false
	for i := 0; i < 100; i++ {
		ds, err := env.D.Inspect(ctx, in.C)
		if !s.CheckErr("inspect_after_kill", err) {
			return s.Finish(started)
		}
		if !ds.Running {
			stopped = true
			s.Check("redis_exit_137_not_oom", ds.ExitCode == 137 && !ds.OOMKilled, "exit=%d oom=%v", ds.ExitCode, ds.OOMKilled)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Check("redis_stopped", stopped, "container still running 5s after SIGKILL")
	tStartCmd := time.Now()
	if !s.CheckErr("docker_start", env.D.Start(ctx, in.C)) {
		return s.Finish(started)
	}
	tStartDone := time.Now()
	if !s.CheckErr("wait_ready", env.waitReady(ctx, in, 30*time.Second)) {
		return s.Finish(started)
	}
	tPing := time.Now()
	s.Metrics["kill_to_ping_s"] = tPing.Sub(tKill).Seconds()
	s.Metrics["start_cmd_s"] = tStartDone.Sub(tStartCmd).Seconds()
	s.Metrics["start_done_to_ping_s"] = tPing.Sub(tStartDone).Seconds()

	rr = rawClient(in.Addr)
	defer rr.Close()
	after, err := takeSnap(ctx, rr, q)
	if s.CheckErr("snapshot_after", err) {
		d := diffSnap(before, after)
		s.Check("selected_state_identical_after_restart", len(d) == 0, "%d differences, first: %v", len(d), firstN(d, 5))
		s.Metrics["snapshot_keys"] = len(before)
	}
	if ptAfter, err := tombPTTL(ctx, rr, q, roles.Done[0].ID); s.CheckErr("tomb_pttl_after", err) {
		s.Check("tomb_ttl_survived", ptAfter > 0 && ptAfter <= ptBefore, "before=%v after=%v", ptBefore, ptAfter)
	}

	// ---- 恢复后消费 ----
	rc2, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client_after", err) {
		return s.Finish(started)
	}
	defer rc2.Close()
	replay := roles.Failed[0]
	mustNot := []string{}
	for _, it := range roles.Done {
		mustNot = append(mustNot, it.ID)
	}
	var expFailed []string
	for _, it := range roles.Failed[1:] {
		mustNot = append(mustNot, it.ID)
		expFailed = append(expFailed, it.ID)
	}
	s.Exp = ledger.Expect{MustNotProcess: mustNot, ExpectedFailed: expFailed} // 恢复后每个任务恰好一次
	m, err := queue.NewManager(&recProc{L: s.L}, mgrCfg(q, rc2, 8, lease))
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	lf, err := m.ListFailed(ctx, 1000)
	if s.CheckErr("list_failed", err) {
		ok := len(lf) == len(roles.Failed)
		got := map[string]bool{}
		for _, f := range lf {
			got[f.ID] = true
			ok = ok && f.Reason == "seed-failed"
		}
		for _, it := range roles.Failed {
			ok = ok && got[it.ID]
		}
		s.Check("failed_records_listed", ok, "listed %d", len(lf))
	}
	s.CheckErr("replay_failed", m.ReplayFailed(ctx, replay.ID))
	// 已完成任务：同内容重复提交应视为重复（不再执行），不同内容应冲突
	d0 := roles.Done[0]
	s.CheckErr("resubmit_done_same_payload", m.AddDataWithID(ctx, d0.ID, d0.Payload))
	other, _ := ledger.MakePayload("zz-other-"+d0.ID, len(d0.Payload))
	cerr := m.AddDataWithID(ctx, d0.ID, other)
	s.Check("resubmit_done_different_payload_conflicts", errors.Is(cerr, common.ErrIDConflict), "got %v", cerr)

	doneSet := map[string]string{}
	for _, it := range roles.Done {
		doneSet[it.ID] = it.Sha
	}
	for _, g := range [][]item{roles.Delayed, roles.Inflight, roles.Ready} {
		for _, it := range g {
			doneSet[it.ID] = it.Sha
		}
	}
	doneSet[replay.ID] = replay.Sha
	pending, werr := s.waitFinal(ctx, rr, q, doneSet, nil, 40*time.Second)
	s.Metrics["restart_to_all_confirmed_s"] = time.Since(tPing).Seconds()
	if werr != nil {
		s.Check("all_confirmed_after_restart", false, "%v; %d unconfirmed (first %v)", werr, len(pending), firstN(pending, 3))
		s.CheckErr("wait_final", errIfNotTimeout(werr))
	}
	obs := lease + 1500*time.Millisecond
	s.Event("extra_observation", obs.String())
	time.Sleep(obs)
	s.stopMgr(m)
	failSet := map[string]string{}
	for _, it := range roles.Failed[1:] {
		failSet[it.ID] = it.Sha
	}
	_, werr = s.waitFinal(ctx, rr, q, failSet, map[string]bool{roles.Failed[1].ID: true, roles.Failed[2].ID: true, roles.Failed[3].ID: true, roles.Failed[4].ID: true}, 5*time.Second)
	s.CheckErr("failed_final_observation", werr)
	s.reconcileQuiescent(ctx, rr, q, doneSet, dedupWindow, QState{Failed: 4, Records: 4, Tombs: int64(len(doneSet))})
	return s.Finish(started, "Redis process SIGKILL with same volume (AOF always); not host power loss or disk corruption; state compared on selected fields only")
}

// ---------------------------------------------------------------------------
// OOM

func scOOM(ctx context.Context, env *Env) bool {
	started := time.Now()
	s, err := env.newSc("oom")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	in, err := env.newRedis(ctx, redisOpts{Tag: "oom", MaxMemory: "16mb", AppendOnly: true, AofAlways: true})
	if !s.CheckErr("redis_start", err) {
		return s.Finish(started)
	}
	const lease = 3 * time.Second
	q := qname(env, "oom")
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	rr := rawClient(in.Addr)
	defer rr.Close()
	s.Require("pre_oom_submits_accepted", "reached_oom", "oom_rejected_some_submits", "no_false_success", "no_false_rejection", "fill_released", "resubmits_all_accepted")
	hold := make(chan struct{})
	m, err := queue.NewManager(&recProc{L: s.L, Hold: hold}, mgrCfg(q, rc, 4, lease))
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	s.Exp = ledger.Expect{AllowRedelivery: true}
	a, _ := makeItems("oa", 20, 1024)
	b, _ := makeItems("ob", 100, 1024)
	ca := s.produce(ctx, mgrEnqueue(m), a, 4, 0, 1)
	s.Check("pre_oom_submits_accepted", ca[ledger.ResAccepted] == len(a), "%s", countStr(ca))
	// 等 4 个 worker 领取并阻塞在 Processor 内
	time.Sleep(1500 * time.Millisecond)

	// 填充至 OOM（填充键不在队列命名空间；记录精确键名用于精确释放）
	var fill []string
	blob := strings.Repeat("x", 100*1024)
	hitOOM := false
	for i := 0; i < 400; i++ {
		key := fmt.Sprintf("tps-fill:%s:%03d", env.RunID, i)
		err := rr.Set(ctx, key, blob, 0).Err()
		if err != nil {
			if strings.HasPrefix(err.Error(), "OOM ") {
				hitOOM = true
				break
			}
			s.CheckErr("fill_set", err)
			break
		}
		fill = append(fill, key)
	}
	s.Check("reached_oom", hitOOM, "no OOM after %d fill keys", len(fill))
	s.Metrics["fill_keys"] = len(fill)
	if !hitOOM {
		return s.Finish(started)
	}
	s.usedMemory(ctx, rr, "at_oom")

	cb := s.produce(ctx, mgrEnqueue(m), b, 4, 0, 1)
	s.Metrics["submits_during_oom"] = cb
	s.Check("oom_rejected_some_submits", cb[ledger.ResRejected] > 0, "%s", countStr(cb))
	// 逐ID核对：accepted 必须在 Redis，rejected 必须不在（接收账本与服务端事实对应）
	recs, err := ledger.ReadFile(s.L.Path())
	falseOK, falseFail, uncertainResolved := 0, 0, 0
	if s.CheckErr("read_ledger", err) {
		k := keysFor(q)
		for _, r := range recs {
			if r.K != ledger.KSubmit || !strings.HasPrefix(r.ID, "ob-") {
				continue
			}
			n, err := rr.Exists(ctx, k.mp+r.ID).Result()
			if !s.CheckErr("exists_"+r.ID, err) {
				continue
			}
			switch {
			case r.Res == ledger.ResAccepted && n == 0:
				falseOK++
			case r.Res == ledger.ResRejected && n == 1:
				falseFail++
			case r.Res == ledger.ResUncertain:
				uncertainResolved++
			}
		}
		s.Check("no_false_success", falseOK == 0, "%d accepted ids missing in Redis", falseOK)
		s.Check("no_false_rejection", falseFail == 0, "%d rejected ids present in Redis", falseFail)
		s.Metrics["uncertain_during_oom"] = uncertainResolved
	}

	// 放开 Processor：Ack 在 OOM 下预期失败（记录观察，不预设数量）
	close(hold)
	time.Sleep(lease + 1500*time.Millisecond)
	if st, err := queueState(ctx, rr, q, false); s.CheckErr("state_during_oom_after_release", err) {
		s.Metrics["state_during_oom_after_release"] = st
	}

	// 释放填充键（精确键名）
	delErr := error(nil)
	if len(fill) > 0 {
		delErr = rr.Del(ctx, fill...).Err()
	}
	if s.CheckErr("release_fill", delErr) {
		left, err := rr.Exists(ctx, fill...).Result()
		if s.CheckErr("verify_fill_released", err) {
			s.Check("fill_released", left == 0, "%d fill keys remain", left)
		}
	}
	s.usedMemory(ctx, rr, "after_release")

	// 重投所有未被明确接受的提交（同ID同内容：幂等）
	var retry []item
	for _, it := range b {
		for _, r := range recs {
			if r.K == ledger.KSubmit && r.ID == it.ID && r.Res != ledger.ResAccepted {
				retry = append(retry, it)
				break
			}
		}
	}
	cr := s.produce(ctx, mgrEnqueue(m), retry, 4, 0, 2)
	s.Metrics["resubmit_after_release"] = cr
	s.Check("resubmits_all_accepted", cr[ledger.ResAccepted] == len(retry), "%s", countStr(cr))

	all := wantMap(append(append([]item{}, a...), b...))
	pending, werr := s.waitFinal(ctx, rr, q, all, nil, 40*time.Second)
	if werr != nil {
		s.Check("all_accepted_confirmed", false, "%v; %d unconfirmed (first %v)", werr, len(pending), firstN(pending, 3))
		s.CheckErr("wait_final", errIfNotTimeout(werr))
	}
	time.Sleep(lease + 1500*time.Millisecond) // 额外观察一个租期，累计重复
	s.stopMgr(m)
	s.reconcileQuiescent(ctx, rr, q, all, dedupWindow, QState{Tombs: int64(len(all))})
	return s.Finish(started, "executions/duplicates are cumulative across the whole scenario; Stop success is not treated as confirmation")
}

// ---------------------------------------------------------------------------
// RDB 备份 → 另一个 owner 标签容器/卷恢复

func scBackup(ctx context.Context, env *Env) bool {
	started := time.Now()
	s, err := env.newSc("backup-restore")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	src, err := env.newRedis(ctx, redisOpts{Tag: "bk-src", AppendOnly: true, AofAlways: true})
	if !s.CheckErr("source_start", err) {
		return s.Finish(started)
	}
	const lease = 5 * time.Second
	q := qname(env, "backup-restore")
	rc, err := queueClient(src.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	rr := rawClient(src.Addr)
	eng, err := engine.NewRedisStreamEngine(ctx, rc, engine.ReliableOptions{Name: q, LeaseDuration: lease, DedupWindow: dedupWindow})
	if !s.CheckErr("new_engine", err) {
		return s.Finish(started)
	}
	s.Require("bgsave_completed_ok", "rdb_checksum_ok", "rdb_non_empty", "post_snapshot_writes_accepted", "restore_aof_rewrite_done",
		"restored_state_equals_snapshot_time_state", "post_snapshot_writes_absent")
	roles, err := s.seed(ctx, eng, seedPlan{Prefix: "p", Done: 10, Delayed: 5, Failed: 5, Inflight: 5, Ready: 40, DelayFor: 6 * time.Second, Size: 512})
	if !s.CheckErr("seed", err) {
		return s.Finish(started)
	}
	pre, err := takeSnap(ctx, rr, q)
	if !s.CheckErr("snapshot_pre_backup", err) {
		return s.Finish(started)
	}
	// BGSAVE 并等待完成；超时是失败，不忽略
	info0, err := rr.Info(ctx, "persistence").Result()
	if !s.CheckErr("info_persistence_before", err) {
		return s.Finish(started)
	}
	saves0, err := infoInt(info0, "rdb_saves")
	if !s.CheckErr("rdb_saves_before", err) {
		return s.Finish(started)
	}
	tBg := time.Now()
	if !s.CheckErr("bgsave", rr.BgSave(ctx).Err()) {
		return s.Finish(started)
	}
	okSave := false
	for time.Since(tBg) < 30*time.Second {
		info, err := rr.Info(ctx, "persistence").Result()
		if !s.CheckErr("info_persistence_poll", err) {
			return s.Finish(started)
		}
		saves, err := infoInt(info, "rdb_saves")
		if !s.CheckErr("rdb_saves_poll", err) {
			return s.Finish(started)
		}
		if saves > saves0 && infoField(info, "rdb_bgsave_in_progress") == "0" {
			okSave = infoField(info, "rdb_last_bgsave_status") == "ok"
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tSnap := time.Now()
	s.Check("bgsave_completed_ok", okSave, "BGSAVE did not complete with status ok within 30s")
	if !okSave {
		return s.Finish(started)
	}
	s.Metrics["bgsave_seconds"] = tSnap.Sub(tBg).Seconds()
	if ls, err := rr.LastSave(ctx).Result(); s.CheckErr("lastsave", err) {
		s.Metrics["snapshot_lastsave_unix"] = ls
	}
	s.Event("snapshot_done", tSnap.Format(time.RFC3339Nano))

	// 备份之后的写入：不应出现在快照里
	post, _ := makeItems("p-post", 20, 512)
	for i := range post {
		post[i].Tag = "after_snapshot"
	}
	cp := s.produce(ctx, engEnqueue(eng), post, 1, 0, 1)
	s.Check("post_snapshot_writes_accepted", cp[ledger.ResAccepted] == len(post), "%s", countStr(cp))
	var lostIDs []string
	for _, it := range post {
		lostIDs = append(lostIDs, it.ID)
	}

	// 校验并导出 RDB
	chk, err := env.D.Exec(ctx, src.C, "redis-check-rdb", "/data/dump.rdb")
	if s.CheckErr("redis_check_rdb", err) {
		s.Check("rdb_checksum_ok", strings.Contains(chk, "Checksum OK") || strings.Contains(chk, "RDB looks OK"), "output: %s", truncate(strings.TrimSpace(chk), 300))
	}
	rdbPath := env.path("backup-dump.rdb")
	if !s.CheckErr("docker_cp_out", env.D.CopyFrom(ctx, src.C, "/data/dump.rdb", rdbPath)) {
		return s.Finish(started)
	}
	fi, err := os.Stat(rdbPath)
	if !s.CheckErr("stat_rdb", err) || !s.Check("rdb_non_empty", fi.Size() > 0, "size=%d", fi.Size()) {
		return s.Finish(started)
	}
	data, err := os.ReadFile(rdbPath)
	if s.CheckErr("read_rdb", err) {
		s.Metrics["rdb_sha256"], s.Metrics["rdb_bytes"] = ledger.SHA256Hex(data), len(data)
	}
	rc.Close()
	rr.Close()

	// 恢复到另一个容器 + 新卷（同 owner/run 标签，purpose 不同）。恢复时间从创建前起计。
	tRestore0 := time.Now()
	dst, err := env.newRedis(ctx, redisOpts{Tag: "bk-restore", AppendOnly: false, CreateOnly: true})
	if !s.CheckErr("restore_create", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("restore_cp_in", env.D.CopyTo(ctx, dst.C, rdbPath, "/data/dump.rdb")) {
		return s.Finish(started)
	}
	tStartCmd := time.Now()
	if !s.CheckErr("restore_start", env.D.Start(ctx, dst.C)) {
		return s.Finish(started)
	}
	tStartDone := time.Now()
	if !s.CheckErr("restore_wait_ready", env.waitReady(ctx, dst, 30*time.Second)) {
		return s.Finish(started)
	}
	tPing := time.Now()
	s.Metrics["restore_create_to_ping_s"] = tPing.Sub(tRestore0).Seconds()
	s.Metrics["restore_start_cmd_s"] = tStartDone.Sub(tStartCmd).Seconds()
	s.Metrics["restore_start_done_to_ping_s"] = tPing.Sub(tStartDone).Seconds()
	dr := rawClient(dst.Addr)
	defer dr.Close()
	// 启用 AOF 并等待重写完成
	s.CheckErr("restore_enable_aof", dr.ConfigSet(ctx, "appendonly", "yes").Err())
	s.CheckErr("restore_aof_always", dr.ConfigSet(ctx, "appendfsync", "always").Err())
	aofOK := false
	for i := 0; i < 600; i++ {
		info, err := dr.Info(ctx, "persistence").Result()
		if !s.CheckErr("restore_info_persistence", err) {
			break
		}
		if infoField(info, "aof_enabled") == "1" && infoField(info, "aof_rewrite_in_progress") == "0" && infoField(info, "aof_last_bgrewrite_status") == "ok" {
			aofOK = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Check("restore_aof_rewrite_done", aofOK, "AOF not enabled/rewritten within 30s")
	s.Metrics["restore_total_with_aof_s"] = time.Since(tRestore0).Seconds()

	got, err := takeSnap(ctx, dr, q)
	if s.CheckErr("snapshot_restored", err) {
		d := diffSnap(pre, got)
		s.Check("restored_state_equals_snapshot_time_state", len(d) == 0, "%d differences, first %v", len(d), firstN(d, 5))
	}
	k := keysFor(q)
	postPresent := 0
	for _, it := range post {
		n, err := dr.Exists(ctx, k.mp+it.ID).Result()
		if !s.CheckErr("post_exists_"+it.ID, err) {
			continue
		}
		postPresent += int(n)
	}
	s.Check("post_snapshot_writes_absent", postPresent == 0, "%d post-snapshot tasks present in restored instance", postPresent)
	s.Metrics["rpo_lost_ids"] = len(post)

	// 在恢复库上消费
	rc2, err := queueClient(dst.Addr)
	if !s.CheckErr("queue_client_restored", err) {
		return s.Finish(started)
	}
	defer rc2.Close()
	mustNot := []string{}
	for _, it := range roles.Done {
		mustNot = append(mustNot, it.ID)
	}
	var expFailed []string
	for _, it := range roles.Failed {
		mustNot = append(mustNot, it.ID)
		expFailed = append(expFailed, it.ID)
	}
	s.Exp = ledger.Expect{MustNotProcess: mustNot, ExpectedFailed: expFailed, ExpectedLost: lostIDs}
	m, err := queue.NewManager(&recProc{L: s.L}, mgrCfg(q, rc2, 8, lease))
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	doneSet := map[string]string{}
	for _, it := range roles.Done {
		doneSet[it.ID] = it.Sha
	}
	for _, g := range [][]item{roles.Delayed, roles.Inflight, roles.Ready} {
		for _, it := range g {
			doneSet[it.ID] = it.Sha
		}
	}
	pending, werr := s.waitFinal(ctx, dr, q, doneSet, nil, 40*time.Second)
	if werr != nil {
		s.Check("all_snapshot_tasks_confirmed", false, "%v; %d unconfirmed (first %v)", werr, len(pending), firstN(pending, 3))
		s.CheckErr("wait_final", errIfNotTimeout(werr))
	}
	time.Sleep(lease + 1500*time.Millisecond)
	s.stopMgr(m)
	failSet, failOK := map[string]string{}, map[string]bool{}
	for _, it := range roles.Failed {
		failSet[it.ID], failOK[it.ID] = it.Sha, true
	}
	_, werr = s.waitFinal(ctx, dr, q, failSet, failOK, 5*time.Second)
	s.CheckErr("failed_final_observation", werr)
	s.reconcileQuiescent(ctx, dr, q, doneSet, dedupWindow, QState{Failed: int64(len(roles.Failed)), Records: int64(len(roles.Failed)), Tombs: int64(len(doneSet))})
	return s.Finish(started, "RPO is bounded by the snapshot instant, not by when the file was copied; RTO measured on a tiny dataset")
}

var _ = redis.Nil
