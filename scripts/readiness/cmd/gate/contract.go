package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"

	"taurus-readiness/ledger"
)

// ---------------------------------------------------------------------------
// 公开 API 契约（经打包安装的候选，只用公开入口；Redis 键探针仅用于确认终态，单独标注 _probe）
//
// 覆盖：配置拒绝、单次生命周期、成功、RetryableError 重试、panic 重试、重试耗尽留存、普通错误直接留存、
// MaxRetries=0、ListFailed 内容/顺序/上下限、ReplayFailed（成功/非失败/未知ID）、稳定ID去重/冲突
// （执行中、失败留存、完成后窗口内、窗口到期后）、非法ID/空数据、ctx 取消/超时映射、Stop 成功/合作式超时
// 后恢复/非合作式超时如实报告、内存引擎的 QueueSize/失败/重放。

func scContract(ctx context.Context, env *Env, in *Inst) bool {
	started := time.Now()
	s, err := env.newSc("contract")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	rr := rawClient(in.Addr)
	defer rr.Close()

	// req 登记为必需并断言：任何一个契约断言没有执行到都会以 missing_required_check 失败
	req := func(name string, ok bool, f string, a ...any) bool {
		s.Require(name)
		return s.Check(name, ok, f, a...)
	}
	is := func(err, target error) bool { return errors.Is(err, target) }
	expCalls := map[string]int{}
	var expFailed []string
	stop := func(m *queue.Manager, d time.Duration) error {
		c, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return m.Stop(c)
	}
	waitCalls := func(p *scriptProc, id string, n int, within time.Duration) bool {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if p.Calls(id) >= n {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	// ---- A. 配置拒绝（NewManager 不应创建任何东西） ----
	for _, c := range []struct {
		name string
		mut  func(*queue.Config)
	}{
		{"empty_source", func(c *queue.Config) { c.Source = "" }},
		{"invalid_source", func(c *queue.Config) { c.Source = "bad name" }},
		{"lease_below_500ms", func(c *queue.Config) { c.LeaseDuration = 100 * time.Millisecond }},
		{"negative_max_retries", func(c *queue.Config) { c.MaxRetries = -1 }},
		{"negative_dedup_window", func(c *queue.Config) { c.DedupWindow = -time.Second }},
		{"unknown_engine", func(c *queue.Config) { c.EngineType = "kafka" }},
	} {
		cfg := mgrCfg(qname(env, "contract-cfg"), rc, 2, 30*time.Second)
		c.mut(cfg)
		_, err := queue.NewManager(&recProc{L: s.L}, cfg)
		req("config_rejects_"+c.name, err != nil, "NewManager err=%v", err)
	}
	_, err = queue.NewManager(nil, mgrCfg(qname(env, "contract-cfg"), rc, 2, 30*time.Second))
	req("config_rejects_nil_processor", err != nil, "err=%v", err)
	_, err = queue.NewManager(&recProc{L: s.L}, nil)
	req("config_rejects_nil_config", err != nil, "err=%v", err)
	callerCfg := mgrCfg(qname(env, "contract-cfg"), rc, 2, 30*time.Second)
	callerCfg.MaxRetries, callerCfg.PollInterval = 0, 0
	if _, err := queue.NewManager(&recProc{L: s.L}, callerCfg); s.CheckErr("new_manager_cfg_copy", err) {
		req("config_not_mutated_and_zero_retries_kept", callerCfg.MaxRetries == 0 && callerCfg.PollInterval == 0, "caller config after NewManager: MaxRetries=%d PollInterval=%v", callerCfg.MaxRetries, callerCfg.PollInterval)
	}

	// ---- B. 单次生命周期 ----
	lp, _ := ledger.MakePayload("c-life-0", 256)
	ml, err := queue.NewManager(&recProc{L: s.L}, mgrCfg(qname(env, "contract-life"), rc, 2, 30*time.Second))
	if !s.CheckErr("new_manager_life", err) {
		return s.Finish(started)
	}
	req("add_before_start_not_running", is(ml.AddDataWithID(ctx, "c-life-0", lp), common.ErrManagerNotRunning), "AddDataWithID before Start")
	req("status_before_start_stopped", ml.GetStatus() == queue.StatusStopped, "status=%v", ml.GetStatus())
	req("stop_before_start_not_running", is(stop(ml, 5*time.Second), common.ErrManagerNotRunning), "Stop before Start")
	if !s.CheckErr("life_start", ml.Start()) {
		return s.Finish(started)
	}
	req("start_twice_already_running", is(ml.Start(), common.ErrManagerAlreadyRunning), "second Start")
	req("status_running", ml.GetStatus() == queue.StatusRunning, "status=%v", ml.GetStatus())
	req("stop_idle_ok", stop(ml, 20*time.Second) == nil, "Stop on an idle manager")
	req("stop_twice_not_running", is(stop(ml, 5*time.Second), common.ErrManagerNotRunning), "second Stop")
	req("start_after_stop_closed", is(ml.Start(), common.ErrManagerClosed), "Start after Stop")
	req("add_after_stop_not_running", is(ml.AddData(ctx, lp), common.ErrManagerNotRunning), "AddData after Stop")
	req("status_after_stop_stopped", ml.GetStatus() == queue.StatusStopped, "status=%v", ml.GetStatus())

	// ---- C. 主流程 ----
	qM := qname(env, "contract-main")
	hold := make(chan struct{})
	p := &scriptProc{L: s.L, Release: hold, Decide: func(id string, n int) action {
		pre := func(x string) bool { return strings.HasPrefix(id, x) }
		switch {
		case pre("c-retry1-"), pre("c-zero-"):
			if n == 1 || pre("c-zero-") {
				return actRetryable
			}
		case pre("c-panic1-"), pre("c-zeropanic-"):
			if n == 1 || pre("c-zeropanic-") {
				return actPanic
			}
		case pre("c-exhaust-"):
			return actRetryable
		case pre("c-panicx-"):
			return actPanic
		case pre("c-fatal-"):
			if n == 1 {
				return actFatal
			}
		case pre("c-fatalkeep-"):
			return actFatal
		case pre("c-hold-"):
			if n == 1 {
				return actBlockHard
			}
		}
		return actOK
	}}
	cfg := mgrCfg(qM, rc, 4, 30*time.Second)
	cfg.MaxRetries, cfg.RetryDelay, cfg.MaxRetryDelay = 2, 100*time.Millisecond, 300*time.Millisecond
	m, err := queue.NewManager(p, cfg)
	if !s.CheckErr("new_manager_main", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start_main", m.Start()) {
		return s.Finish(started)
	}
	mk := func(prefix string, n int) []item {
		its, err := makeItems(prefix, n, 512)
		if err != nil {
			panic(err)
		}
		return its
	}
	ok5, retry1, panic1 := mk("c-ok", 5), mk("c-retry1", 3), mk("c-panic1", 3)
	exhaust, panicx, fatal, fatalkeep, held := mk("c-exhaust", 2), mk("c-panicx", 1), mk("c-fatal", 2), mk("c-fatalkeep", 2), mk("c-hold", 1)
	var all []item
	for _, g := range [][]item{ok5, retry1, panic1, exhaust, panicx, fatal, fatalkeep, held} {
		all = append(all, g...)
	}
	set := func(g []item, calls int, failed bool) {
		for _, it := range g {
			expCalls[it.ID] = calls
			if failed {
				expFailed = append(expFailed, it.ID)
			}
		}
	}
	set(ok5, 1, false)
	set(retry1, 2, false)
	set(panic1, 2, false)
	set(exhaust, cfg.MaxRetries+1, true)
	set(panicx, cfg.MaxRetries+1, true)
	set(fatal, 2, false) // 第 1 次致命错误留存，重放后第 2 次成功
	set(fatalkeep, 1, true)
	set(held, 1, false)
	counts := s.produce(ctx, mgrEnqueue(m), all, 4, 0, 1)
	req("all_submits_accepted", counts[ledger.ResAccepted] == len(all), "%s", countStr(counts))

	// 执行中（未完成）的稳定ID：同内容=重复(nil)，不同内容=冲突；都不得产生新执行
	h := held[0]
	req("held_task_started", waitCalls(p, h.ID, 1, 10*time.Second), "held task not started")
	// 布局探针只能打在确定未完成的任务上（被阻塞的 held 任务）：普通任务可能在探针前已确认并删除记录
	s.probeLayout(ctx, rr, qM, h.ID)
	s.Require("layout_probe")
	req("dedup_inflight_same_payload_nil", s.submit(ctx, mgrEnqueue(m), h, 2) == ledger.ResAccepted, "same id+payload while executing must be a duplicate (nil)")
	other, _ := ledger.MakePayload("zz-"+h.ID, len(h.Payload))
	req("dedup_inflight_different_payload_conflict", is(m.AddDataWithID(ctx, h.ID, other), common.ErrIDConflict), "different payload while executing")

	// 输入错误：不入队
	for i, bad := range []string{"", "-lead", "has space", strings.Repeat("a", 129), "x/y"} {
		err := m.AddDataWithID(ctx, bad, ok5[0].Payload)
		req(fmt.Sprintf("invalid_id_rejected_%d", i), is(err, common.ErrInvalidID), "err=%v", err)
	}
	req("empty_data_with_id_rejected", is(m.AddDataWithID(ctx, "c-empty-0", nil), common.ErrItemEmpty), "nil data")
	req("empty_data_rejected", is(m.AddData(ctx, []byte{}), common.ErrItemEmpty), "empty data")
	// ctx 边界：已取消/已超时的 ctx 被映射为公开错误；探针确认未写入（任何意外写入也会作为未知ID执行而被账本发现）
	cctx, ccancel := context.WithCancel(ctx)
	ccancel()
	cp, _ := ledger.MakePayload("c-ctxcancel-0", 256)
	err = m.AddDataWithID(cctx, "c-ctxcancel-0", cp)
	req("canceled_ctx_maps_to_ErrContextCanceled", is(err, common.ErrContextCanceled), "err=%v", err)
	ap, _ := ledger.MakePayload("c-ctxauto-0", 256)
	err = m.AddData(cctx, ap)
	req("canceled_ctx_auto_id_maps_to_ErrContextCanceled", is(err, common.ErrContextCanceled), "err=%v", err)
	dctx, dcancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	tp, _ := ledger.MakePayload("c-ctxdeadline-0", 256)
	err = m.AddDataWithID(dctx, "c-ctxdeadline-0", tp)
	dcancel()
	req("expired_ctx_maps_to_ErrContextTimeout", is(err, common.ErrContextTimeout), "err=%v", err)
	k := keysFor(qM)
	if n, err := rr.Exists(ctx, k.mp+"c-ctxcancel-0", k.mp+"c-ctxdeadline-0").Result(); s.CheckErr("ctx_probe", err) {
		req("ctx_rejected_not_enqueued_probe", n == 0, "%d records exist for ctx-rejected submissions", n)
	}
	_, err = m.ListFailed(cctx, 10)
	req("list_failed_canceled_ctx_errors", err != nil, "ListFailed with canceled ctx returned nil error")
	close(hold)

	// 终态：成功组完成；失败组进入失败留存（fatal 也先进入失败留存）
	doneNow := wantMap(append(append(append(append([]item{}, ok5...), retry1...), panic1...), held...))
	if pend, werr := s.waitFinal(ctx, rr, qM, doneNow, nil, 20*time.Second); werr != nil {
		s.Check("main_done_within_deadline", false, "%v; pending %v", werr, firstN(pend, 5))
		s.CheckErr("wait_final_main", errIfNotTimeout(werr))
	}
	failNow := wantMap(append(append(append(append([]item{}, exhaust...), panicx...), fatal...), fatalkeep...))
	failOK := map[string]bool{}
	for id := range failNow {
		failOK[id] = true
	}
	if pend, werr := s.waitFinal(ctx, rr, qM, failNow, failOK, 20*time.Second); werr != nil {
		s.Check("main_failed_within_deadline", false, "%v; pending %v", werr, firstN(pend, 5))
		s.CheckErr("wait_failed_main", errIfNotTimeout(werr))
	}

	// ListFailed：内容、原因、次数、顺序、limit 上下限
	lf, err := m.ListFailed(ctx, 1000)
	if s.CheckErr("list_failed", err) {
		byID := map[string]queue.FailedMessage{}
		ordered := true
		for i, f := range lf {
			byID[f.ID] = f
			if i > 0 && f.FailedAt.Before(lf[i-1].FailedAt) {
				ordered = false
			}
		}
		req("list_failed_exact_set", len(lf) == len(failNow) && func() bool {
			for id := range failNow {
				if _, ok := byID[id]; !ok {
					return false
				}
			}
			return true
		}(), "listed %d, want %d", len(lf), len(failNow))
		req("list_failed_oldest_first", ordered, "FailedAt not ascending")
		bad := []string{}
		for _, g := range [][]item{exhaust, panicx, fatal, fatalkeep} {
			for _, it := range g {
				f := byID[it.ID]
				okData := string(f.Data) == string(it.Payload) && !f.FailedAt.IsZero()
				var okReason bool
				switch {
				case strings.HasPrefix(it.ID, "c-exhaust-"):
					okReason = strings.HasPrefix(f.Reason, "retries exhausted: ") && f.Attempts == cfg.MaxRetries+1
				case strings.HasPrefix(it.ID, "c-panicx-"):
					okReason = strings.HasPrefix(f.Reason, "retries exhausted: panic: ") && f.Attempts == cfg.MaxRetries+1
				default:
					okReason = strings.Contains(f.Reason, errScriptedFatal.Error()) && !strings.HasPrefix(f.Reason, "retries exhausted") && f.Attempts == 1
				}
				if !okData || !okReason {
					bad = append(bad, fmt.Sprintf("%s(data=%v reason=%q attempts=%d)", it.ID, okData, truncate(f.Reason, 60), f.Attempts))
				}
			}
		}
		req("list_failed_payload_reason_attempts", len(bad) == 0, "mismatches: %v", bad)
		two, err := m.ListFailed(ctx, 2)
		if s.CheckErr("list_failed_limit2", err) {
			req("list_failed_limit_returns_oldest", len(two) == 2 && len(lf) >= 2 && two[0].ID == lf[0].ID && two[1].ID == lf[1].ID, "limit=2 returned %d", len(two))
		}
	}
	for _, lim := range []int{0, -1, engine.MaxListFailed + 1} {
		_, err := m.ListFailed(ctx, lim)
		req(fmt.Sprintf("list_failed_limit_%d_rejected", lim), err != nil, "ListFailed(%d) returned nil error", lim)
	}
	// 失败留存中的稳定ID：同内容=重复且不执行，不同内容=冲突
	e0 := exhaust[0]
	req("dedup_failed_same_payload_nil", s.submit(ctx, mgrEnqueue(m), e0, 2) == ledger.ResAccepted, "same id+payload for a failed-retained task")
	o2, _ := ledger.MakePayload("zz-"+e0.ID, len(e0.Payload))
	req("dedup_failed_different_payload_conflict", is(m.AddDataWithID(ctx, e0.ID, o2), common.ErrIDConflict), "different payload for failed-retained task")

	// ReplayFailed：失败任务转回就绪并成功；非失败/未知ID报 ErrFailedNotFound
	for _, it := range fatal {
		req("replay_failed_ok_"+it.ID, m.ReplayFailed(ctx, it.ID) == nil, "ReplayFailed(%s)", it.ID)
	}
	req("replay_done_task_not_found", is(m.ReplayFailed(ctx, ok5[0].ID), common.ErrFailedNotFound), "ReplayFailed on a completed id")
	req("replay_unknown_not_found", is(m.ReplayFailed(ctx, "c-never-submitted"), common.ErrFailedNotFound), "ReplayFailed on an unknown id")
	if pend, werr := s.waitFinal(ctx, rr, qM, wantMap(fatal), nil, 20*time.Second); werr != nil {
		s.Check("replayed_done_within_deadline", false, "%v; pending %v", werr, pend)
		s.CheckErr("wait_replayed", errIfNotTimeout(werr))
	}
	req("replay_after_done_not_found", is(m.ReplayFailed(ctx, fatal[0].ID), common.ErrFailedNotFound), "second ReplayFailed after the replayed task completed")
	// 完成后窗口内：同内容=重复且不执行，不同内容=冲突
	o0 := ok5[0]
	req("dedup_done_same_payload_nil", s.submit(ctx, mgrEnqueue(m), o0, 2) == ledger.ResAccepted, "same id+payload after completion within DedupWindow")
	o3, _ := ledger.MakePayload("zz-"+o0.ID, len(o0.Payload))
	req("dedup_done_different_payload_conflict", is(m.AddDataWithID(ctx, o0.ID, o3), common.ErrIDConflict), "different payload after completion")
	time.Sleep(time.Second) // 给任何不该发生的重复执行留出时间（账本会把它记为重复）
	st := m.GetStats()
	req("stats_reports_workers_and_idle", st["workers"] == 4 && st["inflight"] == int64(0), "stats=%v/%v", st["workers"], st["inflight"])
	req("main_stop_ok", stop(m, 20*time.Second) == nil, "Stop after all tasks settled")
	doneAll := wantMap(append(append(append(append(append([]item{}, ok5...), retry1...), panic1...), held...), fatal...))
	keep := int64(len(exhaust) + len(panicx) + len(fatalkeep))
	s.reconcileQuiescent(ctx, rr, qM, doneAll, dedupWindow, QState{Failed: keep, Records: keep, Tombs: int64(len(doneAll))})

	// ---- D. MaxRetries=0：可重试错误与 panic 都直接失败留存（只执行一次） ----
	qZ := qname(env, "contract-zero")
	cz := mgrCfg(qZ, rc, 2, 30*time.Second)
	cz.MaxRetries = 0
	mz, err := queue.NewManager(p, cz)
	if s.CheckErr("new_manager_zero", err) && s.CheckErr("manager_start_zero", mz.Start()) {
		zs := append(mk("c-zero", 1), mk("c-zeropanic", 1)...)
		set(zs, 1, true)
		cnt := s.produce(ctx, mgrEnqueue(mz), zs, 1, 0, 1)
		req("zero_submits_accepted", cnt[ledger.ResAccepted] == len(zs), "%s", countStr(cnt))
		fz := map[string]bool{zs[0].ID: true, zs[1].ID: true}
		if pend, werr := s.waitFinal(ctx, rr, qZ, wantMap(zs), fz, 15*time.Second); werr != nil {
			s.Check("zero_failed_within_deadline", false, "%v; %v", werr, pend)
			s.CheckErr("wait_zero", errIfNotTimeout(werr))
		}
		lz, err := mz.ListFailed(ctx, 10)
		if s.CheckErr("list_failed_zero", err) {
			okz := len(lz) == 2
			for _, f := range lz {
				okz = okz && strings.HasPrefix(f.Reason, "retries exhausted: ") && f.Attempts == 1
			}
			req("zero_retries_fail_after_one_call", okz, "listed=%d", len(lz))
		}
		req("zero_stop_ok", stop(mz, 20*time.Second) == nil, "Stop")
	}

	// ---- E. Stop 合作式超时：返回 ErrStopTimeout，任务保留租约，另一 Manager 在租约到期后恢复 ----
	qS := qname(env, "contract-stopcoop")
	const shortLease = 2 * time.Second
	pc := &scriptProc{L: s.L, Decide: func(id string, n int) action {
		if n == 1 {
			return actBlockCoop
		}
		return actOK
	}}
	cs := mgrCfg(qS, rc, 1, shortLease)
	cs.Timeout = time.Second
	ms, err := queue.NewManager(pc, cs)
	sc := mk("c-stopcoop", 1)
	set(sc, 2, false)
	if s.CheckErr("new_manager_stopcoop", err) && s.CheckErr("manager_start_stopcoop", ms.Start()) {
		cnt := s.produce(ctx, mgrEnqueue(ms), sc, 1, 0, 1)
		req("stopcoop_submit_accepted", cnt[ledger.ResAccepted] == 1, "%s", countStr(cnt))
		req("stopcoop_started", waitCalls(pc, sc[0].ID, 1, 10*time.Second), "not started")
		t0 := time.Now()
		serr := stop(ms, 10*time.Second)
		el := time.Since(t0)
		s.Metrics["stop_coop_timeout_s"] = el.Seconds()
		req("stopcoop_returns_ErrStopTimeout", is(serr, common.ErrStopTimeout), "err=%v", serr)
		req("stopcoop_bounded", el < cs.Timeout+3*time.Second, "Stop took %v (Timeout %v + 2s grace)", el, cs.Timeout)
		if q, err := queueState(ctx, rr, qS, false); s.CheckErr("stopcoop_state", err) {
			req("stopcoop_task_kept_not_failed_probe", q.Failed == 0 && q.Pending == 1 && q.Leases == 1 && q.Records == 1, "%+v", q)
		}
		ms2, err := queue.NewManager(pc, cs)
		if s.CheckErr("new_manager_stopcoop2", err) && s.CheckErr("manager_start_stopcoop2", ms2.Start()) {
			if pend, werr := s.waitFinal(ctx, rr, qS, wantMap(sc), nil, 15*time.Second); werr != nil {
				s.Check("stopcoop_recovered_within_deadline", false, "%v; %v", werr, pend)
				s.CheckErr("wait_stopcoop", errIfNotTimeout(werr))
			}
			req("stopcoop_recovery_manager_stop_ok", stop(ms2, 20*time.Second) == nil, "Stop")
		}
	}

	// ---- F. Stop 非合作式超时：如实返回仍在运行；迟到的成功结果在租约有效时仍被确认 ----
	qH := qname(env, "contract-stophard")
	rel := make(chan struct{})
	ph := &scriptProc{L: s.L, Release: rel, Decide: func(string, int) action { return actBlockHard }}
	chh := mgrCfg(qH, rc, 1, 30*time.Second)
	chh.Timeout = time.Second
	mh, err := queue.NewManager(ph, chh)
	sh := mk("c-stophard", 1)
	set(sh, 1, false)
	var relOnce sync.Once
	release := func() { relOnce.Do(func() { close(rel) }) }
	defer release() // 任何提前返回都放行阻塞的 goroutine
	if s.CheckErr("new_manager_stophard", err) && s.CheckErr("manager_start_stophard", mh.Start()) {
		cnt := s.produce(ctx, mgrEnqueue(mh), sh, 1, 0, 1)
		req("stophard_submit_accepted", cnt[ledger.ResAccepted] == 1, "%s", countStr(cnt))
		req("stophard_started", waitCalls(ph, sh[0].ID, 1, 10*time.Second), "not started")
		serr := stop(mh, 10*time.Second)
		req("stophard_returns_ErrStopTimeout_still_running", is(serr, common.ErrStopTimeout) && serr != nil && strings.Contains(serr.Error(), "still running"), "err=%v", serr)
		req("stophard_status_stopped_and_closed", mh.GetStatus() == queue.StatusStopped && is(mh.Start(), common.ErrManagerClosed), "status=%v", mh.GetStatus())
		release()
		if pend, werr := s.waitFinal(ctx, rr, qH, wantMap(sh), nil, 10*time.Second); werr != nil {
			s.Check("stophard_late_success_confirmed", false, "%v; %v", werr, pend)
			s.CheckErr("wait_stophard", errIfNotTimeout(werr))
		}
	}
	for _, qq := range []string{qS, qH} {
		if q, err := queueState(ctx, rr, qq, false); s.CheckErr("stop_queues_state", err) {
			req("quiescent_"+qq, q == QState{Tombs: 1}, "%+v", q)
		}
	}

	// ---- G. DedupWindow 到期：窗口内重复不执行；墓碑到期后同ID视为新任务再次执行 ----
	qD := qname(env, "contract-expire")
	cd := mgrCfg(qD, rc, 1, 30*time.Second)
	cd.DedupWindow = 1500 * time.Millisecond
	md, err := queue.NewManager(&recProc{L: s.L}, cd)
	sd := mk("c-expire", 1)
	set(sd, 2, false)
	if s.CheckErr("new_manager_expire", err) && s.CheckErr("manager_start_expire", md.Start()) {
		d0 := sd[0]
		req("expire_first_accepted", s.submit(ctx, mgrEnqueue(md), d0, 1) == ledger.ResAccepted, "first submit")
		if _, werr := s.waitFinal(ctx, rr, qD, wantMap(sd), nil, 10*time.Second); s.CheckErr("wait_expire_first", werr) {
			req("expire_within_window_duplicate_nil", s.submit(ctx, mgrEnqueue(md), d0, 2) == ledger.ResAccepted, "resubmit inside window")
			if pt, err := tombPTTL(ctx, rr, qD, d0.ID); s.CheckErr("expire_pttl_probe", err) {
				req("expire_tomb_ttl_bounded_probe", pt > 0 && pt <= cd.DedupWindow, "PTTL=%v window=%v", pt, cd.DedupWindow)
			}
			gone := false
			for t := time.Now(); time.Since(t) < 5*time.Second; time.Sleep(100 * time.Millisecond) {
				n, err := rr.Exists(ctx, keysFor(qD).dp+d0.ID).Result()
				if !s.CheckErr("expire_exists_probe", err) {
					break
				}
				if n == 0 {
					gone = true
					break
				}
			}
			req("expire_tomb_gone_after_window_probe", gone, "tombstone still present 5s after window")
			req("expire_resubmit_after_window_accepted", s.submit(ctx, mgrEnqueue(md), d0, 3) == ledger.ResAccepted, "resubmit after window")
			_, werr = s.waitFinal(ctx, rr, qD, wantMap(sd), nil, 10*time.Second)
			s.CheckErr("wait_expire_second", werr)
		}
		req("expire_stop_ok", stop(md, 20*time.Second) == nil, "Stop")
	}

	// ---- H. 内存（channel）引擎：同一 Manager API；进程内、易失 ----
	contractMemory(ctx, s, req)

	s.Exp = ledger.Expect{ExpectCalls: expCalls, ExpectedFailed: expFailed}
	return s.Finish(started, "public API exercised through the packed candidate (no replace); checks suffixed _probe read Redis keys directly and are internal-state probes, not API contracts",
		"Redis-backed contracts only on the pinned Redis 8.10.2; memory engine checks are in-process and volatile by design")
}

// memProc 内存引擎契约用的 Processor（不写账本：内存引擎任务不在 Redis 中，无法逐ID对账）
type memProc struct {
	mu    sync.Mutex
	calls map[string]int
	ok    map[string]int
	block chan struct{}
}

func (p *memProc) Process(ctx context.Context, data []byte) error {
	id := string(data)
	p.mu.Lock()
	p.calls[id]++
	n := p.calls[id]
	p.mu.Unlock()
	switch {
	case id == "block":
		<-p.block
	case strings.HasPrefix(id, "fatal") && n == 1:
		return errors.New("memory fatal")
	}
	p.mu.Lock()
	p.ok[id]++
	p.mu.Unlock()
	return nil
}

func (p *memProc) okCount(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ok[id]
}

func contractMemory(ctx context.Context, s *Sc, req func(string, bool, string, ...any) bool) {
	p := &memProc{calls: map[string]int{}, ok: map[string]int{}, block: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.block) }) }
	defer unblock()
	m, err := queue.NewManager(p, &queue.Config{EngineType: engine.CHANNEL, Source: "contract-mem", QueueSize: 2, WorkerCount: 1,
		MaxRetries: 1, RetryDelay: 50 * time.Millisecond, MaxRetryDelay: 100 * time.Millisecond, LeaseDuration: 2 * time.Second, Timeout: 5 * time.Second})
	if !s.CheckErr("memory_new_manager", err) || !s.CheckErr("memory_start", m.Start()) {
		return
	}
	waitOK := func(id string) bool {
		for t := time.Now(); time.Since(t) < 5*time.Second; time.Sleep(10 * time.Millisecond) {
			if p.okCount(id) > 0 {
				return true
			}
		}
		return false
	}
	s.CheckErr("memory_add_block", m.AddDataWithID(ctx, "m-block", []byte("block")))
	s.CheckErr("memory_add_ok", m.AddDataWithID(ctx, "m-ok", []byte("ok")))
	err = m.AddDataWithID(ctx, "m-over", []byte("over"))
	req("memory_queue_size_full", errors.Is(err, common.ErrQueueFull), "third task with QueueSize=2: err=%v", err)
	unblock()
	req("memory_tasks_processed", waitOK("block") && waitOK("ok"), "block/ok not processed")
	s.CheckErr("memory_add_fatal", m.AddDataWithID(ctx, "m-fatal", []byte("fatal")))
	var lf []queue.FailedMessage
	for t := time.Now(); time.Since(t) < 5*time.Second && len(lf) == 0; time.Sleep(10 * time.Millisecond) {
		if lf, err = m.ListFailed(ctx, 10); !s.CheckErr("memory_list_failed", err) {
			return
		}
	}
	req("memory_failed_retained", len(lf) == 1 && lf[0].ID == "m-fatal" && string(lf[0].Data) == "fatal", "listed=%d", len(lf))
	req("memory_replay_ok", m.ReplayFailed(ctx, "m-fatal") == nil, "ReplayFailed")
	req("memory_replayed_processed", waitOK("fatal"), "replayed task not processed")
	lf, err = m.ListFailed(ctx, 10)
	req("memory_failed_empty_after_replay", err == nil && len(lf) == 0, "listed=%d err=%v", len(lf), err)
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req("memory_stop_ok", m.Stop(c) == nil, "Stop")
}
