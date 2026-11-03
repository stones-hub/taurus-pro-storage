package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

type procFunc func(ctx context.Context, data []byte) error

func (f procFunc) Process(ctx context.Context, data []byte) error { return f(ctx, data) }

func memConfig(mod func(*Config)) *Config {
	c := &Config{
		EngineType:    engine.CHANNEL,
		Source:        "unit",
		WorkerCount:   2,
		MaxRetries:    2,
		RetryDelay:    10 * time.Millisecond,
		MaxRetryDelay: 20 * time.Millisecond,
		RetryFactor:   2,
		Timeout:       2 * time.Second,
		LeaseDuration: time.Second,
		PollInterval:  5 * time.Millisecond,
	}
	if mod != nil {
		mod(c)
	}
	return c
}

func startMem(t *testing.T, p Processor, mod func(*Config)) *Manager {
	t.Helper()
	m, err := NewManager(p, memConfig(mod))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if m.GetStatus() == StatusRunning {
			_ = m.Stop(context.Background())
		}
	})
	return m
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestConfigDefaultsAndExplicitZero(t *testing.T) {
	c := &Config{Source: "q", EngineType: engine.CHANNEL, MaxRetries: 0}
	if err := c.MergeWithDefaults(); err != nil {
		t.Fatal(err)
	}
	if c.MaxRetries != 0 {
		t.Errorf("explicit MaxRetries=0 must not be overwritten, got %d", c.MaxRetries)
	}
	if c.WorkerCount != 5 || c.LeaseDuration != 30*time.Second || c.DedupWindow != 24*time.Hour || c.EnableRetryQueue || c.EnableFailedQueue {
		t.Errorf("unexpected merged config %+v", c)
	}
	d := DefaultConfig()
	if d.MaxRetries != 3 || d.Source != "" {
		t.Errorf("DefaultConfig %+v", d)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"empty source":     func(c *Config) { c.Source = "" },
		"bad source chars": func(c *Config) { c.Source = "a b" },
		"brace in source":  func(c *Config) { c.Source = "a{b}" },
		"negative retries": func(c *Config) { c.MaxRetries = -1 },
		"tiny lease":       func(c *Config) { c.LeaseDuration = time.Millisecond },
		"negative window":  func(c *Config) { c.DedupWindow = -1 },
		"bad engine":       func(c *Config) { c.EngineType = "kafka" },
		"negative workers": func(c *Config) { c.WorkerCount = -1 },
	}
	for name, mod := range cases {
		c := memConfig(mod)
		if _, err := NewManager(procFunc(func(context.Context, []byte) error { return nil }), c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := NewManager(nil, memConfig(nil)); err == nil {
		t.Error("nil processor")
	}
	if _, err := NewManager(procFunc(func(context.Context, []byte) error { return nil }), nil); err == nil {
		t.Error("nil config")
	}
}

func TestNewManagerDoesNotMutateCallerConfig(t *testing.T) {
	c := &Config{Source: "q", EngineType: engine.CHANNEL}
	if _, err := NewManager(procFunc(func(context.Context, []byte) error { return nil }), c); err != nil {
		t.Fatal(err)
	}
	if c.WorkerCount != 0 || c.LeaseDuration != 0 {
		t.Errorf("caller config mutated: %+v", c)
	}
}

func TestDeprecatedFieldsAreAcceptedWithWarning(t *testing.T) {
	c := memConfig(func(c *Config) { c.ReaderCount = 3; c.Processing = "p"; c.EnableFailedDataToFile = true })
	if _, err := NewManager(procFunc(func(context.Context, []byte) error { return nil }), c); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessFlow(t *testing.T) {
	var got sync.Map
	m := startMem(t, procFunc(func(_ context.Context, d []byte) error { got.Store(string(d), true); return nil }), nil)
	for i := 0; i < 20; i++ {
		if err := m.AddData(context.Background(), []byte(fmt.Sprintf("msg-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "all processed", func() bool {
		n := 0
		got.Range(func(_, _ any) bool { n++; return true })
		return n == 20
	})
	if f, _ := m.ListFailed(context.Background(), 10); len(f) != 0 {
		t.Fatalf("unexpected failed: %v", f)
	}
}

func TestPanicIsIsolatedRetriedThenRetained(t *testing.T) {
	var calls, ok atomic.Int64
	m := startMem(t, procFunc(func(_ context.Context, d []byte) error {
		if string(d) == "boom" {
			calls.Add(1)
			panic("kaboom")
		}
		ok.Add(1)
		return nil
	}), func(c *Config) { c.WorkerCount = 1; c.MaxRetries = 1 })

	_ = m.AddDataWithID(context.Background(), "bad", []byte("boom"))
	for i := 0; i < 5; i++ {
		_ = m.AddData(context.Background(), []byte("fine"))
	}
	// 同一个 worker 在 panic 之后仍继续服务
	eventually(t, "good messages processed after panic", func() bool { return ok.Load() == 5 })
	eventually(t, "panic task retained as failed", func() bool {
		f, _ := m.ListFailed(context.Background(), 10)
		return len(f) == 1
	})
	f, _ := m.ListFailed(context.Background(), 10)
	if f[0].ID != "bad" || !strings.Contains(f[0].Reason, "retries exhausted") || !strings.Contains(f[0].Reason, "kaboom") {
		t.Fatalf("failed record: %+v", f[0])
	}
	if calls.Load() != 2 { // 首次 + 1 次重试
		t.Fatalf("panic task processed %d times, want 2", calls.Load())
	}
	// 失败任务不会被自动再次处理
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("failed task reprocessed automatically: %d", calls.Load())
	}
}

func TestRetryPolicies(t *testing.T) {
	var attempts atomic.Int64
	m := startMem(t, procFunc(func(_ context.Context, d []byte) error {
		switch string(d) {
		case "flaky":
			if attempts.Add(1) < 3 {
				return common.NewRetryableError(errors.New("try later"))
			}
			return nil
		case "wrapped":
			return fmt.Errorf("wrapped: %w", common.NewRetryableError(errors.New("x")))
		default: // permanent
			return errors.New("permanent")
		}
	}), nil)
	ctx := context.Background()
	_ = m.AddDataWithID(ctx, "flaky", []byte("flaky"))
	_ = m.AddDataWithID(ctx, "perm", []byte("perm"))
	_ = m.AddDataWithID(ctx, "wrapped", []byte("wrapped"))
	eventually(t, "perm+wrapped failed", func() bool {
		f, _ := m.ListFailed(ctx, 10)
		return len(f) == 2
	})
	if attempts.Load() != 3 {
		t.Fatalf("flaky attempts = %d", attempts.Load())
	}
	f, _ := m.ListFailed(ctx, 10)
	reasons := map[string]string{}
	for _, x := range f {
		reasons[x.ID] = x.Reason
	}
	if reasons["perm"] != "permanent" || !strings.HasPrefix(reasons["wrapped"], "retries exhausted") {
		t.Fatalf("reasons: %v", reasons)
	}
}

func TestMaxRetriesZeroMeansNoRetry(t *testing.T) {
	var calls atomic.Int64
	m := startMem(t, procFunc(func(context.Context, []byte) error {
		calls.Add(1)
		return common.NewRetryableError(errors.New("x"))
	}), func(c *Config) { c.MaxRetries = 0 })
	_ = m.AddData(context.Background(), []byte("a"))
	eventually(t, "failed", func() bool { f, _ := m.ListFailed(context.Background(), 5); return len(f) == 1 })
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestReplayFailed(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var ok atomic.Int64
	m := startMem(t, procFunc(func(context.Context, []byte) error {
		if fail.Load() {
			return errors.New("nope")
		}
		ok.Add(1)
		return nil
	}), nil)
	ctx := context.Background()
	_ = m.AddDataWithID(ctx, "job", []byte("x"))
	eventually(t, "failed", func() bool { f, _ := m.ListFailed(ctx, 5); return len(f) == 1 })
	if err := m.ReplayFailed(ctx, "missing"); !errors.Is(err, common.ErrFailedNotFound) {
		t.Fatal(err)
	}
	fail.Store(false)
	if err := m.ReplayFailed(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "replayed task succeeds", func() bool { return ok.Load() == 1 })
	if f, _ := m.ListFailed(ctx, 5); len(f) != 0 {
		t.Fatalf("failed left: %v", f)
	}
}

func TestAddDataWithIDDedupAndConflict(t *testing.T) {
	var calls atomic.Int64
	m := startMem(t, procFunc(func(context.Context, []byte) error { calls.Add(1); return nil }), nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := m.AddDataWithID(ctx, "same", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "processed", func() bool { return calls.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("processed %d times", calls.Load())
	}
	// 完成后窗口内仍去重
	if err := m.AddDataWithID(ctx, "same", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := m.AddDataWithID(ctx, "same", []byte("y")); !errors.Is(err, common.ErrIDConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err := m.AddDataWithID(ctx, "bad id", []byte("y")); !errors.Is(err, common.ErrInvalidID) {
		t.Fatalf("invalid id: %v", err)
	}
	if err := m.AddData(ctx, nil); !errors.Is(err, common.ErrItemEmpty) {
		t.Fatalf("empty: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.AddData(cctx, []byte("z")); !errors.Is(err, common.ErrContextCanceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestLifecycleSingleUse(t *testing.T) {
	m := startMem(t, procFunc(func(context.Context, []byte) error { return nil }), nil)
	if err := m.Start(); !errors.Is(err, common.ErrManagerAlreadyRunning) {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); !errors.Is(err, common.ErrManagerNotRunning) {
		t.Fatal(err)
	}
	if err := m.Start(); !errors.Is(err, common.ErrManagerClosed) {
		t.Fatalf("restart after stop: %v", err)
	}
	if err := m.AddData(context.Background(), []byte("x")); !errors.Is(err, common.ErrManagerNotRunning) {
		t.Fatal(err)
	}
	// 未启动就 Stop
	m2, _ := NewManager(procFunc(func(context.Context, []byte) error { return nil }), memConfig(nil))
	if err := m2.Stop(context.Background()); !errors.Is(err, common.ErrManagerNotRunning) {
		t.Fatal(err)
	}
}

func TestConcurrentStartStopAddDoesNotPanic(t *testing.T) {
	for round := 0; round < 20; round++ {
		m, err := NewManager(procFunc(func(context.Context, []byte) error { return nil }), memConfig(nil))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(3)
			go func() { defer wg.Done(); _ = m.Start() }()
			go func() { defer wg.Done(); _ = m.Stop(context.Background()) }()
			go func() { defer wg.Done(); _ = m.AddData(context.Background(), []byte("x")); _ = m.GetStats() }()
		}
		wg.Wait()
		if m.GetStatus() == StatusRunning {
			_ = m.Stop(context.Background())
		}
	}
}

func TestStopDrainsInflightAndStopsClaiming(t *testing.T) {
	var started, done atomic.Int64
	m := startMem(t, procFunc(func(context.Context, []byte) error {
		started.Add(1)
		time.Sleep(200 * time.Millisecond)
		done.Add(1)
		return nil
	}), func(c *Config) { c.WorkerCount = 1 })
	for i := 0; i < 5; i++ {
		_ = m.AddData(context.Background(), []byte("x"))
	}
	eventually(t, "first task started", func() bool { return started.Load() == 1 })
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if started.Load() != 1 || done.Load() != 1 {
		t.Fatalf("stop must finish in-flight and claim nothing new: started=%d done=%d", started.Load(), done.Load())
	}
}

func TestStopTimeoutIsReportedHonestly(t *testing.T) {
	release := make(chan struct{})
	var entered atomic.Bool
	m := startMem(t, procFunc(func(ctx context.Context, _ []byte) error {
		entered.Store(true)
		<-release // 故意不响应 ctx 取消
		return nil
	}), func(c *Config) { c.Timeout = 200 * time.Millisecond; c.WorkerCount = 1 })
	_ = m.AddData(context.Background(), []byte("x"))
	eventually(t, "processor entered", entered.Load)

	start := time.Now()
	err := m.Stop(context.Background())
	if !errors.Is(err, common.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("stop took too long: %v", time.Since(start))
	}
	if m.GetStatus() != StatusStopped {
		t.Fatalf("status %v", m.GetStatus())
	}
	if err := m.Start(); !errors.Is(err, common.ErrManagerClosed) {
		t.Fatalf("restart after timeout stop: %v", err)
	}
	close(release)
}

func TestStopTimeoutCancelsCooperativeProcessorAndKeepsTask(t *testing.T) {
	var entered atomic.Bool
	m := startMem(t, procFunc(func(ctx context.Context, _ []byte) error {
		entered.Store(true)
		<-ctx.Done()
		return ctx.Err()
	}), func(c *Config) { c.Timeout = 100 * time.Millisecond; c.WorkerCount = 1 })
	_ = m.AddDataWithID(context.Background(), "t1", []byte("x"))
	eventually(t, "entered", entered.Load)
	if err := m.Stop(context.Background()); !errors.Is(err, common.ErrStopTimeout) {
		t.Fatalf("got %v", err)
	}
	// 被取消不是业务失败：任务既未失败留存也未被确认
	if f, _ := m.ListFailed(context.Background(), 5); len(f) != 0 {
		t.Fatalf("interrupted task must not be failed: %v", f)
	}
}

func TestLongTaskIsLeaseRenewed(t *testing.T) {
	var calls atomic.Int64
	m := startMem(t, procFunc(func(ctx context.Context, _ []byte) error {
		calls.Add(1)
		select {
		case <-time.After(1800 * time.Millisecond): // 超过 3 倍租约
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), func(c *Config) { c.LeaseDuration = 600 * time.Millisecond; c.WorkerCount = 2 })
	_ = m.AddData(context.Background(), []byte("x"))
	time.Sleep(2200 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("task redelivered while being renewed: %d calls", calls.Load())
	}
}

// 续租发现凭证失效：取消 Processor，且不能再确认或把任务转为失败/重试。
type lostLeaseEngine struct {
	engine.ReliableEngine
	acks, fails, retries atomic.Int64
}

func (e *lostLeaseEngine) Renew(context.Context, *engine.Delivery) error { return common.ErrLeaseLost }
func (e *lostLeaseEngine) Ack(ctx context.Context, d *engine.Delivery) error {
	e.acks.Add(1)
	return e.ReliableEngine.Ack(ctx, d)
}
func (e *lostLeaseEngine) Fail(ctx context.Context, d *engine.Delivery, r string) error {
	e.fails.Add(1)
	return e.ReliableEngine.Fail(ctx, d, r)
}
func (e *lostLeaseEngine) Retry(ctx context.Context, d *engine.Delivery, dl time.Duration, r string) error {
	e.retries.Add(1)
	return e.ReliableEngine.Retry(ctx, d, dl, r)
}

func TestLeaseLostCancelsProcessorAndSkipsTransfer(t *testing.T) {
	// 无论 Processor 在租约失效后返回 nil、可重试错误还是 panic，都不能 Ack/Retry/Fail
	outcomes := map[string]func() error{
		"nil":       func() error { return nil },
		"retryable": func() error { return common.NewRetryableError(errors.New("x")) },
		"error":     func() error { return errors.New("plain") },
		"panic":     func() error { panic("kaboom") },
	}
	for name, result := range outcomes {
		t.Run(name, func(t *testing.T) {
			var cancelled atomic.Int64
			m, err := NewManager(procFunc(func(ctx context.Context, _ []byte) error {
				<-ctx.Done()
				cancelled.Add(1)
				return result()
			}), memConfig(func(c *Config) { c.LeaseDuration = 600 * time.Millisecond; c.WorkerCount = 1 }))
			if err != nil {
				t.Fatal(err)
			}
			wrap := &lostLeaseEngine{ReliableEngine: m.eng}
			m.eng = wrap
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			_ = m.AddData(context.Background(), []byte("x"))
			eventually(t, "processor cancelled after lease loss", func() bool { return cancelled.Load() >= 1 })
			time.Sleep(300 * time.Millisecond)
			if wrap.acks.Load()+wrap.fails.Load()+wrap.retries.Load() != 0 {
				t.Fatalf("lost lease must not ack/fail/retry: %d/%d/%d", wrap.acks.Load(), wrap.fails.Load(), wrap.retries.Load())
			}
			_ = m.Stop(context.Background())
		})
	}
}

// 续租超时/网络错误时，本地租约起点取请求发送前的时刻（保守），并在 lease 到期后取消 Processor。
type flakyRenewEngine struct {
	engine.ReliableEngine
	renews atomic.Int64
}

func (e *flakyRenewEngine) Renew(ctx context.Context, d *engine.Delivery) error {
	e.renews.Add(1)
	return errors.New("network down")
}

func TestRenewFailureCancelsProcessorAfterConservativeLease(t *testing.T) {
	var cancelledAt atomic.Value
	var started atomic.Value
	var once sync.Once // 租约过期后内存引擎会重新投递，只记录第一次
	m, err := NewManager(procFunc(func(ctx context.Context, _ []byte) error {
		first := false
		once.Do(func() { first = true; started.Store(time.Now()) })
		<-ctx.Done()
		if first {
			cancelledAt.Store(time.Now())
		}
		return nil
	}), memConfig(func(c *Config) { c.LeaseDuration = 900 * time.Millisecond; c.WorkerCount = 1 }))
	if err != nil {
		t.Fatal(err)
	}
	wrap := &flakyRenewEngine{ReliableEngine: m.eng}
	m.eng = wrap
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	_ = m.AddData(context.Background(), []byte("x"))
	eventually(t, "processor cancelled after renew failures", func() bool { return cancelledAt.Load() != nil })
	d := cancelledAt.Load().(time.Time).Sub(started.Load().(time.Time))
	if d < 900*time.Millisecond-50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("cancelled %v after start, want ~lease (900ms)", d)
	}
	_ = m.Stop(context.Background())
}

func TestRetryDelayBackoff(t *testing.T) {
	cfg := &Config{RetryDelay: time.Second, MaxRetryDelay: 10 * time.Second, RetryFactor: 2}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := retryDelay(cfg, i); got != w {
			t.Errorf("retries=%d: got %v want %v", i, got, w)
		}
	}
	if got := retryDelay(cfg, 100000); got != 10*time.Second {
		t.Errorf("large retries: %v", got)
	}
}
