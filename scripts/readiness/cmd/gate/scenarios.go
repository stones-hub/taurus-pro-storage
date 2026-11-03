package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"

	"taurus-readiness/ledger"
)

const dedupWindow = 24 * time.Hour

func mgrCfg(name string, rc *redisx.RedisClient, workers int, lease time.Duration) *queue.Config {
	return &queue.Config{
		EngineType: engine.REDIS, Source: name, RedisClient: rc,
		WorkerCount: workers, MaxRetries: 3, RetryDelay: 200 * time.Millisecond, MaxRetryDelay: time.Second,
		LeaseDuration: lease, DedupWindow: dedupWindow, Timeout: 15 * time.Second,
	}
}

func qname(env *Env, scn string) string { return env.RunID + "-" + scn }

// stopMgr 在静止边界停止 Manager；Stop 返回错误即场景检查失败（不把 Stop 成功当作已确认）
func (s *Sc) stopMgr(m *queue.Manager) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.CheckErr("manager_stop", m.Stop(ctx))
}

func (s *Sc) usedMemory(ctx context.Context, r *redis.Client, label string) {
	info, err := r.Info(ctx, "memory").Result()
	if !s.CheckErr("info_memory_"+label, err) {
		return
	}
	v, err := infoInt(info, "used_memory")
	if s.CheckErr("info_memory_parse_"+label, err) {
		s.Metrics["used_memory_"+label] = v
	}
}

func mgrEnqueue(m *queue.Manager) enqueueFn {
	return func(ctx context.Context, id string, data []byte) error { return m.AddDataWithID(ctx, id, data) }
}

func engEnqueue(e engine.ReliableEngine) enqueueFn {
	return func(ctx context.Context, id string, data []byte) error {
		_, err := e.Enqueue(ctx, id, data, true)
		return err
	}
}

func countStr(m map[string]int) string {
	return fmt.Sprintf("accepted=%d rejected=%d uncertain=%d", m[ledger.ResAccepted], m[ledger.ResRejected], m[ledger.ResUncertain])
}

// ---------------------------------------------------------------------------
// 正常矩阵 / 短稳态

type normalOpts struct {
	Tag    string
	Size   int
	Work   time.Duration
	N      int
	Rate   float64
	Within time.Duration
	Total  time.Duration // >0：整个场景（含确认、停止、对账）的硬截止
}

func scNormal(ctx context.Context, env *Env, in *Inst, o normalOpts) bool {
	started := time.Now()
	if o.Total > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Total)
		defer cancel()
	}
	s, err := env.newSc(o.Tag)
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	q := qname(env, o.Tag)
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	rr := rawClient(in.Addr)
	defer rr.Close()
	items, err := makeItems("t", o.N, o.Size)
	if !s.CheckErr("make_items", err) {
		return s.Finish(started)
	}
	s.Exp = ledger.Expect{} // 正常运行：不允许重复
	s.Require("all_submits_accepted")
	s.Metrics["size"], s.Metrics["work_ms"], s.Metrics["n"], s.Metrics["rate_per_s"] = o.Size, o.Work.Milliseconds(), o.N, o.Rate

	m, err := queue.NewManager(&recProc{L: s.L, Work: o.Work}, mgrCfg(q, rc, 8, 30*time.Second))
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	s.usedMemory(ctx, rr, "before")
	counts := s.produce(ctx, mgrEnqueue(m), items, 8, o.Rate, 1)
	s.Metrics["submit_results"] = counts
	s.Check("all_submits_accepted", counts[ledger.ResAccepted] == o.N, "%s", countStr(counts))

	pending, werr := s.waitFinal(ctx, rr, q, wantMap(items), nil, o.Within)
	if werr != nil {
		s.Check("all_completed_within_deadline", false, "%v; %d not completed (first: %v)", werr, len(pending), firstN(pending, 3))
		s.CheckErr("wait_final", errIfNotTimeout(werr))
	}
	s.usedMemory(ctx, rr, "after_completion")
	s.stopMgr(m)
	s.reconcileQuiescent(ctx, rr, q, wantMap(items), dedupWindow, QState{Tombs: int64(o.N)})
	if o.Total > 0 {
		el := time.Since(started)
		s.Metrics["scenario_total_s"] = el.Seconds()
		s.Require("scenario_total_deadline")
		s.Check("scenario_total_deadline", el <= o.Total, "scenario took %v, hard limit %v (submit+confirm+stop+reconcile)", el.Round(time.Millisecond), o.Total)
	}
	notes := []string{"completion observed by polling (50ms) after submission ends; not the Ack instant; the observation upper bound (UB) is measured from the submit instant, so submit_to_final_observed is only meaningful when the whole load is submitted quickly"}
	if o.Rate > 0 {
		notes = append(notes, "rate-limited run: polling starts after the last submission, so submit_to_final_observed and its UB are NOT completion latency; use submit_to_process_* and throughput_per_s")
	}
	return s.Finish(started, notes...)
}

func errIfNotTimeout(err error) error {
	if errors.Is(err, errTimeout) {
		return nil
	}
	return err
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// ---------------------------------------------------------------------------
// 积压

func scBacklog(ctx context.Context, env *Env, in *Inst, n int) bool {
	started := time.Now()
	s, err := env.newSc("backlog")
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	q := qname(env, "backlog")
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	rr := rawClient(in.Addr)
	defer rr.Close()
	items, err := makeItems("b", n, 1024)
	if !s.CheckErr("make_items", err) {
		return s.Finish(started)
	}
	eng, err := engine.NewRedisStreamEngine(ctx, rc, engine.ReliableOptions{Name: q, LeaseDuration: 30 * time.Second, DedupWindow: dedupWindow})
	if !s.CheckErr("new_engine", err) {
		return s.Finish(started)
	}
	counts := s.produce(ctx, engEnqueue(eng), items, 8, 0, 1)
	s.Metrics["preload_results"] = counts
	s.Require("preload_all_accepted", "layout_probe", "backlog_stream_len", "backlog_records", "backlog_no_consumption")
	s.Check("preload_all_accepted", counts[ledger.ResAccepted] == n, "%s", countStr(counts))
	s.probeLayout(ctx, rr, q, items[0].ID)
	pre, err := queueState(ctx, rr, q, true)
	if s.CheckErr("state_before_start", err) {
		s.Metrics["state_before_start"] = pre
		s.Check("backlog_stream_len", pre.StreamLen == int64(n), "got %d want %d", pre.StreamLen, n)
		s.Check("backlog_records", pre.Records == int64(n), "got %d want %d", pre.Records, n)
		s.Check("backlog_no_consumption", pre.Tombs == 0 && pre.Pending == 0, "tombs=%d pending=%d", pre.Tombs, pre.Pending)
	}
	s.Exp = ledger.Expect{}
	m, err := queue.NewManager(&recProc{L: s.L, Work: 5 * time.Millisecond}, mgrCfg(q, rc, 8, 30*time.Second))
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	pending, werr := s.waitFinal(ctx, rr, q, wantMap(items), nil, 15*time.Second)
	if werr != nil {
		s.Check("backlog_drained_within_deadline", false, "%v; %d not completed", werr, len(pending))
		s.CheckErr("wait_final", errIfNotTimeout(werr))
	}
	s.stopMgr(m)
	s.reconcileQuiescent(ctx, rr, q, wantMap(items), dedupWindow, QState{Tombs: int64(n)})
	return s.Finish(started)
}

// ---------------------------------------------------------------------------
// 种子：用公开引擎 API 构造 ready/delayed/inflight/failed/done 五种状态

type seedPlan struct {
	Prefix                                 string
	Done, Delayed, Failed, Inflight, Ready int
	DelayFor                               time.Duration
	Size                                   int
}

type seedRoles struct {
	Done, Delayed, Failed, Inflight, Ready []item
}

func (r seedRoles) all() []item {
	var out []item
	for _, g := range [][]item{r.Done, r.Delayed, r.Failed, r.Inflight, r.Ready} {
		out = append(out, g...)
	}
	return out
}

func (s *Sc) seed(ctx context.Context, eng engine.ReliableEngine, p seedPlan) (seedRoles, error) {
	var roles seedRoles
	mk := func(kind string, n int) ([]item, error) {
		its, err := makeItems(p.Prefix+"-"+kind, n, p.Size)
		return its, err
	}
	var err error
	if roles.Done, err = mk("done", p.Done); err != nil {
		return roles, err
	}
	if roles.Delayed, err = mk("delayed", p.Delayed); err != nil {
		return roles, err
	}
	if roles.Failed, err = mk("failed", p.Failed); err != nil {
		return roles, err
	}
	if roles.Inflight, err = mk("inflight", p.Inflight); err != nil {
		return roles, err
	}
	if roles.Ready, err = mk("ready", p.Ready); err != nil {
		return roles, err
	}
	enq := engEnqueue(eng)
	taken := append(append(append(append([]item{}, roles.Done...), roles.Delayed...), roles.Failed...), roles.Inflight...)
	for _, it := range taken {
		if r := s.submit(ctx, enq, it, 1); r != ledger.ResAccepted {
			return roles, fmt.Errorf("seed enqueue %s: %s", it.ID, r)
		}
	}
	role := map[string]string{}
	for _, it := range roles.Done {
		role[it.ID] = "done"
	}
	for _, it := range roles.Delayed {
		role[it.ID] = "delayed"
	}
	for _, it := range roles.Failed {
		role[it.ID] = "failed"
	}
	for _, it := range roles.Inflight {
		role[it.ID] = "inflight"
	}
	for i := range taken {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		d, err := eng.Receive(cctx)
		cancel()
		if err != nil {
			return roles, fmt.Errorf("seed receive #%d: %w", i, err)
		}
		if d.ID != taken[i].ID {
			return roles, fmt.Errorf("seed receive order: got %s want %s", d.ID, taken[i].ID)
		}
		cctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		switch role[d.ID] {
		case "done":
			err = eng.Ack(cctx, d)
		case "delayed":
			err = eng.Retry(cctx, d, p.DelayFor, "seed-delayed")
		case "failed":
			err = eng.Fail(cctx, d, "seed-failed")
		case "inflight":
			// 保持领取不确认
		}
		cancel()
		if err != nil {
			return roles, fmt.Errorf("seed settle %s (%s): %w", d.ID, role[d.ID], err)
		}
	}
	for _, it := range roles.Ready {
		if r := s.submit(ctx, enq, it, 1); r != ledger.ResAccepted {
			return roles, fmt.Errorf("seed enqueue %s: %s", it.ID, r)
		}
	}
	return roles, nil
}

// seedState 种子完成后的预期队列状态
func (r seedRoles) seedState() QState {
	return QState{
		StreamLen: int64(len(r.Inflight) + len(r.Ready)), Pending: int64(len(r.Inflight)),
		Delayed: int64(len(r.Delayed)), Leases: int64(len(r.Inflight)), Failed: int64(len(r.Failed)),
		Records: int64(len(r.Delayed) + len(r.Failed) + len(r.Inflight) + len(r.Ready)), Tombs: int64(len(r.Done)),
	}
}

// ---------------------------------------------------------------------------
// 选定字段快照（不是 Redis 全部内部状态）

type snap map[string]string

func takeSnap(ctx context.Context, r *redis.Client, name string) (snap, error) {
	k := keysFor(name)
	out := snap{}
	scanKeys := func(pattern string, f func(key string) error) error {
		var cur uint64
		for {
			keys, next, err := r.Scan(ctx, cur, pattern, 1000).Result()
			if err != nil {
				return fmt.Errorf("SCAN %s: %w", pattern, err)
			}
			for _, key := range keys {
				if err := f(key); err != nil {
					return err
				}
			}
			cur = next
			if cur == 0 {
				return nil
			}
		}
	}
	if err := scanKeys(k.mp+"*", func(key string) error {
		h, err := r.HGetAll(ctx, key).Result()
		if err != nil {
			return fmt.Errorf("HGETALL %s: %w", key, err)
		}
		fs := make([]string, 0, len(h))
		for f, v := range h {
			fs = append(fs, f+"="+v)
		}
		sort.Strings(fs)
		out["hash "+key] = strings.Join(fs, "|")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanKeys(k.dp+"*", func(key string) error {
		v, err := r.Get(ctx, key).Result()
		if err != nil {
			return fmt.Errorf("GET %s: %w", key, err)
		}
		out["tomb "+key] = v // PTTL 随时间变化，单独检查
		return nil
	}); err != nil {
		return nil, err
	}
	for _, zk := range []string{k.delayed, k.leases, k.failed} {
		zs, err := r.ZRangeWithScores(ctx, zk, 0, -1).Result()
		if err != nil {
			return nil, fmt.Errorf("ZRANGE %s: %w", zk, err)
		}
		var b strings.Builder
		for _, z := range zs {
			fmt.Fprintf(&b, "%v@%.0f;", z.Member, z.Score)
		}
		out["zset "+zk] = b.String()
	}
	xs, err := r.XRange(ctx, k.ready, "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("XRANGE: %w", err)
	}
	var b strings.Builder
	for _, x := range xs {
		fmt.Fprintf(&b, "%s=%v;", x.ID, x.Values)
	}
	out["stream entries"] = b.String()
	pe, err := r.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: k.ready, Group: "workers", Start: "-", End: "+", Count: 100000}).Result()
	if err != nil && !strings.Contains(err.Error(), "NOGROUP") {
		return nil, fmt.Errorf("XPENDING EXT: %w", err)
	}
	b.Reset()
	for _, p := range pe { // 空闲时间随时间变化，不比较
		fmt.Fprintf(&b, "%s/%s/%d;", p.ID, p.Consumer, p.RetryCount)
	}
	out["pel"] = b.String()
	gs, err := r.XInfoGroups(ctx, k.ready).Result()
	if err != nil && !strings.Contains(err.Error(), "ERR no such key") {
		return nil, fmt.Errorf("XINFO GROUPS: %w", err)
	}
	b.Reset()
	for _, g := range gs {
		fmt.Fprintf(&b, "%s/%s/%d;", g.Name, g.LastDeliveredID, g.Pending)
	}
	out["groups"] = b.String()
	return out, nil
}

func diffSnap(a, b snap) []string {
	var d []string
	for k, v := range a {
		if bv, ok := b[k]; !ok {
			d = append(d, "missing after: "+k)
		} else if bv != v {
			d = append(d, "changed: "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			d = append(d, "extra after: "+k)
		}
	}
	sort.Strings(d)
	return d
}

func tombPTTL(ctx context.Context, r *redis.Client, name, id string) (time.Duration, error) {
	d, err := r.PTTL(ctx, keysFor(name).dp+id).Result()
	if err != nil {
		return 0, err
	}
	return d, nil
}

var _ = common.ErrIDConflict
