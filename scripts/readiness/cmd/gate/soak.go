package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"

	"taurus-readiness/ledger"
)

// ---------------------------------------------------------------------------
// 有界长稳态（soak）：显式可选，不属于默认门禁。
//
// 固定上限：≤10 条/秒、≤18000 次提交调用（含探针）、载荷 256B–4KiB、Worker 8、GOMAXPROCS 由脚本设为 2；
// 独立私有 Redis（1CPU/384MiB，maxmemory 128mb noeviction，AOF always，固定 8.10.2 摘要，127.0.0.1 随机端口）。
// 生产窗口 = Dur（正式 30 分钟）；之后有界收尾：排空（≤3 分钟）+ 等待一个短 DedupWindow 让墓碑到期 + 停止对账。
// 期间按固定间隔连续采样内存/队列状态，越过安全阈值（低于资源上限）立即失败并停止生产。
// 内存趋势只是 30 分钟内的观察，不是“永不泄漏”的证明。

const (
	soakFullSeconds = 1800
	soakRate        = 10.0
	soakMaxSubmits  = 18000
	soakWorkers     = 8
	soakDrainMax    = 3 * time.Minute
)

type soakLimits struct {
	RedisUsedBytes   int64  `json:"redis_used_memory_bytes"` // maxmemory 128mb 之下
	ContainerBytes   int64  `json:"container_memory_bytes"`  // 容器 384MiB 之下
	GateSysBytes     uint64 `json:"gate_go_sys_bytes"`       // 门禁进程 Go 运行时向 OS 申请的内存
	MinFreeDiskBytes uint64 `json:"min_free_disk_bytes"`     // 证据目录所在文件系统
	MaxOutstanding   int    `json:"max_outstanding_tasks"`   // 已接受但未到终态（积压失控）
	MaxFailed        int64  `json:"max_failed_retained"`     // 接近 ListFailed 单次上限 1000 之前停止
	MaxLedgerBytes   int64  `json:"max_ledger_bytes"`        // 账本文件
	MaxSampleGap     int64  `json:"max_sample_gap_ms"`       // 两次采样最大间隔（采样本身卡住）
}

func defaultSoakLimits() soakLimits {
	return soakLimits{
		RedisUsedBytes: 96 << 20, ContainerBytes: 320 << 20, GateSysBytes: 768 << 20, MinFreeDiskBytes: 1 << 30,
		MaxOutstanding: 2000, MaxFailed: 900, MaxLedgerBytes: 256 << 20, MaxSampleGap: 30_000,
	}
}

type soakOpts struct {
	Dur      time.Duration // 生产窗口
	Window   time.Duration // DedupWindow（短，用于墓碑到期检查）
	Sample   time.Duration
	Limits   soakLimits
	Name     string // 结果名：soak / soak-smoke / soak-limit-test
	MaxSubs  int
	Rate     float64
	Workers  int
	Lease    time.Duration
	Poll     time.Duration // 完成观察轮询间隔
	Replay   time.Duration // 失败留存巡检/重放间隔
	DrainMax time.Duration
}

// soakSample 一次采样（各查询非原子；只用于趋势与阈值，不做严格对账）
type soakSample struct {
	Seq          int      `json:"seq"`
	T            string   `json:"t"`
	ElapsedS     float64  `json:"elapsed_s"`
	GapMs        int64    `json:"gap_ms"`
	Phase        string   `json:"phase"`
	Submits      int64    `json:"submit_calls"`
	Accepted     int64    `json:"accepted"`
	NotAccepted  int64    `json:"not_accepted"`
	Calls        int64    `json:"processor_calls"`
	DoneObs      int64    `json:"done_observed"`
	FailedObs    int64    `json:"failed_observed"`
	Outstanding  int      `json:"outstanding"`
	Replayed     int64    `json:"replayed"`
	RedisUsed    int64    `json:"redis_used_memory"`
	RedisRSS     int64    `json:"redis_used_memory_rss"`
	RedisPeak    int64    `json:"redis_used_memory_peak"`
	AOFStatus    string   `json:"aof_last_write_status"`
	DBSize       int64    `json:"dbsize"`
	Q            QState   `json:"queue"`
	ContainerMem int64    `json:"container_memory_current"`
	GoHeap       uint64   `json:"gate_heap_alloc"`
	GoSys        uint64   `json:"gate_sys"`
	Goroutines   int      `json:"gate_goroutines"`
	LedgerBytes  int64    `json:"ledger_bytes"`
	FreeDisk     uint64   `json:"free_disk_bytes"`
	Breaches     []string `json:"breaches,omitempty"`
}

// soakBreaches 纯函数：返回越过的安全阈值（空表示安全）
func soakBreaches(x soakSample, l soakLimits) []string {
	var b []string
	if x.RedisUsed >= l.RedisUsedBytes {
		b = append(b, fmt.Sprintf("redis used_memory %d >= %d", x.RedisUsed, l.RedisUsedBytes))
	}
	if x.ContainerMem >= l.ContainerBytes {
		b = append(b, fmt.Sprintf("container memory %d >= %d", x.ContainerMem, l.ContainerBytes))
	}
	if x.GoSys >= l.GateSysBytes {
		b = append(b, fmt.Sprintf("gate Go sys %d >= %d", x.GoSys, l.GateSysBytes))
	}
	if x.FreeDisk < l.MinFreeDiskBytes {
		b = append(b, fmt.Sprintf("free disk %d < %d", x.FreeDisk, l.MinFreeDiskBytes))
	}
	if x.Outstanding > l.MaxOutstanding {
		b = append(b, fmt.Sprintf("outstanding %d > %d", x.Outstanding, l.MaxOutstanding))
	}
	if x.Q.Failed > l.MaxFailed {
		b = append(b, fmt.Sprintf("failed retained %d > %d", x.Q.Failed, l.MaxFailed))
	}
	if x.LedgerBytes > l.MaxLedgerBytes {
		b = append(b, fmt.Sprintf("ledger %d > %d bytes", x.LedgerBytes, l.MaxLedgerBytes))
	}
	if x.GapMs > l.MaxSampleGap {
		b = append(b, fmt.Sprintf("sample gap %dms > %dms", x.GapMs, l.MaxSampleGap))
	}
	if x.AOFStatus != "" && x.AOFStatus != "ok" {
		b = append(b, "aof_last_write_status="+x.AOFStatus)
	}
	return b
}

// soakClass 按任务序号决定脚本行为（互不重叠）
func soakClass(i int) string {
	switch {
	case i%100 == 13:
		return "replay" // 第 1 次普通错误 -> 失败留存 -> 巡检重放 -> 第 2 次成功
	case i%100 == 57:
		return "fatal" // 普通错误，永久失败留存（不重放）
	case i%500 == 100:
		return "dupprobe" // 完成后窗口内：同内容重复（不执行）+ 不同内容冲突
	case i%500 == 250:
		return "expire" // 完成后墓碑到期，再次提交被当作新任务执行
	case i%50 == 21:
		return "panic1" // 第 1 次 panic，重试后成功
	case i%20 == 7:
		return "retry1" // 第 1 次 RetryableError，重试后成功
	}
	return "ok"
}

func soakID(i int) string { return fmt.Sprintf("s-%05d", i) }

func soakIndex(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "s-"))
	if err != nil || !strings.HasPrefix(id, "s-") {
		return -1
	}
	return n
}

func soakSize(i int) int { return 256 + (i*37)%3841 } // 256..4096

func soakItem(i int) (item, error) {
	id := soakID(i)
	p, err := ledger.MakePayload(id, soakSize(i))
	if err != nil {
		return item{}, err
	}
	return item{ID: id, Payload: p, Sha: ledger.SHA256Hex(p)}, nil
}

// soakProbe 计划在 Due 之后执行的探针提交
type soakProbe struct {
	Due  time.Time
	Kind string // dup-same | dup-conflict | expire-resubmit
	It   item
}

type doneObs struct {
	T  time.Time
	UB time.Duration
}

type soakState struct {
	mu          sync.Mutex
	outstanding map[string]string    // id -> sha（已接受、未到终态）
	prev        map[string]time.Time // id -> UB 起点
	probes      []soakProbe
	done        map[string]doneObs // 最近一次 done 观察
	replayed    map[string]bool
	expireResub map[string]bool
	accepted    map[string]bool
	fatalIDs    map[string]item

	submits, acceptedN, notAccepted, doneN, failedN, replayedN atomic.Int64
	conflictOK, conflictBad, dupBad, expireProbeBad            atomic.Int64
	unexpectedFailed, fatalBad                                 atomic.Int64
	firstBad                                                   atomic.Value // string
}

func (st *soakState) bad(detail string) { st.firstBad.CompareAndSwap(nil, detail) }

func scSoak(ctx context.Context, env *Env, o soakOpts) bool {
	started := time.Now()
	s, err := env.newSc(o.Name)
	if err != nil {
		fmt.Println("  cannot create scenario:", err)
		return false
	}
	// 场景硬截止：生产 + 排空 + 墓碑到期等待 + 余量
	hard := o.Dur + o.DrainMax + o.Window + 4*time.Minute
	ctx, cancelHard := context.WithTimeout(ctx, hard)
	defer cancelHard()
	s.Metrics["limits"], s.Metrics["planned_production_s"], s.Metrics["dedup_window_s"], s.Metrics["hard_limit_s"] = o.Limits, o.Dur.Seconds(), o.Window.Seconds(), hard.Seconds()
	s.Metrics["rate_per_s"], s.Metrics["max_submit_calls"], s.Metrics["workers"], s.Metrics["gomaxprocs"] = o.Rate, o.MaxSubs, o.Workers, runtime.GOMAXPROCS(0)
	s.Require("soak_gomaxprocs_le_2", "soak_redis_config", "soak_container_memory_limit", "soak_production_window_reached", "soak_submit_cap_respected",
		"soak_all_accepted_reached_final", "soak_conflict_probes", "soak_dup_probes", "soak_expire_probes", "soak_failed_list_matches_script",
		"soak_per_id_tombstone_tristate", "soak_final_failed_listed", "soak_stop_ok", "soak_final_quiescent_after_window", "soak_samples_continuous",
		"soak_safety_thresholds", "soak_no_query_errors")
	s.Check("soak_gomaxprocs_le_2", runtime.GOMAXPROCS(0) <= 2, "GOMAXPROCS=%d", runtime.GOMAXPROCS(0))

	in, err := env.newRedis(ctx, redisOpts{Tag: "soak", AppendOnly: true, AofAlways: true})
	if !s.CheckErr("redis_start", err) {
		return s.Finish(started)
	}
	rr := rawClient(in.Addr)
	defer rr.Close()
	rc, err := queueClient(in.Addr)
	if !s.CheckErr("queue_client", err) {
		return s.Finish(started)
	}
	defer rc.Close()
	// 资源配置核验（不只信启动参数）
	cfgOK, cfgDetail := true, []string{}
	for _, kv := range [][2]string{{"maxmemory", "134217728"}, {"maxmemory-policy", "noeviction"}, {"appendonly", "yes"}, {"appendfsync", "always"}} {
		res, err := rr.ConfigGet(ctx, kv[0]).Result()
		if !s.CheckErr("config_get_"+kv[0], err) {
			return s.Finish(started)
		}
		cfgOK = cfgOK && res[kv[0]] == kv[1]
		cfgDetail = append(cfgDetail, kv[0]+"="+res[kv[0]])
	}
	s.Check("soak_redis_config", cfgOK, "%s", strings.Join(cfgDetail, " "))
	if out, err := env.D.Exec(ctx, in.C, "cat", "/sys/fs/cgroup/memory.max"); s.CheckErr("container_memory_max", err) {
		s.Check("soak_container_memory_limit", strings.TrimSpace(out) == strconv.Itoa(384<<20), "memory.max=%q", strings.TrimSpace(out))
	}

	q := qname(env, "soak")
	k := keysFor(q)
	st := &soakState{outstanding: map[string]string{}, prev: map[string]time.Time{}, done: map[string]doneObs{}, replayed: map[string]bool{},
		expireResub: map[string]bool{}, accepted: map[string]bool{}, fatalIDs: map[string]item{}}
	var calls atomic.Int64
	proc := &scriptProc{L: s.L,
		Work: func(id string) time.Duration {
			calls.Add(1)
			return time.Duration(2+soakIndex(id)%9) * time.Millisecond
		},
		Decide: func(id string, n int) action {
			switch soakClass(soakIndex(id)) {
			case "replay":
				if n == 1 {
					return actFatal
				}
			case "fatal":
				return actFatal
			case "panic1":
				if n == 1 {
					return actPanic
				}
			case "retry1":
				if n == 1 {
					return actRetryable
				}
			}
			return actOK
		}}
	cfg := mgrCfg(q, rc, o.Workers, o.Lease)
	cfg.MaxRetries, cfg.RetryDelay, cfg.MaxRetryDelay, cfg.DedupWindow, cfg.Timeout = 2, time.Second, 4*time.Second, o.Window, 30*time.Second
	m, err := queue.NewManager(proc, cfg)
	if !s.CheckErr("new_manager", err) {
		return s.Finish(started)
	}
	if !s.CheckErr("manager_start", m.Start()) {
		return s.Finish(started)
	}
	stopped := false
	defer func() {
		if !stopped { // 异常路径也停掉 Manager（不计入结论）
			c, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			_ = m.Stop(c)
			cancel()
		}
	}()

	// ---- 中止控制：任何查询错误或越过安全阈值都使场景失败并停止生产 ----
	work, abortWork := context.WithCancel(ctx)
	defer abortWork()
	var abortMu sync.Mutex
	var abortReasons []string
	abort := func(why string) {
		abortMu.Lock()
		abortReasons = append(abortReasons, why)
		abortMu.Unlock()
		fmt.Printf("  [%s] ABORT: %s\n", s.Name, why)
		abortWork()
	}
	var queryErrs atomic.Int64
	qerr := func(name string, err error) bool {
		if err == nil {
			return true
		}
		queryErrs.Add(1)
		s.CheckErr(name, err)
		abort(name + ": " + err.Error())
		return false
	}
	op := func(f func(c context.Context) error) error {
		c, cancel := context.WithTimeout(work, 5*time.Second)
		defer cancel()
		return f(c)
	}

	t0 := time.Now()
	produceEnd := t0.Add(o.Dur)
	phase := atomic.Value{}
	phase.Store("produce")

	// ---- 采样 ----
	sf, err := os.OpenFile(env.path(o.Name+".samples.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if !s.CheckErr("samples_file", err) {
		return s.Finish(started)
	}
	var samples []soakSample
	var breached atomic.Bool
	lastSample := time.Time{}
	takeSample := func(c context.Context, seq int) (soakSample, error) {
		now := time.Now()
		x := soakSample{Seq: seq, T: now.Format(time.RFC3339Nano), ElapsedS: now.Sub(t0).Seconds(), Phase: phase.Load().(string),
			Submits: st.submits.Load(), Accepted: st.acceptedN.Load(), NotAccepted: st.notAccepted.Load(), Calls: calls.Load(),
			DoneObs: st.doneN.Load(), FailedObs: st.failedN.Load(), Replayed: st.replayedN.Load()}
		if !lastSample.IsZero() {
			x.GapMs = now.Sub(lastSample).Milliseconds()
		}
		lastSample = now
		st.mu.Lock()
		x.Outstanding = len(st.outstanding)
		st.mu.Unlock()
		info, err := rr.Info(c, "memory", "persistence").Result()
		if err != nil {
			return x, fmt.Errorf("INFO: %w", err)
		}
		for _, f := range []struct {
			key string
			dst *int64
		}{{"used_memory", &x.RedisUsed}, {"used_memory_rss", &x.RedisRSS}, {"used_memory_peak", &x.RedisPeak}} {
			if *f.dst, err = infoInt(info, f.key); err != nil {
				return x, err
			}
		}
		if x.AOFStatus = infoField(info, "aof_last_write_status"); x.AOFStatus == "" {
			return x, errors.New("INFO aof_last_write_status missing")
		}
		if x.DBSize, err = rr.DBSize(c).Result(); err != nil {
			return x, fmt.Errorf("DBSIZE: %w", err)
		}
		if x.Q, err = queueState(c, rr, q, false); err != nil {
			return x, err
		}
		out, err := env.D.Exec(c, in.C, "cat", "/sys/fs/cgroup/memory.current")
		if err != nil {
			return x, fmt.Errorf("container memory.current: %w", err)
		}
		if x.ContainerMem, err = strconv.ParseInt(strings.TrimSpace(out), 10, 64); err != nil {
			return x, fmt.Errorf("parse memory.current %q: %w", out, err)
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		x.GoHeap, x.GoSys, x.Goroutines = ms.HeapAlloc, ms.Sys, runtime.NumGoroutine()
		fi, err := os.Stat(s.L.Path())
		if err != nil {
			return x, err
		}
		x.LedgerBytes = fi.Size()
		var fs syscall.Statfs_t
		if err := syscall.Statfs(env.Dir, &fs); err != nil {
			return x, fmt.Errorf("statfs: %w", err)
		}
		x.FreeDisk = uint64(fs.Bavail) * uint64(fs.Bsize)
		return x, nil
	}
	sampleOnce := func(seq int) {
		c, cancel := context.WithTimeout(ctx, 15*time.Second) // 采样用场景 ctx：中止后仍记录收尾状态
		x, err := takeSample(c, seq)
		cancel()
		if err != nil {
			qerr("soak_sample", err)
			return
		}
		x.Breaches = soakBreaches(x, o.Limits)
		if len(x.Breaches) > 0 && !breached.Swap(true) {
			s.Check("soak_safety_thresholds", false, "sample %d at %.1fs: %v", x.Seq, x.ElapsedS, x.Breaches)
			abort("safety threshold: " + strings.Join(x.Breaches, "; "))
		}
		b, _ := json.Marshal(x)
		if _, err := sf.Write(append(b, '\n')); err != nil {
			qerr("soak_samples_write", err)
			return
		}
		_ = sf.Sync()
		samples = append(samples, x)
	}
	samplerDone := make(chan struct{})
	samplerStop := make(chan struct{})
	go func() {
		defer close(samplerDone)
		seq := 0
		sampleOnce(seq)
		t := time.NewTicker(o.Sample)
		defer t.Stop()
		for {
			select {
			case <-samplerStop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				seq++
				sampleOnce(seq)
			}
		}
	}()

	// ---- 完成观察（连续轮询；每个ID在墓碑到期前被观察，完成证据不依赖到期后的状态） ----
	observe := func() {
		st.mu.Lock()
		ids := make([]string, 0, len(st.outstanding))
		for id := range st.outstanding {
			ids = append(ids, id)
		}
		st.mu.Unlock()
		sort.Strings(ids)
		for lo := 0; lo < len(ids); lo += 250 {
			batch := ids[lo:min(lo+250, len(ids))]
			pollAt := time.Now()
			var ex []*redis.IntCmd
			var gt, hs []*redis.StringCmd
			err := op(func(c context.Context) error {
				pipe := rr.Pipeline()
				ex, gt, hs = make([]*redis.IntCmd, len(batch)), make([]*redis.StringCmd, len(batch)), make([]*redis.StringCmd, len(batch))
				for i, id := range batch {
					ex[i], gt[i], hs[i] = pipe.Exists(c, k.mp+id), pipe.Get(c, k.dp+id), pipe.HGet(c, k.mp+id, "s")
				}
				_, err := pipe.Exec(c)
				if errors.Is(err, redis.Nil) {
					err = nil
				}
				return err
			})
			if !qerr("soak_observe_pipeline", err) {
				return
			}
			now := time.Now()
			for i, id := range batch {
				fs := ""
				if e := hs[i].Err(); e == nil {
					fs = hs[i].Val()
				} else if !errors.Is(e, redis.Nil) {
					qerr("soak_observe_hget", e)
					return
				}
				if ex[i].Err() != nil {
					qerr("soak_observe_exists", ex[i].Err())
					return
				}
				st.mu.Lock()
				sha := st.outstanding[id]
				st.mu.Unlock()
				state, e := idStatus("", ex[i].Val(), gt[i].Val(), gt[i].Err(), sha, fs)
				if e != nil {
					qerr("soak_observe_get", e)
					return
				}
				idx := soakIndex(id)
				if state == ledger.StateFailed && soakClass(idx) != "fatal" {
					state = "" // replay 类等待重放；其它类的失败由巡检判为未预期
				}
				st.mu.Lock()
				if state == "" {
					st.prev[id] = pollAt
					st.mu.Unlock()
					continue
				}
				ub := now.Sub(st.prev[id])
				delete(st.outstanding, id)
				delete(st.prev, id)
				if state == ledger.StateDone {
					st.done[id] = doneObs{T: now, UB: ub}
					switch soakClass(idx) {
					case "dupprobe":
						if it, err := soakItem(idx); err == nil {
							st.probes = append(st.probes, soakProbe{Due: now, Kind: "dup-same", It: it}, soakProbe{Due: now, Kind: "dup-conflict", It: it})
						}
					case "expire":
						if due := now.Add(o.Window + 10*time.Second); !st.expireResub[id] && due.Before(produceEnd.Add(-5*time.Second)) {
							if it, err := soakItem(idx); err == nil {
								st.probes = append(st.probes, soakProbe{Due: due, Kind: "expire-resubmit", It: it})
							}
						}
					}
				}
				st.mu.Unlock()
				if aerr := s.L.Append(ledger.Rec{K: ledger.KFinal, ID: id, State: state, UB: ub.Nanoseconds()}); aerr != nil {
					qerr("soak_ledger_append", aerr)
					return
				}
				if state == ledger.StateDone {
					st.doneN.Add(1)
				} else {
					st.failedN.Add(1)
				}
			}
		}
	}
	// ---- 失败留存巡检：只重放 replay 类；其它类出现在失败留存即为未预期 ----
	var inspectMu sync.Mutex // 巡检协程与收尾核对互斥，避免同一ID被重放两次
	inspectFailed := func() {
		inspectMu.Lock()
		defer inspectMu.Unlock()
		var lf []queue.FailedMessage
		err := op(func(c context.Context) error {
			var e error
			lf, e = m.ListFailed(c, 1000)
			return e
		})
		if !qerr("soak_list_failed", err) {
			return
		}
		for _, f := range lf {
			idx := soakIndex(f.ID)
			switch cls := soakClass(idx); {
			case idx >= 0 && cls == "replay":
				st.mu.Lock()
				again := st.replayed[f.ID]
				st.mu.Unlock()
				if again {
					st.unexpectedFailed.Add(1)
					st.bad("replayed task failed again: " + f.ID)
					continue
				}
				if err := op(func(c context.Context) error { return m.ReplayFailed(c, f.ID) }); !qerr("soak_replay_failed", err) {
					return
				}
				st.mu.Lock()
				st.replayed[f.ID] = true
				st.mu.Unlock()
				st.replayedN.Add(1)
			case idx >= 0 && cls == "fatal":
				it, err := soakItem(idx)
				if err != nil || string(f.Data) != string(it.Payload) || !strings.Contains(f.Reason, errScriptedFatal.Error()) || f.Attempts != 1 {
					st.fatalBad.Add(1)
					st.bad(fmt.Sprintf("failed record mismatch %s reason=%q attempts=%d", f.ID, truncate(f.Reason, 60), f.Attempts))
				}
			default:
				st.unexpectedFailed.Add(1)
				st.bad(fmt.Sprintf("unexpected failed task %s reason=%q", f.ID, truncate(f.Reason, 80)))
			}
		}
	}
	bgDone := make(chan struct{})
	bgStop := make(chan struct{})
	go func() {
		defer close(bgDone)
		ot := time.NewTicker(o.Poll)
		rt := time.NewTicker(o.Replay)
		defer ot.Stop()
		defer rt.Stop()
		for {
			select {
			case <-bgStop:
				return
			case <-work.Done():
				return
			case <-ot.C:
				observe()
			case <-rt.C:
				inspectFailed()
			}
		}
	}()

	// ---- 生产：单一限速器，探针优先；提交调用总数（含冲突探针）不超过上限 ----
	submit := func(it item, try int) string {
		st.submits.Add(1)
		r := s.submit(work, mgrEnqueue(m), it, try)
		if r == ledger.ResAccepted {
			st.acceptedN.Add(1)
		} else {
			st.notAccepted.Add(1)
		}
		return r
	}
	track := func(it item, tb time.Time) {
		st.mu.Lock()
		st.outstanding[it.ID] = it.Sha
		st.prev[it.ID] = tb
		st.accepted[it.ID] = true
		if soakClass(soakIndex(it.ID)) == "fatal" {
			st.fatalIDs[it.ID] = it
		}
		st.mu.Unlock()
	}
	next := 0
	tick := time.NewTicker(time.Duration(float64(time.Second) / o.Rate))
produce:
	for {
		select {
		case <-work.Done():
			break produce
		case <-tick.C:
		}
		if !time.Now().Before(produceEnd) || int(st.submits.Load()) >= o.MaxSubs {
			break
		}
		st.mu.Lock()
		var pr *soakProbe
		for i := range st.probes {
			if !time.Now().Before(st.probes[i].Due) {
				p := st.probes[i]
				st.probes = append(st.probes[:i], st.probes[i+1:]...)
				pr = &p
				break
			}
		}
		st.mu.Unlock()
		if pr != nil {
			switch pr.Kind {
			case "dup-same":
				if r := submit(pr.It, 2); r != ledger.ResAccepted {
					st.dupBad.Add(1)
					st.bad("dup-same probe not accepted as duplicate: " + pr.It.ID + " " + r)
				}
			case "dup-conflict":
				st.submits.Add(1)
				other, _ := ledger.MakePayload("zz-"+pr.It.ID, len(pr.It.Payload))
				err := op(func(c context.Context) error { return m.AddDataWithID(c, pr.It.ID, other) })
				if errors.Is(err, common.ErrIDConflict) {
					st.conflictOK.Add(1)
				} else {
					st.conflictBad.Add(1)
					st.bad(fmt.Sprintf("conflict probe %s: %v", pr.It.ID, err))
				}
			case "expire-resubmit":
				var n int64
				err := op(func(c context.Context) error {
					var e error
					n, e = rr.Exists(c, k.dp+pr.It.ID, k.mp+pr.It.ID).Result()
					return e
				})
				if !qerr("soak_expire_probe", err) {
					break produce
				}
				if n != 0 {
					st.expireProbeBad.Add(1)
					st.bad(fmt.Sprintf("tombstone/record still present %s after window+10s", pr.It.ID))
					continue
				}
				tb := time.Now()
				if r := submit(pr.It, 2); r == ledger.ResAccepted {
					st.mu.Lock()
					st.expireResub[pr.It.ID] = true
					st.mu.Unlock()
					track(pr.It, tb)
				} else {
					st.expireProbeBad.Add(1)
					st.bad("expire resubmit not accepted: " + pr.It.ID + " " + r)
				}
			}
			continue
		}
		it, err := soakItem(next)
		if !s.CheckErr("soak_make_item", err) {
			abort("make item")
			break
		}
		next++
		tb := time.Now()
		if submit(it, 1) == ledger.ResAccepted {
			track(it, tb)
		}
	}
	tick.Stop()
	prodEnd := time.Now()
	s.Metrics["production_s"] = prodEnd.Sub(t0).Seconds()
	s.Metrics["new_tasks"] = next
	s.Check("soak_production_window_reached", work.Err() == nil && !prodEnd.Before(produceEnd) || int(st.submits.Load()) >= o.MaxSubs,
		"production stopped after %.1fs (planned %.0fs), submit calls %d", prodEnd.Sub(t0).Seconds(), o.Dur.Seconds(), st.submits.Load())
	s.Check("soak_submit_cap_respected", int(st.submits.Load()) <= o.MaxSubs, "submit calls %d cap %d", st.submits.Load(), o.MaxSubs)

	// ---- 有界排空：全部已接受任务到终态，replay 类全部重放并完成 ----
	phase.Store("drain")
	drainDeadline := time.Now().Add(o.DrainMax)
	drained := false
	for work.Err() == nil && time.Now().Before(drainDeadline) {
		st.mu.Lock()
		left := len(st.outstanding)
		st.mu.Unlock()
		if left == 0 {
			drained = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	s.Metrics["drain_s"] = time.Since(prodEnd).Seconds()
	st.mu.Lock()
	leftIDs := make([]string, 0, len(st.outstanding))
	for id := range st.outstanding {
		leftIDs = append(leftIDs, id)
	}
	st.mu.Unlock()
	sort.Strings(leftIDs)
	s.Check("soak_all_accepted_reached_final", drained, "%d accepted ids not final after drain (first %v)", len(leftIDs), firstN(leftIDs, 5))
	s.Check("soak_conflict_probes", st.conflictBad.Load() == 0 && st.conflictOK.Load() > 0, "ok=%d bad=%d", st.conflictOK.Load(), st.conflictBad.Load())
	s.Check("soak_dup_probes", st.dupBad.Load() == 0, "bad=%d", st.dupBad.Load())
	st.mu.Lock()
	nExpire := len(st.expireResub)
	st.mu.Unlock()
	s.Check("soak_expire_probes", st.expireProbeBad.Load() == 0 && nExpire > 0, "resubmitted-after-expiry=%d bad=%d", nExpire, st.expireProbeBad.Load())
	s.Metrics["conflict_probes_ok"], s.Metrics["expire_resubmits"] = st.conflictOK.Load(), nExpire

	// 排空时刻的逐ID墓碑三态核对：存在则摘要一致且未超过窗口；不存在则必须已过“最早可能到期时刻”
	if work.Err() == nil {
		soakTombCheck(work, s, rr, k, st, o.Window, qerr)
	}

	// ---- 失败留存最终核对（Stop 之前，公开 API） ----
	inspectFailed()
	s.Check("soak_failed_list_matches_script", st.unexpectedFailed.Load() == 0 && st.fatalBad.Load() == 0,
		"unexpected=%d fatal-mismatch=%d first=%v", st.unexpectedFailed.Load(), st.fatalBad.Load(), st.firstBad.Load())

	// ---- 等一个 DedupWindow 让墓碑全部到期，再停 ----
	phase.Store("expiry-wait")
	waitUntil := time.Now().Add(o.Window + 15*time.Second)
	for work.Err() == nil && time.Now().Before(waitUntil) {
		time.Sleep(time.Second)
	}
	var lf []queue.FailedMessage
	err = op(func(c context.Context) error {
		var e error
		lf, e = m.ListFailed(c, 1000)
		return e
	})
	if qerr("soak_final_list_failed", err) {
		st.mu.Lock()
		want := len(st.fatalIDs)
		okAll := len(lf) == want
		for _, f := range lf {
			it, ok := st.fatalIDs[f.ID]
			okAll = okAll && ok && string(f.Data) == string(it.Payload)
		}
		st.mu.Unlock()
		s.Check("soak_final_failed_listed", okAll, "listed %d, expected exactly the %d fatal-class ids with payload", len(lf), want)
	}
	close(bgStop)
	<-bgDone
	phase.Store("stop")
	sc, scancel := context.WithTimeout(context.Background(), 40*time.Second)
	serr := m.Stop(sc)
	scancel()
	stopped = true
	s.Check("soak_stop_ok", serr == nil, "Stop: %v", serr)
	if q2, err := queueState(ctx, rr, q, false); qerr("soak_final_state", err) {
		s.Metrics["state_final"] = q2
		nf := int64(len(st.fatalIDs))
		s.Check("soak_final_quiescent_after_window", q2 == QState{Failed: nf, Records: nf}, "got %+v want failed=records=%d and no tombstones after window", q2, nf)
	}
	close(samplerStop)
	<-samplerDone
	phase.Store("final")
	sampleOnce(-1) // 采样协程已退出，最后一次在本协程内取
	if err := sf.Close(); err != nil {
		s.CheckErr("samples_close", err)
	}
	if err := writeSamplesCSV(env.path(o.Name+".samples.csv"), samples); err != nil {
		s.CheckErr("samples_csv", err)
	}

	// 采样连续性：数量与最大间隔
	expected := int(time.Since(t0)/o.Sample) - 2
	maxGap := int64(0)
	for _, x := range samples {
		maxGap = max(maxGap, x.GapMs)
	}
	s.Metrics["samples"], s.Metrics["max_sample_gap_ms"] = len(samples), maxGap
	s.Check("soak_samples_continuous", len(samples) >= expected && maxGap <= o.Limits.MaxSampleGap, "samples=%d expected>=%d max_gap=%dms", len(samples), expected, maxGap)
	if !breached.Load() {
		s.Check("soak_safety_thresholds", true, "no threshold crossed in %d samples", len(samples))
	}
	s.Check("soak_no_query_errors", queryErrs.Load() == 0, "%d query/operation errors", queryErrs.Load())
	abortMu.Lock()
	s.Metrics["abort_reasons"] = abortReasons
	abortMu.Unlock()
	s.Metrics["trend"] = soakTrend(samples, 5*60)
	s.Metrics["submit_calls"], s.Metrics["processor_calls"], s.Metrics["replayed"] = st.submits.Load(), calls.Load(), st.replayedN.Load()

	// 期望：脚本调用次数（由提交侧与脚本决定，不从 Processor 观察反推）
	exp := ledger.Expect{ExpectCalls: map[string]int{}}
	st.mu.Lock()
	for id := range st.accepted {
		switch soakClass(soakIndex(id)) {
		case "replay", "panic1", "retry1":
			exp.ExpectCalls[id] = 2
		case "fatal":
			exp.ExpectCalls[id] = 1
			exp.ExpectedFailed = append(exp.ExpectedFailed, id)
		case "expire":
			exp.ExpectCalls[id] = 1
			if st.expireResub[id] {
				exp.ExpectCalls[id] = 2
			}
		}
	}
	st.mu.Unlock()
	sort.Strings(exp.ExpectedFailed)
	s.Exp = exp
	s.Metrics["total_s"] = time.Since(started).Seconds()
	return s.Finish(started,
		"memory/key trend over a bounded window is an observation, NOT a proof that nothing leaks",
		"tombstones that expired after a recorded done observation are expected, not loss; loss is judged per id from in-run done observations",
		"producer and consumer share one process; rate/size/worker limits are the task's bounded soak limits, not a capacity test")
}

// soakTombCheck 在排空时刻逐ID核对墓碑三态（不把已到期墓碑当作丢失）
func soakTombCheck(ctx context.Context, s *Sc, r *redis.Client, k qkeys, st *soakState, window time.Duration, qerr func(string, error) bool) {
	const slack = 2 * time.Second
	st.mu.Lock()
	type row struct {
		id  string
		sha string
		obs doneObs
	}
	var rows []row
	for id, d := range st.done {
		idx := soakIndex(id)
		it, err := soakItem(idx)
		if err != nil {
			st.mu.Unlock()
			qerr("soak_tomb_item", err)
			return
		}
		rows = append(rows, row{id, it.Sha, d})
	}
	st.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	present, expired, bad := 0, 0, 0
	var first []string
	for lo := 0; lo < len(rows); lo += 250 {
		batch := rows[lo:min(lo+250, len(rows))]
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		pipe := r.Pipeline()
		ex, gt, pt := make([]*redis.IntCmd, len(batch)), make([]*redis.StringCmd, len(batch)), make([]*redis.DurationCmd, len(batch))
		for i, x := range batch {
			ex[i], gt[i], pt[i] = pipe.Exists(c, k.mp+x.id), pipe.Get(c, k.dp+x.id), pipe.PTTL(c, k.dp+x.id)
		}
		_, err := pipe.Exec(c)
		cancel()
		if err != nil && !errors.Is(err, redis.Nil) {
			qerr("soak_tomb_pipeline", err)
			return
		}
		now := time.Now()
		for i, x := range batch {
			if ex[i].Err() != nil || pt[i].Err() != nil || (gt[i].Err() != nil && !errors.Is(gt[i].Err(), redis.Nil)) {
				qerr("soak_tomb_query", fmt.Errorf("%s: %v %v %v", x.id, ex[i].Err(), gt[i].Err(), pt[i].Err()))
				return
			}
			var why string
			switch {
			case ex[i].Val() != 0:
				why = "task record still exists after done"
			case gt[i].Err() == nil: // 墓碑存在
				switch {
				case gt[i].Val() != x.sha:
					why = "tombstone digest mismatch"
				case pt[i].Val() <= 0 || pt[i].Val() > window:
					why = fmt.Sprintf("tombstone PTTL %v outside (0,%v]", pt[i].Val(), window)
				case now.After(x.obs.T.Add(window + slack)):
					why = "tombstone outlived DedupWindow"
				default:
					present++
				}
			default: // 墓碑不存在：只有在最早可能到期时刻之后才是“按预期到期”
				if now.Before(x.obs.T.Add(-x.obs.UB).Add(window - slack)) {
					why = fmt.Sprintf("tombstone missing before it could have expired (done observed %v ago)", now.Sub(x.obs.T).Round(time.Millisecond))
				} else {
					expired++
				}
			}
			if why != "" {
				bad++
				if len(first) < 5 {
					first = append(first, x.id+": "+why)
				}
			}
		}
	}
	s.Metrics["tomb_check"] = map[string]int{"done_ids": len(rows), "present_in_window": present, "expired_as_expected": expired, "violations": bad}
	s.Check("soak_per_id_tombstone_tristate", bad == 0 && len(rows) > 0, "done=%d present=%d expired=%d bad=%d first=%v", len(rows), present, expired, bad, first)
}

// soakTrend 生产阶段内（跳过预热）的线性趋势，单位：每分钟
func soakTrend(xs []soakSample, warmupS float64) map[string]any {
	type ser struct {
		name string
		get  func(soakSample) float64
	}
	series := []ser{
		{"redis_used_memory", func(x soakSample) float64 { return float64(x.RedisUsed) }},
		{"container_memory_current", func(x soakSample) float64 { return float64(x.ContainerMem) }},
		{"gate_heap_alloc", func(x soakSample) float64 { return float64(x.GoHeap) }},
		{"queue_keys_records_plus_tombs", func(x soakSample) float64 { return float64(x.Q.Records + x.Q.Tombs) }},
		{"dbsize", func(x soakSample) float64 { return float64(x.DBSize) }},
		{"gate_goroutines", func(x soakSample) float64 { return float64(x.Goroutines) }},
	}
	var pts []soakSample
	for _, x := range xs {
		if x.Phase == "produce" && x.ElapsedS >= warmupS {
			pts = append(pts, x)
		}
	}
	out := map[string]any{"window": fmt.Sprintf("produce phase, elapsed >= %.0fs", warmupS), "points": len(pts),
		"note": "least-squares slope per minute; an observation over a bounded run, not a leak-freedom proof"}
	if len(pts) < 3 {
		return out
	}
	for _, sr := range series {
		var sx, sy, sxx, sxy, mx float64
		n := float64(len(pts))
		for _, x := range pts {
			t, v := x.ElapsedS/60, sr.get(x)
			sx, sy, sxx, sxy = sx+t, sy+v, sxx+t*t, sxy+t*v
			mx = max(mx, v)
		}
		slope := 0.0
		if d := n*sxx - sx*sx; d != 0 {
			slope = (n*sxy - sx*sy) / d
		}
		out[sr.name] = map[string]float64{"first": sr.get(pts[0]), "last": sr.get(pts[len(pts)-1]), "max": mx, "slope_per_min": slope}
	}
	return out
}

func writeSamplesCSV(path string, xs []soakSample) error {
	var b strings.Builder
	b.WriteString("seq,t,elapsed_s,gap_ms,phase,submit_calls,accepted,not_accepted,processor_calls,done_observed,failed_observed,outstanding,replayed," +
		"redis_used_memory,redis_used_memory_rss,redis_used_memory_peak,aof_last_write_status,dbsize,stream_len,pending,delayed,leases,expired_leases,failed,records_m,tombstones_d," +
		"container_memory_current,gate_heap_alloc,gate_sys,gate_goroutines,ledger_bytes,free_disk_bytes,breaches\n")
	for _, x := range xs {
		fmt.Fprintf(&b, "%d,%s,%.3f,%d,%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%q\n",
			x.Seq, x.T, x.ElapsedS, x.GapMs, x.Phase, x.Submits, x.Accepted, x.NotAccepted, x.Calls, x.DoneObs, x.FailedObs, x.Outstanding, x.Replayed,
			x.RedisUsed, x.RedisRSS, x.RedisPeak, x.AOFStatus, x.DBSize, x.Q.StreamLen, x.Q.Pending, x.Q.Delayed, x.Q.Leases, x.Q.Expired, x.Q.Failed, x.Q.Records, x.Q.Tombs,
			x.ContainerMem, x.GoHeap, x.GoSys, x.Goroutines, x.LedgerBytes, x.FreeDisk, strings.Join(x.Breaches, "; "))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
