package queue

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// 这些集成测试只连接 TAURUS_TEST_REDIS_ADDR 指定的隔离 Redis，未设置则跳过，
// 绝不回落到 localhost:6379。参见 scripts/reliability-integration.sh。

func testAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TAURUS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TAURUS_TEST_REDIS_ADDR not set; run scripts/reliability-integration.sh")
	}
	return addr
}

func connect(t testing.TB, addr string) *redisx.RedisClient {
	t.Helper()
	if err := redisx.InitRedis(redisx.WithAddrs(addr), redisx.WithTimeout(time.Second, 3*time.Second, 3*time.Second), redisx.WithMaxRetries(-1)); err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	return redisx.Redis
}

func uniq(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func redisConfig(client *redisx.RedisClient, name string, mod func(*Config)) *Config {
	c := &Config{
		EngineType:    engine.REDIS,
		RedisClient:   client,
		Source:        name,
		WorkerCount:   2,
		MaxRetries:    2,
		RetryDelay:    50 * time.Millisecond,
		MaxRetryDelay: 100 * time.Millisecond,
		RetryFactor:   2,
		Timeout:       3 * time.Second,
		LeaseDuration: time.Second,
		PollInterval:  20 * time.Millisecond,
	}
	if mod != nil {
		mod(c)
	}
	return c
}

func startRedisManager(t *testing.T, client *redisx.RedisClient, name string, p Processor, mod func(*Config)) *Manager {
	t.Helper()
	m, err := NewManager(p, redisConfig(client, name, mod))
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

func cleanQueue(t testing.TB, client *redisx.RedisClient, name string) {
	ctx := context.Background()
	iter := client.GetClient().Scan(ctx, 0, "taurus:q2:{"+name+"}:*", 500).Iterator()
	for iter.Next(ctx) {
		client.GetClient().Del(ctx, iter.Val())
	}
}

func keyCount(t testing.TB, client *redisx.RedisClient, name, kind string) int {
	ctx := context.Background()
	n := 0
	iter := client.GetClient().Scan(ctx, 0, "taurus:q2:{"+name+"}:"+kind+"*", 500).Iterator()
	for iter.Next(ctx) {
		n++
	}
	return n
}

func assertDrained(t *testing.T, client *redisx.RedisClient, name string) {
	t.Helper()
	ctx := context.Background()
	rdb := client.GetClient()
	prefix := "taurus:q2:{" + name + "}:"
	if n := rdb.XLen(ctx, prefix+"ready").Val(); n != 0 {
		t.Errorf("ready stream length = %d", n)
	}
	if p, err := rdb.XPending(ctx, prefix+"ready", "workers").Result(); err == nil && p.Count != 0 {
		t.Errorf("pending = %d", p.Count)
	}
	if n := rdb.ZCard(ctx, prefix+"leases").Val(); n != 0 {
		t.Errorf("leases = %d", n)
	}
	if n := rdb.ZCard(ctx, prefix+"delayed").Val(); n != 0 {
		t.Errorf("delayed = %d", n)
	}
	if n := keyCount(t, client, name, "m:"); n != 0 {
		t.Errorf("message records = %d", n)
	}
}

func TestRedisManagerEndToEnd(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("e2e")
	t.Cleanup(func() { cleanQueue(t, client, name) })

	var okCount, panics atomic.Int64
	var failMode atomic.Bool
	failMode.Store(true)
	m := startRedisManager(t, client, name, procFunc(func(_ context.Context, d []byte) error {
		switch string(d) {
		case "panic":
			panics.Add(1)
			panic("boom")
		case "permanent":
			if failMode.Load() {
				return errors.New("permanent failure")
			}
		}
		okCount.Add(1)
		return nil
	}), func(c *Config) { c.WorkerCount = 1; c.MaxRetries = 1 })
	ctx := context.Background()

	_ = m.AddDataWithID(ctx, "p1", []byte("panic"))
	_ = m.AddDataWithID(ctx, "perm", []byte("permanent"))
	for i := 0; i < 5; i++ {
		if err := m.AddData(ctx, []byte("ok")); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "ok messages processed after panic on the only worker", func() bool { return okCount.Load() == 5 })
	eventually(t, "two failures retained", func() bool { f, _ := m.ListFailed(ctx, 10); return len(f) == 2 })
	if panics.Load() != 2 {
		t.Fatalf("panic task ran %d times, want 2 (1 + 1 retry)", panics.Load())
	}
	byID := map[string]FailedMessage{}
	f, _ := m.ListFailed(ctx, 10)
	for _, x := range f {
		byID[x.ID] = x
	}
	if !strings.HasPrefix(byID["p1"].Reason, "retries exhausted: panic: boom") || byID["perm"].Reason != "permanent failure" {
		t.Fatalf("reasons: %+v", byID)
	}
	// 不会自动再次处理
	time.Sleep(300 * time.Millisecond)
	if panics.Load() != 2 {
		t.Fatalf("failed task reprocessed")
	}
	// 显式重放
	failMode.Store(false)
	if err := m.ReplayFailed(ctx, "perm"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "replayed task processed", func() bool { return okCount.Load() == 6 })
	if err := m.ReplayFailed(ctx, "perm"); !errors.Is(err, common.ErrFailedNotFound) {
		t.Fatalf("second replay: %v", err)
	}
	// p1 仍在失败留存
	f, _ = m.ListFailed(ctx, 10)
	if len(f) != 1 || f[0].ID != "p1" {
		t.Fatalf("remaining failed: %+v", f)
	}
}

func TestRedisPendingCleanupAfterProcessing(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("clean")
	t.Cleanup(func() { cleanQueue(t, client, name) })
	var n atomic.Int64
	m := startRedisManager(t, client, name, procFunc(func(context.Context, []byte) error { n.Add(1); return nil }), nil)
	for i := 0; i < 100; i++ {
		_ = m.AddData(context.Background(), []byte("x"))
	}
	eventually(t, "100 processed", func() bool { return n.Load() == 100 })
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDrained(t, client, name)
}

func TestRedisTwoInstancesShareOneQueue(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("two")
	t.Cleanup(func() { cleanQueue(t, client, name) })
	var mu sync.Mutex
	counts := map[string]int{}
	proc := func(owner string) Processor {
		return procFunc(func(_ context.Context, d []byte) error {
			mu.Lock()
			counts[string(d)]++
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			return nil
		})
	}
	m1 := startRedisManager(t, client, name, proc("a"), func(c *Config) { c.WorkerCount = 4 })
	m2 := startRedisManager(t, client, name, proc("b"), func(c *Config) { c.WorkerCount = 4 })
	const total = 300
	for i := 0; i < total; i++ {
		if err := m1.AddDataWithID(context.Background(), fmt.Sprintf("id-%d", i), []byte(fmt.Sprintf("job-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "all processed", func() bool { mu.Lock(); defer mu.Unlock(); return len(counts) == total })
	_ = m1.Stop(context.Background())
	_ = m2.Stop(context.Background())
	for k, v := range counts {
		if v != 1 {
			t.Errorf("%s processed %d times without any crash", k, v)
		}
	}
	assertDrained(t, client, name)
}

// Processor 不配合取消时 Stop 如实返回超时；任务保留，租约到期后被另一个实例恢复。
func TestRedisStopTimeoutThenRecoveredByAnotherInstance(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("stoprec")
	t.Cleanup(func() { cleanQueue(t, client, name) })

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	m1 := startRedisManager(t, client, name, procFunc(func(context.Context, []byte) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // 忽略 ctx
		return nil
	}), func(c *Config) { c.WorkerCount = 1; c.Timeout = 300 * time.Millisecond })
	if err := m1.AddDataWithID(context.Background(), "stuck", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := m1.Stop(context.Background()); !errors.Is(err, common.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}

	var got atomic.Value
	m2 := startRedisManager(t, client, name, procFunc(func(_ context.Context, d []byte) error { got.Store(string(d)); return nil }), nil)
	eventually(t, "recovered by second instance", func() bool { return got.Load() == "payload" })
	_ = m2.Stop(context.Background())

	// 卡住的旧 Processor 之后“醒来”并返回成功：它的凭证已失效，不能影响结果
	close(release)
	time.Sleep(300 * time.Millisecond)
	assertDrained(t, client, name)
}

// 进程级故障：辅助进程领取任务并阻塞，被 SIGKILL 后由本进程通过 Manager 正式 API 恢复。
func TestHelperClaimAndBlock(t *testing.T) {
	if os.Getenv("TAURUS_HELPER") != "claim" {
		t.Skip("helper process only")
	}
	client := connect(t, os.Getenv("TAURUS_TEST_REDIS_ADDR"))
	name := os.Getenv("TAURUS_HELPER_QUEUE")
	effects := os.Getenv("TAURUS_HELPER_EFFECTS")
	m, err := NewManager(procFunc(func(_ context.Context, d []byte) error {
		// 业务副作用已发生，但确认前进程被杀
		f, _ := os.OpenFile(effects, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "helper:%s\n", d)
		f.Close()
		fmt.Printf("CLAIMED:%s\n", d)
		select {}
	}), redisConfig(client, name, func(c *Config) { c.WorkerCount = 1; c.LeaseDuration = time.Second }))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.AddDataWithID(context.Background(), "crash-job", []byte("important")); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ENQUEUED")
	select {}
}

func TestRedisCrashedConsumerProcessIsRecovered(t *testing.T) {
	addr := testAddr(t)
	client := connect(t, addr)
	name := uniq("crash")
	t.Cleanup(func() { cleanQueue(t, client, name) })
	effects := t.TempDir() + "/effects.log"

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperClaimAndBlock$", "-test.v")
	cmd.Env = append(os.Environ(), "TAURUS_HELPER=claim", "TAURUS_TEST_REDIS_ADDR="+addr, "TAURUS_HELPER_QUEUE="+name, "TAURUS_HELPER_EFFECTS="+effects)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	claimed := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "CLAIMED:important") {
				close(claimed)
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case <-claimed:
	case <-time.After(15 * time.Second):
		t.Fatal("helper process never claimed the task")
	}
	// 领取后、确认前 SIGKILL
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	killed = true
	killedAt := time.Now()

	var recoveredAt atomic.Value
	m := startRedisManager(t, client, name, procFunc(func(_ context.Context, d []byte) error {
		f, _ := os.OpenFile(effects, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "recovery:%s\n", d)
		f.Close()
		recoveredAt.Store(time.Now())
		return nil
	}), nil)
	eventually(t, "task recovered by another process", func() bool { return recoveredAt.Load() != nil })
	if d := recoveredAt.Load().(time.Time).Sub(killedAt); d < 200*time.Millisecond {
		t.Errorf("task redelivered %v after kill, before its lease could expire", d)
	}
	_ = m.Stop(context.Background())
	assertDrained(t, client, name)

	b, _ := os.ReadFile(effects)
	if got := strings.TrimSpace(string(b)); got != "helper:important\nrecovery:important" {
		t.Fatalf("effects (duplicate delivery after crash is expected and counted separately): %q", got)
	}
}

// ---- 断连 / 响应丢失：入队结果不确定，稳定ID重试后只处理一次 ----

type lossyProxy struct {
	ln     net.Listener
	target string
	drop   atomic.Bool  // 丢弃所有连接上的响应
	marker atomic.Value // string：请求中出现该标记的连接，其下一个响应被丢弃（请求已执行）
	mu     sync.Mutex
	conns  []net.Conn
}

func newLossyProxy(t *testing.T, target string) *lossyProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &lossyProxy{ln: ln, target: target}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *lossyProxy) serve(c net.Conn) {
	b, err := net.Dial("tcp", p.target)
	if err != nil {
		c.Close()
		return
	}
	p.mu.Lock()
	p.conns = append(p.conns, c, b)
	p.mu.Unlock()
	var armed atomic.Bool
	go func() {
		rb := make([]byte, 32*1024)
		for {
			n, err := c.Read(rb)
			if n > 0 {
				if m, _ := p.marker.Load().(string); m != "" && strings.Contains(string(rb[:n]), m) {
					armed.Store(true)
				}
				if _, werr := b.Write(rb[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		b.Close()
		c.Close()
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := b.Read(buf)
		if err != nil {
			break
		}
		if p.drop.Load() || armed.Load() { // 请求已到达 Redis 并执行，但响应丢失
			break
		}
		if _, err := c.Write(buf[:n]); err != nil {
			break
		}
	}
	b.Close()
	c.Close()
}

func TestRedisUncertainEnqueueResultWithStableID(t *testing.T) {
	addr := testAddr(t)
	direct := connect(t, addr)
	name := uniq("lossy")
	t.Cleanup(func() { cleanQueue(t, direct, name) })
	proxy := newLossyProxy(t, addr)
	viaProxy := connect(t, proxy.ln.Addr().String())

	var mu sync.Mutex
	counts := map[string]int{}
	m := startRedisManager(t, viaProxy, name, procFunc(func(_ context.Context, d []byte) error {
		mu.Lock()
		counts[string(d)]++
		mu.Unlock()
		return nil
	}), func(c *Config) { c.WorkerCount = 1 })

	ctx := context.Background()
	// 预热：让 Enqueue 脚本进入 Redis 脚本缓存，之后的请求走 EVALSHA 单次往返
	if err := m.AddDataWithID(ctx, "warmup", []byte("warmup")); err != nil {
		t.Fatal(err)
	}
	proxy.marker.Store("order-1")
	err := m.AddDataWithID(ctx, "order-1", []byte("order-payload"))
	if err == nil {
		t.Fatal("expected an error when the response is lost")
	}
	t.Logf("AddDataWithID with lost response returned: %v", err)
	// 结果不确定：命令其实已经执行
	exists := direct.GetClient().Exists(ctx, "taurus:q2:{"+name+"}:m:order-1").Val()
	if exists != 1 {
		keys, _ := direct.GetClient().Keys(ctx, "taurus:q2:{"+name+"}:*").Result()
		t.Logf("DEBUG keys=%v", keys)
		t.Fatalf("the lost-response enqueue should have taken effect on the server, exists=%d", exists)
	}
	proxy.marker.Store("")

	// 调用方用同一ID重试：被识别为重复，不会产生第二个任务
	for i := 0; i < 3; i++ {
		if err := m.AddDataWithID(ctx, "order-1", []byte("order-payload")); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	eventually(t, "processed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return counts["order-payload"] >= 1 && counts["warmup"] >= 1
	})
	time.Sleep(2500 * time.Millisecond) // 超过租约，确认没有第二份
	mu.Lock()
	defer mu.Unlock()
	if counts["order-payload"] != 1 {
		t.Fatalf("processed %d times", counts["order-payload"])
	}
}

// 领取响应丢失（服务端已写入凭证和租约）：任务在租约到期后被恢复，不会丢失。
func TestRedisReceiveResponseLostIsRecoveredAfterLease(t *testing.T) {
	addr := testAddr(t)
	direct := connect(t, addr)
	name := uniq("lostrecv")
	t.Cleanup(func() { cleanQueue(t, direct, name) })
	proxy := newLossyProxy(t, addr)
	viaProxy := connect(t, proxy.ln.Addr().String())

	// 先用直连引擎入队
	eng, err := engine.NewRedisStreamEngine(context.Background(), direct, engine.ReliableOptions{Name: name, LeaseDuration: time.Second, DedupWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Enqueue(context.Background(), "job", []byte("data"), true); err != nil {
		t.Fatal(err)
	}
	lossy, err := engine.NewRedisStreamEngine(context.Background(), viaProxy, engine.ReliableOptions{Name: name, LeaseDuration: time.Second, DedupWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	proxy.drop.Store(true)
	if _, err := lossy.Receive(context.Background()); err == nil {
		t.Fatal("expected error when receive response is lost")
	}
	proxy.drop.Store(false)

	if _, err := eng.Receive(context.Background()); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("task is leased by the lost claim, must not be delivered yet: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	d, err := eng.Receive(context.Background())
	if err != nil || d.ID != "job" || !d.Recovered {
		t.Fatalf("not recovered: %+v %v", d, err)
	}
	_ = eng.Ack(context.Background(), d)
}

// ---- 持久化 / Redis 重启：由 scripts/reliability-integration.sh 分两阶段驱动 ----

func TestPersistenceAcrossRestart(t *testing.T) {
	phase := os.Getenv("TAURUS_PERSIST_PHASE")
	if phase == "" {
		t.Skip("driven by scripts/reliability-integration.sh")
	}
	client := connect(t, testAddr(t))
	const name = "persist-check"
	ctx := context.Background()
	opts := engine.ReliableOptions{Name: name, LeaseDuration: 3 * time.Second, DedupWindow: time.Hour}
	eng, err := engine.NewRedisStreamEngine(ctx, client, opts)
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "seed":
		cleanQueue(t, client, name)
		for _, id := range []string{"p-done", "p-failed", "p-delayed", "p-inflight", "p-ready"} {
			if _, err := eng.Enqueue(ctx, id, []byte("data-"+id), true); err != nil {
				t.Fatal(err)
			}
		}
		take := func(want string) *engine.Delivery {
			d, err := eng.Receive(ctx)
			if err != nil || d.ID != want {
				t.Fatalf("seed receive want %s got %+v err %v", want, d, err)
			}
			return d
		}
		if err := eng.Ack(ctx, take("p-done")); err != nil {
			t.Fatal(err)
		}
		if err := eng.Fail(ctx, take("p-failed"), "seed failure"); err != nil {
			t.Fatal(err)
		}
		if err := eng.Retry(ctx, take("p-delayed"), time.Hour, "seed retry"); err != nil {
			t.Fatal(err)
		}
		take("p-inflight") // 不确认，模拟消费者在 Redis 重启前持有任务
	case "verify":
		defer cleanQueue(t, client, name)
		failed, err := eng.ListFailed(ctx, 10)
		if err != nil || len(failed) != 1 || failed[0].ID != "p-failed" || failed[0].Reason != "seed failure" {
			t.Fatalf("failed retention lost across restart: %+v %v", failed, err)
		}
		if dup, err := eng.Enqueue(ctx, "p-done", []byte("data-p-done"), true); err != nil || !dup {
			t.Fatalf("dedup tombstone lost across restart: dup=%v err=%v", dup, err)
		}
		if n := client.GetClient().ZCard(ctx, "taurus:q2:{"+name+"}:delayed").Val(); n != 1 {
			t.Fatalf("delayed set lost across restart: %d", n)
		}
		d, err := eng.Receive(ctx)
		if err != nil || d.ID != "p-ready" {
			t.Fatalf("ready task lost across restart: %+v %v", d, err)
		}
		_ = eng.Ack(ctx, d)
		// 重启前被持有的任务在租约到期后恢复
		time.Sleep(3500 * time.Millisecond)
		d, err = eng.Receive(ctx)
		if err != nil || d.ID != "p-inflight" || !d.Recovered {
			t.Fatalf("in-flight task not recovered after restart: %+v %v", d, err)
		}
		_ = eng.Ack(ctx, d)
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}

// 长任务由 Manager 自动续租：运行时间是租约的数倍，两个实例都在轮询，任务期间不会被第二次领取。
func TestRedisManagerAutoRenewPreventsEarlyTakeover(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("renew")
	t.Cleanup(func() { cleanQueue(t, client, name) })

	var calls, completed atomic.Int64
	long := procFunc(func(ctx context.Context, _ []byte) error {
		calls.Add(1)
		select {
		case <-time.After(2500 * time.Millisecond): // > 4 倍租约 (600ms)
			completed.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	mod := func(c *Config) { c.WorkerCount = 2; c.LeaseDuration = 600 * time.Millisecond }
	m1 := startRedisManager(t, client, name, long, mod)
	m2 := startRedisManager(t, client, name, long, mod)

	if err := m1.AddDataWithID(context.Background(), "long", []byte("x")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "task claimed", func() bool { return calls.Load() >= 1 })
	eventually(t, "task completed", func() bool { return completed.Load() >= 1 })
	time.Sleep(700 * time.Millisecond) // 再多等一个租约，确认没有迟到的重复领取
	if calls.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("early takeover despite auto-renew: calls=%d completed=%d", calls.Load(), completed.Load())
	}
	_ = m1.Stop(context.Background())
	_ = m2.Stop(context.Background())
	assertDrained(t, client, name)
}

// 合作式 Processor：Stop 超时后收到取消并返回，任务既不失败也不被确认，由另一实例在租约到期后恢复。
func TestRedisCooperativeStopCancelsAndKeepsTask(t *testing.T) {
	client := connect(t, testAddr(t))
	name := uniq("coop")
	t.Cleanup(func() { cleanQueue(t, client, name) })

	entered := make(chan struct{}, 1)
	var cancelled atomic.Bool
	m1 := startRedisManager(t, client, name, procFunc(func(ctx context.Context, _ []byte) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled.Store(true)
		return ctx.Err()
	}), func(c *Config) { c.WorkerCount = 1; c.Timeout = 300 * time.Millisecond })
	if err := m1.AddDataWithID(context.Background(), "coop-job", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := m1.Stop(context.Background()); !errors.Is(err, common.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}
	if !cancelled.Load() {
		t.Fatal("cooperative processor was not cancelled")
	}
	if f, _ := m1.ListFailed(context.Background(), 10); len(f) != 0 {
		t.Fatalf("cancelled task must not be failed: %v", f)
	}
	// 任务仍在 Redis 中且未确认
	if n := client.GetClient().Exists(context.Background(), "taurus:q2:{"+name+"}:m:coop-job").Val(); n != 1 {
		t.Fatalf("task record gone after cancelled stop: %d", n)
	}

	var got atomic.Value
	m2 := startRedisManager(t, client, name, procFunc(func(_ context.Context, d []byte) error { got.Store(string(d)); return nil }), nil)
	eventually(t, "recovered after lease expiry", func() bool { return got.Load() == "payload" })
	_ = m2.Stop(context.Background())
	assertDrained(t, client, name)
}
