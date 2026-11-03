package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"

	"taurus-readiness/ledger"
	"taurus-readiness/scanx"
)

// Sc 是一个场景的运行上下文：账本、期望、指标、检查。
type Sc struct {
	Name    string
	Env     *Env
	L       *ledger.Writer
	Exp     ledger.Expect
	Metrics map[string]any
	Extra   []string // 其它进程写的账本文件（子进程）
	Req     []string // 必须出现且通过的 check 名（写入 expect，由 Validate 强制）
	subTB   sync.Map // id -> 最近一次提交调用前的时刻（time.Time），用于完成观察的上界起点
}

func (e *Env) newSc(name string) (*Sc, error) {
	l, err := ledger.Create(e.path(name + ".ledger.jsonl"))
	if err != nil {
		return nil, err
	}
	return &Sc{Name: name, Env: e, L: l, Metrics: map[string]any{}}, nil
}

// Require 登记必须出现且通过的 check；缺失（例如对账被跳过）会被 Validate 判为违规
func (s *Sc) Require(names ...string) { s.Req = append(s.Req, names...) }

func (s *Sc) Event(name, detail string) {
	_ = s.L.Append(ledger.Rec{K: ledger.KEvent, Name: name, Detail: detail}) // 写错误由 sticky Err 在收尾时断言
}

// Check 记录一个断言结果（失败会使场景失败）
func (s *Sc) Check(name string, pass bool, f string, a ...any) bool {
	_ = s.L.Append(ledger.Rec{K: ledger.KCheck, Name: name, Pass: ledger.Bool(pass), Detail: fmt.Sprintf(f, a...)})
	if !pass {
		fmt.Printf("  [%s] CHECK FAILED %s: %s\n", s.Name, name, fmt.Sprintf(f, a...))
	}
	return pass
}

// CheckErr 记录一次操作/查询失败（绝不当作 0 值处理）。err==nil 时返回 true 且不写记录。
func (s *Sc) CheckErr(name string, err error) bool {
	if err == nil {
		return true
	}
	_ = s.L.Append(ledger.Rec{K: ledger.KCheck, Name: name, Err: err.Error()})
	fmt.Printf("  [%s] ERROR %s: %v\n", s.Name, name, err)
	return false
}

// Result 场景结果文件
type Result struct {
	Scenario   string             `json:"scenario"`
	Pass       bool               `json:"pass"`
	Violations []ledger.Violation `json:"violations"`
	Report     *ledger.Report     `json:"report"`
	Timing     *ledger.Timing     `json:"timing,omitempty"`
	Metrics    map[string]any     `json:"metrics"`
	Ledgers    []string           `json:"ledgers"`
	StartedAt  time.Time          `json:"started_at"`
	Seconds    float64            `json:"seconds"`
	Notes      []string           `json:"notes,omitempty"`
}

// Finish 收尾：关闭账本、从磁盘重新读取、校验、落盘期望与结果。返回是否通过。
func (s *Sc) Finish(started time.Time, notes ...string) bool {
	s.Check("ledger_write_ok", s.L.Err() == nil, "sticky writer error: %v", s.L.Err())
	s.Exp.RequiredChecks = dedupe(append(append([]string{"ledger_write_ok"}, s.Exp.RequiredChecks...), s.Req...))
	cerr := s.L.Close()
	s.Metrics["ledger_close_error"] = fmt.Sprint(cerr)
	files := append([]string{s.L.Path()}, s.Extra...)
	res := Result{Scenario: s.Name, StartedAt: started, Seconds: time.Since(started).Seconds(), Metrics: s.Metrics, Ledgers: files, Notes: notes}
	if err := writeJSON(s.Env.path(s.Name+".expect.json"), expectFile{Expect: s.Exp, Ledgers: files}); err != nil {
		fmt.Printf("  [%s] cannot write expect file: %v\n", s.Name, err)
		return false
	}
	recs, err := ledger.ReadFiles(files...)
	if err != nil {
		res.Violations = []ledger.Violation{{Code: "ledger_unreadable", Detail: err.Error()}}
		_ = writeJSON(s.Env.path(s.Name+".result.json"), res)
		fmt.Printf("  [%s] FAIL ledger unreadable: %v\n", s.Name, err)
		return false
	}
	rep := ledger.Validate(recs, s.Exp)
	if cerr != nil {
		rep.Violations = append(rep.Violations, ledger.Violation{Code: "ledger_close_error", Detail: cerr.Error()})
	}
	tm := ledger.ComputeTiming(recs)
	res.Report, res.Timing, res.Violations = rep, &tm, rep.Violations
	res.Pass = rep.Ok() && cerr == nil
	if err := writeJSON(s.Env.path(s.Name+".result.json"), res); err != nil {
		fmt.Printf("  [%s] cannot write result: %v\n", s.Name, err)
		return false
	}
	status := "PASS"
	if !res.Pass {
		status = "FAIL"
	}
	fmt.Printf("  [%s] %s  submitted=%d accepted=%d calls=%d unique=%d dup=%d confirmed=%d violations=%d (%.1fs)\n",
		s.Name, status, rep.UniqueSubmitted, rep.Accepted, rep.ProcCalls, rep.UniqueExecuted, rep.Duplicates, rep.Confirmed, len(rep.Violations), res.Seconds)
	if !res.Pass {
		fmt.Print(indent(rep.Summary(15)))
	}
	return res.Pass
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func indent(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("      " + ln + "\n")
	}
	return b.String()
}

type expectFile struct {
	Expect  ledger.Expect `json:"expect"`
	Ledgers []string      `json:"ledgers"`
}

// ---- Processor ----

// recProc 把每次调用追加到账本。Hold 非 nil 时在 proc_start 之后阻塞直到关闭或 ctx 取消。
type recProc struct {
	L    *ledger.Writer
	Work time.Duration
	Hold chan struct{}
}

func (p *recProc) Process(ctx context.Context, data []byte) error {
	id, ok := ledger.VerifyPayload(data)
	call := p.L.NextCall()
	_ = p.L.Append(ledger.Rec{K: ledger.KProcStart, ID: id, Sha: ledger.SHA256Hex(data), Len: len(data), Pid: os.Getpid(), Call: call, POK: ledger.Bool(ok)})
	end := func(ret string) {
		_ = p.L.Append(ledger.Rec{K: ledger.KProcEnd, ID: id, Pid: os.Getpid(), Call: call, Ret: ret})
	}
	if p.Hold != nil {
		select {
		case <-p.Hold:
		case <-ctx.Done():
			end("ctx: " + ctx.Err().Error())
			return ctx.Err()
		}
	}
	if p.Work > 0 {
		t := time.NewTimer(p.Work)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			end("ctx: " + ctx.Err().Error())
			return ctx.Err()
		}
	}
	end("nil")
	return nil
}

// ---- 提交 ----

type enqueueFn func(ctx context.Context, id string, data []byte) error

// classify 把提交错误分为“明确拒绝”和“不确定”。只有 Redis 服务端的 OOM 拒绝回复与
// ID 冲突属于明确拒绝（脚本在写入前被拒）；其余（超时、断连、未知）一律不确定。
func classify(err error) string {
	if err == nil {
		return ledger.ResAccepted
	}
	if errors.Is(err, common.ErrIDConflict) {
		return ledger.ResRejected
	}
	var re redis.Error
	if errors.As(err, &re) && strings.HasPrefix(re.Error(), "OOM ") {
		return ledger.ResRejected
	}
	return ledger.ResUncertain
}

type item struct {
	ID      string
	Payload []byte
	Sha     string
	Tag     string
}

func makeItems(prefix string, n, size int) ([]item, error) {
	out := make([]item, n)
	for i := range out {
		id := fmt.Sprintf("%s-%05d", prefix, i)
		p, err := ledger.MakePayload(id, size)
		if err != nil {
			return nil, err
		}
		out[i] = item{ID: id, Payload: p, Sha: ledger.SHA256Hex(p)}
	}
	return out, nil
}

// submit 追加一条 submit 记录（每次尝试一条，不覆盖）
func (s *Sc) submit(ctx context.Context, fn enqueueFn, it item, try int) string {
	tbT := time.Now()
	tb := tbT.UnixNano()
	s.subTB.Store(it.ID, tbT)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := fn(cctx, it.ID, it.Payload)
	cancel()
	ta := time.Now().UnixNano()
	res := classify(err)
	rec := ledger.Rec{K: ledger.KSubmit, ID: it.ID, Sha: it.Sha, Len: len(it.Payload), Res: res, TB: tb, TA: ta, Try: try, Tag: it.Tag}
	if err != nil {
		rec.Err = truncate(err.Error(), 300)
	}
	_ = s.L.Append(rec)
	return res
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// produce 以 conc 并发提交；rate>0 时限速（条/秒）。返回各结果计数。
func (s *Sc) produce(ctx context.Context, fn enqueueFn, items []item, conc int, rate float64, try int) map[string]int {
	var mu sync.Mutex
	counts := map[string]int{}
	ch := make(chan item)
	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				r := s.submit(ctx, fn, it, try)
				mu.Lock()
				counts[r]++
				mu.Unlock()
			}
		}()
	}
	var tick <-chan time.Time
	if rate > 0 {
		t := time.NewTicker(time.Duration(float64(time.Second) / rate))
		defer t.Stop()
		tick = t.C
	}
loop:
	for _, it := range items {
		if tick != nil {
			select {
			case <-tick:
			case <-ctx.Done():
				break loop
			}
		}
		select {
		case ch <- it:
		case <-ctx.Done():
			break loop
		}
	}
	close(ch)
	wg.Wait()
	return counts
}

// ---- Redis 状态（所有查询检查 error；失败不会被当成 0） ----

// qkeys 队列在 Redis 中的键（黑盒假设：与 pkg/queue/engine 文档一致；布局探针见 probeLayout）
type qkeys struct{ ready, delayed, leases, failed, mp, dp string }

func keysFor(name string) qkeys {
	p := "taurus:q2:{" + name + "}:"
	return qkeys{p + "ready", p + "delayed", p + "leases", p + "failed", p + "m:", p + "d:"}
}

// QState 队列在一个观察时刻的状态（各查询非原子，只能在静止边界严格比较）
type QState struct {
	StreamLen    int64 `json:"stream_len"`
	Pending      int64 `json:"pending"`
	Delayed      int64 `json:"delayed"`
	Leases       int64 `json:"leases"`
	Expired      int64 `json:"expired_leases"`
	Failed       int64 `json:"failed"`
	Records      int64 `json:"records_m"`
	Tombs        int64 `json:"tombstones_d"`
	GroupMissing bool  `json:"group_missing"`
}

func countKeys(ctx context.Context, r *redis.Client, pattern string) (int64, error) {
	n, err := scanx.CountUnique(func(cursor uint64) ([]string, uint64, error) {
		return r.Scan(ctx, cursor, pattern, 1000).Result()
	})
	if err != nil {
		return 0, fmt.Errorf("SCAN %s: %w", pattern, err)
	}
	return n, nil
}

func queueState(ctx context.Context, r *redis.Client, name string, allowNoGroup bool) (QState, error) {
	k := keysFor(name)
	var q QState
	var err error
	if q.StreamLen, err = r.XLen(ctx, k.ready).Result(); err != nil {
		return q, fmt.Errorf("XLEN: %w", err)
	}
	pend, err := r.XPending(ctx, k.ready, "workers").Result()
	switch {
	case err == nil:
		q.Pending = pend.Count
	case strings.Contains(err.Error(), "NOGROUP") && allowNoGroup:
		q.GroupMissing = true
	default:
		return q, fmt.Errorf("XPENDING: %w", err)
	}
	for _, c := range []struct {
		key string
		dst *int64
	}{{k.delayed, &q.Delayed}, {k.leases, &q.Leases}, {k.failed, &q.Failed}} {
		if *c.dst, err = r.ZCard(ctx, c.key).Result(); err != nil {
			return q, fmt.Errorf("ZCARD %s: %w", c.key, err)
		}
	}
	t, err := r.Time(ctx).Result()
	if err != nil {
		return q, fmt.Errorf("TIME: %w", err)
	}
	if q.Expired, err = r.ZCount(ctx, k.leases, "-inf", fmt.Sprint(t.UnixMilli())).Result(); err != nil {
		return q, fmt.Errorf("ZCOUNT: %w", err)
	}
	if q.Records, err = countKeys(ctx, r, k.mp+"*"); err != nil {
		return q, err
	}
	if q.Tombs, err = countKeys(ctx, r, k.dp+"*"); err != nil {
		return q, err
	}
	return q, nil
}

// idStatus 单个ID的最终状态判定
func idStatus(rec string, recExists int64, tomb string, tombErr error, sha string, failedState string) (string, error) {
	_ = rec
	if tombErr != nil && !errors.Is(tombErr, redis.Nil) {
		return "", tombErr
	}
	switch {
	case recExists == 0 && tombErr == nil && tomb == sha:
		return ledger.StateDone, nil
	case recExists == 0 && tombErr == nil:
		return ledger.StateDigestMismatch, nil
	case recExists == 1 && failedState == "failed":
		return ledger.StateFailed, nil
	}
	return "", nil // 尚未到终态
}

// waitFinal 轮询直到 want 中每个ID到达终态（done；failedOK 中的ID也接受 failed），
// 并为每个ID追加一条 final 记录，UB 为“自该ID提交前或上一次成功轮询起”的间隔：真实完成时刻 ∈ (T-UB, T]。
// 任何查询错误立即返回（不重试、不当 0）；超时返回 errTimeout 及未到终态的ID。
func (s *Sc) waitFinal(ctx context.Context, r *redis.Client, name string, want map[string]string, failedOK map[string]bool, within time.Duration) (pending []string, err error) {
	k := keysFor(name)
	left := map[string]string{}
	for id, sha := range want {
		left[id] = sha
	}
	prev := map[string]time.Time{}
	start := time.Now()
	for id := range left {
		prev[id] = start
		if v, ok := s.subTB.Load(id); ok { // 完成不可能早于提交：上界从提交前或上次成功轮询起算
			prev[id] = v.(time.Time)
		}
	}
	deadline := start.Add(within)
	for len(left) > 0 {
		if time.Now().After(deadline) {
			break
		}
		ids := make([]string, 0, len(left))
		for id := range left {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for lo := 0; lo < len(ids); lo += 250 {
			hi := min(lo+250, len(ids))
			batch := ids[lo:hi]
			pollAt := time.Now()
			pipe := r.Pipeline()
			ex := make([]*redis.IntCmd, len(batch))
			gt := make([]*redis.StringCmd, len(batch))
			st := make([]*redis.StringCmd, len(batch))
			for i, id := range batch {
				ex[i] = pipe.Exists(ctx, k.mp+id)
				gt[i] = pipe.Get(ctx, k.dp+id)
				st[i] = pipe.HGet(ctx, k.mp+id, "s")
			}
			if _, perr := pipe.Exec(ctx); perr != nil && !errors.Is(perr, redis.Nil) {
				return nil, fmt.Errorf("completion poll pipeline: %w", perr)
			}
			now := time.Now()
			for i, id := range batch {
				if ex[i].Err() != nil {
					return nil, fmt.Errorf("EXISTS m:%s: %w", id, ex[i].Err())
				}
				fs := ""
				if serr := st[i].Err(); serr == nil {
					fs = st[i].Val()
				} else if !errors.Is(serr, redis.Nil) {
					return nil, fmt.Errorf("HGET m:%s s: %w", id, serr)
				}
				state, serr := idStatus("", ex[i].Val(), gt[i].Val(), gt[i].Err(), left[id], fs)
				if serr != nil {
					return nil, fmt.Errorf("GET d:%s: %w", id, serr)
				}
				if state == ledger.StateFailed && !failedOK[id] {
					state = "" // 不在预期内的失败不算终态：继续等（也许是重试窗口）；超时后会报告
				}
				if state != "" {
					if aerr := s.L.Append(ledger.Rec{K: ledger.KFinal, ID: id, State: state, UB: now.Sub(prev[id]).Nanoseconds()}); aerr != nil {
						return nil, aerr
					}
					delete(left, id)
					continue
				}
				prev[id] = pollAt
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	for id := range left {
		pending = append(pending, id)
	}
	sort.Strings(pending)
	if len(pending) > 0 {
		return pending, errTimeout
	}
	return nil, nil
}

func wantMap(items []item) map[string]string {
	m := make(map[string]string, len(items))
	for _, it := range items {
		m[it.ID] = it.Sha
	}
	return m
}

// reconcileQuiescent 在 Stop 之后（静止边界）严格核对：
//   - 每个 done 的ID：任务记录 m:<id> 消失，墓碑 d:<id> 存在且摘要等于账本摘要，PTTL 在 (0, window]
//   - 队列级：Stream/PEL/delayed/leases 为 0，failed/records/tombs 等于期望
func (s *Sc) reconcileQuiescent(ctx context.Context, r *redis.Client, name string, done map[string]string, window time.Duration, expect QState) QState {
	k := keysFor(name)
	s.Require("stream_len", "pending", "delayed", "leases", "failed", "records", "tombstones", "per_id_reconcile_all")
	q, err := queueState(ctx, r, name, false)
	if !s.CheckErr("queue_state_after_stop", err) {
		return q
	}
	s.Metrics["state_after_stop"] = q
	s.Check("stream_len", q.StreamLen == expect.StreamLen, "got %d want %d", q.StreamLen, expect.StreamLen)
	s.Check("pending", q.Pending == expect.Pending, "got %d want %d", q.Pending, expect.Pending)
	s.Check("delayed", q.Delayed == expect.Delayed, "got %d want %d", q.Delayed, expect.Delayed)
	s.Check("leases", q.Leases == expect.Leases, "got %d want %d", q.Leases, expect.Leases)
	s.Check("failed", q.Failed == expect.Failed, "got %d want %d", q.Failed, expect.Failed)
	s.Check("records", q.Records == expect.Records, "got %d want %d", q.Records, expect.Records)
	s.Check("tombstones", q.Tombs == expect.Tombs, "got %d want %d", q.Tombs, expect.Tombs)
	var bad atomic.Int64
	ids := make([]string, 0, len(done))
	for id := range done {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for lo := 0; lo < len(ids); lo += 250 {
		batch := ids[lo:min(lo+250, len(ids))]
		pipe := r.Pipeline()
		ex := make([]*redis.IntCmd, len(batch))
		gt := make([]*redis.StringCmd, len(batch))
		tt := make([]*redis.DurationCmd, len(batch))
		for i, id := range batch {
			ex[i] = pipe.Exists(ctx, k.mp+id)
			gt[i] = pipe.Get(ctx, k.dp+id)
			tt[i] = pipe.PTTL(ctx, k.dp+id)
		}
		if _, perr := pipe.Exec(ctx); perr != nil && !errors.Is(perr, redis.Nil) {
			s.CheckErr("per_id_reconcile", perr)
			return q
		}
		for i, id := range batch {
			if ex[i].Err() != nil || (gt[i].Err() != nil) || tt[i].Err() != nil {
				s.CheckErr("per_id_reconcile", fmt.Errorf("id %s: exists=%v get=%v pttl=%v", id, ex[i].Err(), gt[i].Err(), tt[i].Err()))
				bad.Add(1)
				continue
			}
			if ex[i].Val() != 0 || gt[i].Val() != done[id] || tt[i].Val() <= 0 || tt[i].Val() > window {
				bad.Add(1)
				s.Check("per_id_reconcile", false, "id %s: record_exists=%d tomb_match=%v pttl=%v", id, ex[i].Val(), gt[i].Val() == done[id], tt[i].Val())
			}
		}
	}
	s.Check("per_id_reconcile_all", bad.Load() == 0, "%d of %d ids failed per-id reconcile", bad.Load(), len(ids))
	return q
}

// probeLayout 布局探针：确认黑盒键名假设成立（否则所有状态查询会误报为 0）
func (s *Sc) probeLayout(ctx context.Context, r *redis.Client, name, id string) {
	k := keysFor(name)
	n, err := r.Exists(ctx, k.mp+id).Result()
	if s.CheckErr("layout_probe", err) {
		s.Check("layout_probe", n == 1, "record key %s%s not found right after accepted enqueue (key layout assumption broken)", k.mp, id)
	}
}

func ensureDir(p string) error { return os.MkdirAll(filepath.Clean(p), 0o700) }
