package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// 这些测试连接显式隔离的 Redis（环境变量 TAURUS_TEST_REDIS_ADDR），未设置时跳过，
// 绝不回落到默认的 localhost:6379。参见 scripts/reliability-integration.sh。
func testClient(t *testing.T) *redisx.RedisClient {
	t.Helper()
	addr := os.Getenv("TAURUS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TAURUS_TEST_REDIS_ADDR not set; run scripts/reliability-integration.sh")
	}
	if err := redisx.InitRedis(redisx.WithAddrs(addr), redisx.WithTimeout(time.Second, 3*time.Second, 3*time.Second), redisx.WithMaxRetries(-1)); err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	return redisx.Redis
}

func uniqueName(t *testing.T) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)
}

func newStreamEngine(t *testing.T, c *redisx.RedisClient, name string, mod func(*ReliableOptions)) *RedisStreamEngine {
	t.Helper()
	opts := ReliableOptions{Name: name, LeaseDuration: time.Second, DedupWindow: time.Hour}
	if mod != nil {
		mod(&opts)
	}
	e, err := NewRedisStreamEngine(context.Background(), c, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupKeys(c, name) })
	return e
}

func cleanupKeys(c *redisx.RedisClient, name string) {
	ctx := context.Background()
	iter := c.GetClient().Scan(ctx, 0, streamKeyPrefix+"{"+name+"}:*", 500).Iterator()
	for iter.Next(ctx) {
		c.GetClient().Del(ctx, iter.Val())
	}
}

func countKeys(t *testing.T, c *redisx.RedisClient, name, kind string) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	iter := c.GetClient().Scan(ctx, 0, streamKeyPrefix+"{"+name+"}:"+kind+"*", 500).Iterator()
	for iter.Next(ctx) {
		n++
	}
	if err := iter.Err(); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustReceive(t *testing.T, e ReliableEngine) *Delivery {
	t.Helper()
	d, err := e.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	return d
}

func TestStreamReceiveAckBasics(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()

	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("empty queue: want ErrQueueEmpty, got %v", err)
	}
	if dup, err := e.Enqueue(ctx, "job-1", []byte("hello"), true); err != nil || dup {
		t.Fatalf("enqueue: dup=%v err=%v", dup, err)
	}
	d := mustReceive(t, e)
	if d.ID != "job-1" || string(d.Data) != "hello" || d.Attempt != 1 || d.Recovered || d.Token == "" {
		t.Fatalf("unexpected delivery %+v", d)
	}
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("claimed task must not be delivered again: %v", err)
	}
	if err := e.Ack(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Ack 对已不存在的任务幂等
	if err := e.Ack(ctx, d); err != nil {
		t.Fatalf("second ack should be idempotent, got %v", err)
	}
	// 成功后：stream/pending/lease/记录全部清理
	if n := c.GetClient().XLen(ctx, e.ready).Val(); n != 0 {
		t.Fatalf("stream length after ack = %d", n)
	}
	if p := c.GetClient().XPending(ctx, e.ready, streamGroup).Val(); p.Count != 0 {
		t.Fatalf("pending after ack = %d", p.Count)
	}
	if n := c.GetClient().ZCard(ctx, e.leases).Val(); n != 0 {
		t.Fatalf("leases after ack = %d", n)
	}
	if n := countKeys(t, c, name, "m:"); n != 0 {
		t.Fatalf("message records after ack = %d", n)
	}
}

func TestStreamEnqueueValidation(t *testing.T) {
	c := testClient(t)
	e := newStreamEngine(t, c, uniqueName(t), nil)
	ctx := context.Background()
	for _, id := range []string{"", "a b", "a{b}", "-x", string(make([]byte, 200))} {
		if _, err := e.Enqueue(ctx, id, []byte("x"), true); !errors.Is(err, common.ErrInvalidID) {
			t.Errorf("id %q: want ErrInvalidID, got %v", id, err)
		}
	}
	if _, err := e.Enqueue(ctx, "ok", nil, true); !errors.Is(err, common.ErrItemEmpty) {
		t.Errorf("empty data: %v", err)
	}
	if _, err := NewRedisStreamEngine(ctx, c, ReliableOptions{Name: "bad name", LeaseDuration: time.Second}); !errors.Is(err, common.ErrInvalidID) {
		t.Errorf("bad queue name: %v", err)
	}
	if _, err := NewRedisStreamEngine(ctx, c, ReliableOptions{Name: "q", LeaseDuration: time.Millisecond}); err == nil {
		t.Error("tiny lease must be rejected")
	}
	if _, err := NewRedisStreamEngine(ctx, nil, ReliableOptions{Name: "q", LeaseDuration: time.Second}); err == nil {
		t.Error("nil client must be rejected")
	}
}

func TestStreamLeaseTakeoverInvalidatesOldToken(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e1 := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
	e2 := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
	ctx := context.Background()

	if _, err := e1.Enqueue(ctx, "job", []byte("payload"), true); err != nil {
		t.Fatal(err)
	}
	old := mustReceive(t, e1)

	// 租约未到期，其它实例领不到
	if _, err := e2.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("live lease must not be taken over: %v", err)
	}
	// 续期延长租约
	time.Sleep(400 * time.Millisecond)
	if err := e1.Renew(ctx, old); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := e2.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("renewed lease must not be taken over: %v", err)
	}

	time.Sleep(800 * time.Millisecond) // 不再续期，租约到期
	nw := mustReceive(t, e2)
	if !nw.Recovered || nw.Attempt != 2 || nw.Token == old.Token || nw.ID != "job" || string(nw.Data) != "payload" {
		t.Fatalf("takeover delivery %+v (old %+v)", nw, old)
	}

	// 旧凭证的一切操作都不能影响新领取
	if err := e1.Ack(ctx, old); !errors.Is(err, common.ErrLeaseLost) {
		t.Errorf("old ack: %v", err)
	}
	if err := e1.Renew(ctx, old); !errors.Is(err, common.ErrLeaseLost) {
		t.Errorf("old renew: %v", err)
	}
	if err := e1.Retry(ctx, old, 0, "x"); !errors.Is(err, common.ErrLeaseLost) {
		t.Errorf("old retry: %v", err)
	}
	if err := e1.Fail(ctx, old, "x"); !errors.Is(err, common.ErrLeaseLost) {
		t.Errorf("old fail: %v", err)
	}
	if err := e2.Renew(ctx, nw); err != nil {
		t.Errorf("new holder renew: %v", err)
	}
	if err := e2.Ack(ctx, nw); err != nil {
		t.Errorf("new holder ack: %v", err)
	}
	if p := c.GetClient().XPending(ctx, e1.ready, streamGroup).Val(); p.Count != 0 {
		t.Errorf("pending left: %d", p.Count)
	}
}

func TestStreamRetryFailReplay(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()

	if _, err := e.Enqueue(ctx, "job", []byte("p"), true); err != nil {
		t.Fatal(err)
	}
	d := mustReceive(t, e)
	if err := e.Retry(ctx, d, 700*time.Millisecond, "boom"); err != nil {
		t.Fatal(err)
	}
	if p := c.GetClient().XPending(ctx, e.ready, streamGroup).Val(); p.Count != 0 {
		t.Fatalf("pending after retry transfer: %d", p.Count)
	}
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("delayed task delivered early: %v", err)
	}
	time.Sleep(900 * time.Millisecond)
	d2 := mustReceive(t, e)
	if d2.Retries != 1 || d2.Attempt != 2 || d2.Recovered {
		t.Fatalf("after retry: %+v", d2)
	}
	if err := e.Fail(ctx, d2, "permanent"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatalf("failed task must not be redelivered: %v", err)
	}

	failed, err := e.ListFailed(ctx, 10)
	if err != nil || len(failed) != 1 {
		t.Fatalf("list failed: %v %v", failed, err)
	}
	f := failed[0]
	if f.ID != "job" || string(f.Data) != "p" || f.Reason != "permanent" || f.Attempts != 2 || time.Since(f.FailedAt) > time.Minute {
		t.Fatalf("failed record %+v", f)
	}
	if _, err := e.ListFailed(ctx, 0); err == nil {
		t.Error("limit 0 must be rejected")
	}
	if _, err := e.ListFailed(ctx, MaxListFailed+1); err == nil {
		t.Error("limit above max must be rejected")
	}
	// 失败任务仍占用该ID
	if dup, err := e.Enqueue(ctx, "job", []byte("p"), true); err != nil || !dup {
		t.Errorf("failed task id must dedup: dup=%v err=%v", dup, err)
	}
	if _, err := e.Enqueue(ctx, "job", []byte("other"), true); !errors.Is(err, common.ErrIDConflict) {
		t.Errorf("different payload on failed id: %v", err)
	}

	if err := e.ReplayFailed(ctx, "nope"); !errors.Is(err, common.ErrFailedNotFound) {
		t.Errorf("replay unknown: %v", err)
	}
	if err := e.ReplayFailed(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if err := e.ReplayFailed(ctx, "job"); !errors.Is(err, common.ErrFailedNotFound) {
		t.Errorf("second replay must fail, not duplicate: %v", err)
	}
	d3 := mustReceive(t, e)
	if d3.ID != "job" || d3.Retries != 0 {
		t.Fatalf("replayed delivery %+v", d3)
	}
	if err := e.Ack(ctx, d3); err != nil {
		t.Fatal(err)
	}
	if left, _ := e.ListFailed(ctx, 10); len(left) != 0 {
		t.Errorf("failed left after replay+ack: %v", left)
	}
}

func TestStreamReplayFailureKeepsOriginal(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "job", []byte("p"), true)
	d := mustReceive(t, e)
	if err := e.Fail(ctx, d, "x"); err != nil {
		t.Fatal(err)
	}
	// 让 ready 变成错误类型，重放必须在写入前失败并保留原记录
	c.GetClient().Del(ctx, e.ready)
	c.GetClient().Set(ctx, e.ready, "not-a-stream", 0)
	if err := e.ReplayFailed(ctx, "job"); err == nil {
		t.Fatal("replay into corrupted ready key must fail")
	}
	failed, err := e.ListFailed(ctx, 10)
	if err != nil || len(failed) != 1 || failed[0].ID != "job" {
		t.Fatalf("original must be preserved after failed replay: %v %v", failed, err)
	}
	if st := c.GetClient().HGet(ctx, e.msgPrefix+"job", "s").Val(); st != "failed" {
		t.Fatalf("state changed to %q", st)
	}
}

func TestStreamWrongTypeRejectedBeforeWrites(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()
	c.GetClient().Set(ctx, e.ready, "oops", 0)
	if _, err := e.Enqueue(ctx, "job", []byte("p"), true); err == nil {
		t.Fatal("expected wrongtype error")
	}
	if n := countKeys(t, c, name, "m:"); n != 0 {
		t.Fatalf("partial write: %d message records created", n)
	}
	if n := countKeys(t, c, name, "d:"); n != 0 {
		t.Fatalf("partial write: %d dedup records created", n)
	}
}

func TestStreamDedupWindowAndUnfinished(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.DedupWindow = 600 * time.Millisecond })
	ctx := context.Background()

	// 未完成的任务不受去重窗口影响
	if _, err := e.Enqueue(ctx, "pending", []byte("a"), true); err != nil {
		t.Fatal(err)
	}
	// 已完成的任务在窗口内去重
	if _, err := e.Enqueue(ctx, "done", []byte("a"), true); err != nil {
		t.Fatal(err)
	}
	var doneDelivery *Delivery
	for i := 0; i < 2; i++ {
		d := mustReceive(t, e)
		if d.ID == "done" {
			doneDelivery = d
		}
	}
	if err := e.Ack(ctx, doneDelivery); err != nil {
		t.Fatal(err)
	}
	if dup, err := e.Enqueue(ctx, "done", []byte("a"), true); err != nil || !dup {
		t.Fatalf("within window: dup=%v err=%v", dup, err)
	}
	if _, err := e.Enqueue(ctx, "done", []byte("b"), true); !errors.Is(err, common.ErrIDConflict) {
		t.Fatalf("within window different payload: %v", err)
	}

	time.Sleep(900 * time.Millisecond)
	if dup, err := e.Enqueue(ctx, "done", []byte("b"), true); err != nil || dup {
		t.Fatalf("after window id is new: dup=%v err=%v", dup, err)
	}
	// 窗口早已过期，但未完成的任务仍然去重 / 冲突，且没被覆盖
	if dup, err := e.Enqueue(ctx, "pending", []byte("a"), true); err != nil || !dup {
		t.Fatalf("unfinished task after window: dup=%v err=%v", dup, err)
	}
	if _, err := e.Enqueue(ctx, "pending", []byte("zzz"), true); !errors.Is(err, common.ErrIDConflict) {
		t.Fatalf("unfinished task overwritten after window: %v", err)
	}
	if got := c.GetClient().HGet(ctx, e.msgPrefix+"pending", "d").Val(); got != "a" {
		t.Fatalf("payload overwritten: %q", got)
	}
	// 无ID去重的任务不产生墓碑（有界）
	if _, err := e.Enqueue(ctx, "auto-1", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	d := mustReceive(t, e)
	for d.ID != "auto-1" {
		d = mustReceive(t, e)
	}
	_ = e.Ack(ctx, d)
	if n := countKeys(t, c, name, "d:auto-1"); n != 0 {
		t.Fatalf("tombstone for non-dedup task: %d", n)
	}
}

func TestStreamFailedRetentionPrunes(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.FailedRetention = 500 * time.Millisecond })
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "old", []byte("1"), true)
	_ = e.Fail(ctx, mustReceive(t, e), "x")
	time.Sleep(700 * time.Millisecond)
	_, _ = e.Enqueue(ctx, "new", []byte("2"), true)
	_ = e.Fail(ctx, mustReceive(t, e), "y") // 触发清理
	failed, _ := e.ListFailed(ctx, 10)
	if len(failed) != 1 || failed[0].ID != "new" {
		t.Fatalf("retention prune: %+v", failed)
	}
	if n := countKeys(t, c, name, "m:old"); n != 0 {
		t.Fatalf("pruned record still present")
	}
}

// 多个引擎实例并发领取：每个任务恰好被领取一次，没有遗漏。
func TestStreamConcurrentConsumersExactlyOnceClaim(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	const total, consumers = 300, 8
	engines := make([]*RedisStreamEngine, consumers)
	for i := range engines {
		engines[i] = newStreamEngine(t, c, name, func(o *ReliableOptions) { o.LeaseDuration = 10 * time.Second })
	}
	ctx := context.Background()
	for i := 0; i < total; i++ {
		if _, err := engines[0].Enqueue(ctx, fmt.Sprintf("j%d", i), []byte("x"), true); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Add(1)
		go func(e *RedisStreamEngine) {
			defer wg.Done()
			for {
				d, err := e.Receive(ctx)
				if errors.Is(err, common.ErrQueueEmpty) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seen[d.ID]++
				mu.Unlock()
				if err := e.Ack(ctx, d); err != nil {
					t.Error(err)
				}
			}
		}(e)
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("claimed %d of %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task %s claimed %d times", id, n)
		}
	}
	if p := c.GetClient().XPending(ctx, engines[0].ready, streamGroup).Val(); p.Count != 0 {
		t.Fatalf("pending left: %d", p.Count)
	}
	if n := c.GetClient().XLen(ctx, engines[0].ready).Val(); n != 0 {
		t.Fatalf("stream not cleaned: %d", n)
	}
}

// 恢复扫描有界且不会永远停在首页：大量租约同时到期时，全部都能被依次恢复。
func TestStreamRecoveryNotStuckOnFirstPage(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
	ctx := context.Background()
	const total = 60 // > 单脚本一次最多扫描的 20 个候选
	for i := 0; i < total; i++ {
		_, _ = e.Enqueue(ctx, fmt.Sprintf("j%d", i), []byte("x"), true)
	}
	for i := 0; i < total; i++ {
		mustReceive(t, e) // 全部领取后“崩溃”：不确认
	}
	time.Sleep(800 * time.Millisecond)
	seen := map[string]bool{}
	for i := 0; i < total; i++ {
		d := mustReceive(t, e)
		if !d.Recovered || d.Attempt != 2 {
			t.Fatalf("expected recovered attempt 2: %+v", d)
		}
		seen[d.ID] = true
		if err := e.Ack(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != total {
		t.Fatalf("recovered %d of %d", len(seen), total)
	}
}

func TestStreamRejectsClusterClient(t *testing.T) {
	cc := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}})
	defer cc.Close()
	err := checkTopology(context.Background(), cc)
	if !errors.Is(err, common.ErrUnsupportedTopology) {
		t.Fatalf("cluster client must be rejected, got %v", err)
	}
}

// ---- 损坏记录：显式报错、保留现场、不留下不可恢复孤儿 ----

type queueState struct {
	hash                          map[string]string
	xlen, pending, leases, delays int64
	failed                        int64
}

func snapshotState(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) queueState {
	t.Helper()
	ctx := context.Background()
	rdb := c.GetClient()
	st := queueState{hash: rdb.HGetAll(ctx, e.msgPrefix+id).Val()}
	st.xlen = rdb.XLen(ctx, e.ready).Val()
	if p, err := rdb.XPending(ctx, e.ready, streamGroup).Result(); err == nil {
		st.pending = p.Count
	}
	st.leases = rdb.ZCard(ctx, e.leases).Val()
	st.delays = rdb.ZCard(ctx, e.delayed).Val()
	st.failed = rdb.ZCard(ctx, e.failed).Val()
	return st
}

func assertSame(t *testing.T, what string, before, after queueState) {
	t.Helper()
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("%s: state changed\nbefore=%+v\nafter =%+v", what, before, after)
	}
}

func TestStreamReceiveCorruptHeadRecordKeepsScene(t *testing.T) {
	cases := map[string]func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string){
		"invalid state, complete payload": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().HSet(context.Background(), e.msgPrefix+id, "s", "bogus")
		},
		"missing state": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().HDel(context.Background(), e.msgPrefix+id, "s")
		},
		"malformed n": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().HSet(context.Background(), e.msgPrefix+id, "n", "abc")
		},
		"malformed r": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().HSet(context.Background(), e.msgPrefix+id, "r", "-1")
		},
		"missing payload": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().HDel(context.Background(), e.msgPrefix+id, "d")
		},
		"wrong key type": func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine, id string) {
			c.GetClient().Del(context.Background(), e.msgPrefix+id)
			c.GetClient().Set(context.Background(), e.msgPrefix+id, "str", 0)
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			c := testClient(t)
			qname := uniqueName(t)
			e := newStreamEngine(t, c, qname, nil)
			ctx := context.Background()
			if _, err := e.Enqueue(ctx, "job", []byte("precious-payload"), true); err != nil {
				t.Fatal(err)
			}
			corrupt(t, c, e, "job")
			before := snapshotState(t, c, e, "job")
			// 反复领取都应该显式报错，且现场不变（尤其不会 XACK/XDEL/进入 PEL）
			for i := 0; i < 3; i++ {
				_, err := e.Receive(ctx)
				if err == nil || errors.Is(err, common.ErrQueueEmpty) {
					t.Fatalf("attempt %d: want explicit error, got %v", i, err)
				}
				assertSame(t, "receive on corrupt record", before, snapshotState(t, c, e, "job"))
			}
			if before.xlen != 1 || before.pending != 0 || before.leases != 0 {
				t.Fatalf("unexpected precondition %+v", before)
			}
			if name != "wrong key type" && name != "missing payload" {
				if got := c.GetClient().HGet(ctx, e.msgPrefix+"job", "d").Val(); got != "precious-payload" {
					t.Fatalf("payload lost: %q", got)
				}
			}
			// 人工修复后自然恢复
			c.GetClient().Del(ctx, e.msgPrefix+"job")
			c.GetClient().HSet(ctx, e.msgPrefix+"job", "s", "ready", "d", "precious-payload", "n", 0, "r", 0, "h", contentHash([]byte("precious-payload")), "dd", "1")
			d := mustReceive(t, e)
			if d.ID != "job" || string(d.Data) != "precious-payload" {
				t.Fatalf("after repair: %+v", d)
			}
		})
	}
	// 类型错误的 error 可识别为损坏记录或 WRONGTYPE，而非 ErrQueueEmpty
	c := testClient(t)
	e := newStreamEngine(t, c, uniqueName(t), nil)
	_, _ = e.Enqueue(context.Background(), "j", []byte("x"), true)
	c.GetClient().HSet(context.Background(), e.msgPrefix+"j", "n", "abc")
	if _, err := e.Receive(context.Background()); !errors.Is(err, common.ErrCorruptRecord) {
		t.Fatalf("want ErrCorruptRecord, got %v", err)
	}
}

func TestStreamReceiveCorruptLeaseCandidateKeepsScene(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
	d := mustReceive(t, e)
	c.GetClient().HSet(ctx, e.msgPrefix+"job", "n", "x")
	time.Sleep(800 * time.Millisecond)
	before := snapshotState(t, c, e, "job")
	if before.leases != 1 || before.pending != 1 {
		t.Fatalf("precondition %+v", before)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.Receive(ctx); !errors.Is(err, common.ErrCorruptRecord) {
			t.Fatalf("want ErrCorruptRecord, got %v", err)
		}
		assertSame(t, "corrupt expired lease", before, snapshotState(t, c, e, "job"))
	}
	if err := e.Renew(ctx, d); err != nil { // 原持有者的凭证仍有效，没有被抛弃
		t.Fatalf("original token must survive: %v", err)
	}
}

func TestStreamReceiveCorruptDelayedRecordKeepsScene(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
	d := mustReceive(t, e)
	if err := e.Retry(ctx, d, 0, "r"); err != nil {
		t.Fatal(err)
	}
	c.GetClient().HSet(ctx, e.msgPrefix+"job", "s", "bogus")
	before := snapshotState(t, c, e, "job")
	if before.delays != 1 {
		t.Fatalf("precondition %+v", before)
	}
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrCorruptRecord) {
		t.Fatalf("got %v", err)
	}
	assertSame(t, "corrupt delayed record", before, snapshotState(t, c, e, "job"))
}

func TestStreamRetryFailReplayPreValidateBeforeWrites(t *testing.T) {
	c := testClient(t)
	name := uniqueName(t)
	e := newStreamEngine(t, c, name, nil)
	ctx := context.Background()

	// Retry：r 畸形 -> 报错，记录、PEL、租约全部不变
	_, _ = e.Enqueue(ctx, "a", []byte("pa"), true)
	d := mustReceive(t, e)
	c.GetClient().HSet(ctx, e.msgPrefix+"a", "r", "oops")
	before := snapshotState(t, c, e, "a")
	if err := e.Retry(ctx, d, time.Second, "x"); !errors.Is(err, common.ErrCorruptRecord) {
		t.Fatalf("retry with malformed r: %v", err)
	}
	assertSame(t, "retry pre-validation", before, snapshotState(t, c, e, "a"))
	if before.pending != 1 || before.leases != 1 || before.delays != 0 {
		t.Fatalf("precondition %+v", before)
	}
	if got := c.GetClient().HGet(ctx, e.msgPrefix+"a", "d").Val(); got != "pa" {
		t.Fatalf("payload lost: %q", got)
	}
	// 修复后同一凭证仍可完成
	c.GetClient().HSet(ctx, e.msgPrefix+"a", "r", "0")
	if err := e.Retry(ctx, d, time.Second, "x"); err != nil {
		t.Fatalf("retry after repair: %v", err)
	}

	// Fail：failed 索引类型错误 -> 报错，记录不变
	_, _ = e.Enqueue(ctx, "b", []byte("pb"), true)
	var db *Delivery
	for db == nil || db.ID != "b" {
		db = mustReceive(t, e)
	}
	c.GetClient().Del(ctx, e.failed)
	c.GetClient().Set(ctx, e.failed, "str", 0)
	before = snapshotState(t, c, e, "b")
	before.failed = 0 // failed 键是字符串，ZCard 会报错
	if err := e.Fail(ctx, db, "x"); err == nil {
		t.Fatal("fail into wrong-type failed key must error")
	}
	after := snapshotState(t, c, e, "b")
	after.failed = 0
	assertSame(t, "fail pre-validation", before, after)
	if st := c.GetClient().HGet(ctx, e.msgPrefix+"b", "s").Val(); st != "inflight" {
		t.Fatalf("state %q", st)
	}
	c.GetClient().Del(ctx, e.failed)
	if err := e.Fail(ctx, db, "x"); err != nil {
		t.Fatalf("fail after repair: %v", err)
	}

	// Replay：rp 畸形 -> 报错，仍为 failed
	c.GetClient().HSet(ctx, e.msgPrefix+"b", "rp", "zzz")
	before = snapshotState(t, c, e, "b")
	if err := e.ReplayFailed(ctx, "b"); !errors.Is(err, common.ErrCorruptRecord) {
		t.Fatalf("replay with malformed rp: %v", err)
	}
	assertSame(t, "replay pre-validation", before, snapshotState(t, c, e, "b"))
	if st := c.GetClient().HGet(ctx, e.msgPrefix+"b", "s").Val(); st != "failed" {
		t.Fatalf("state %q", st)
	}
}

// 记录已不存在的入口没有 payload 可保护：清理后继续服务后面的任务，不阻塞队列。
func TestStreamReceiveSkipsEntryWithoutRecord(t *testing.T) {
	c := testClient(t)
	e := newStreamEngine(t, c, uniqueName(t), nil)
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "gone", []byte("x"), true)
	_, _ = e.Enqueue(ctx, "next", []byte("y"), true)
	c.GetClient().Del(ctx, e.msgPrefix+"gone")
	d := mustReceive(t, e)
	if d.ID != "next" {
		t.Fatalf("got %+v", d)
	}
	if p := c.GetClient().XPending(ctx, e.ready, streamGroup).Val(); p.Count != 1 {
		t.Fatalf("pending %d", p.Count)
	}
}

// ---- HINCRBY 前置校验：规范 int64 字符串，+1 的字段须 <= max-1 ----

const (
	i64Max      = "9223372036854775807"
	i64MaxMinus = "9223372036854775806"
)

// Redis HINCRBY 拒绝（或会溢出）的值，在任何相关写入之前必须被预检拦下
var malformedCounters = []string{"007", "00", "99999999999999999999", "9223372036854775808", "-1", "1.5", " 1", "+1", ""}

type counterScene struct {
	name string
	// 构造场景，返回执行被测操作的函数以及被测字段所在记录ID
	setup func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (op func() error, id string)
	field string
	// 该字段在此路径上是否会被 +1；false 时 max 是允许值
	incremented bool
}

func counterScenes() []counterScene {
	ctx := context.Background()
	claim := func(field string) func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (func() error, string) {
		return func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (func() error, string) {
			_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
			return func() error { _, err := e.Receive(ctx); return err }, "job"
		}
	}
	recoverScene := func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (func() error, string) {
		_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
		mustReceive(t, e)
		time.Sleep(700 * time.Millisecond) // 租约 600ms
		return func() error { _, err := e.Receive(ctx); return err }, "job"
	}
	retryScene := func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (func() error, string) {
		_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
		d := mustReceive(t, e)
		return func() error { return e.Retry(ctx, d, time.Hour, "x") }, "job"
	}
	replayScene := func(t *testing.T, c *redisx.RedisClient, e *RedisStreamEngine) (func() error, string) {
		_, _ = e.Enqueue(ctx, "job", []byte("payload"), true)
		if err := e.Fail(ctx, mustReceive(t, e), "x"); err != nil {
			t.Fatal(err)
		}
		return func() error { return e.ReplayFailed(ctx, "job") }, "job"
	}
	return []counterScene{
		{"claim n", claim("n"), "n", true},
		{"claim r", claim("r"), "r", false},
		{"recover n", recoverScene, "n", true},
		{"recover r", recoverScene, "r", false},
		{"retry r", retryScene, "r", true},
		{"retry n", retryScene, "n", false},
		{"replay rp", replayScene, "rp", true},
		{"replay n", replayScene, "n", false},
		{"replay r", replayScene, "r", false},
	}
}

func TestStreamCounterPrecheckRejectsBeforeAnyWrite(t *testing.T) {
	for _, sc := range counterScenes() {
		for _, bad := range malformedCounters {
			bads := bad
			if sc.incremented {
				// 会被 +1 的字段：int64 最大值同样必须被拒绝
				bads = bad
			}
			t.Run(fmt.Sprintf("%s=%q", sc.name, bads), func(t *testing.T) {
				c := testClient(t)
				e := newStreamEngine(t, c, uniqueName(t), func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
				op, id := sc.setup(t, c, e)
				ctx := context.Background()
				c.GetClient().HSet(ctx, e.msgPrefix+id, sc.field, bads)
				before := snapshotState(t, c, e, id)
				for i := 0; i < 2; i++ {
					if err := op(); !errors.Is(err, common.ErrCorruptRecord) {
						t.Fatalf("want ErrCorruptRecord, got %v", err)
					}
					assertSame(t, "precheck must not write", before, snapshotState(t, c, e, id))
				}
				// 修复后正常恢复
				c.GetClient().HSet(ctx, e.msgPrefix+id, sc.field, "0")
				if err := op(); err != nil {
					t.Fatalf("after repair: %v", err)
				}
			})
		}
	}
}

func TestStreamCounterBoundaries(t *testing.T) {
	for _, sc := range counterScenes() {
		sc := sc
		if sc.incremented {
			t.Run(sc.name+" max is rejected", func(t *testing.T) {
				c := testClient(t)
				e := newStreamEngine(t, c, uniqueName(t), func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
				op, id := sc.setup(t, c, e)
				c.GetClient().HSet(context.Background(), e.msgPrefix+id, sc.field, i64Max)
				before := snapshotState(t, c, e, id)
				if err := op(); !errors.Is(err, common.ErrCorruptRecord) {
					t.Fatalf("max on incremented field must be rejected before writes, got %v", err)
				}
				assertSame(t, "max precheck", before, snapshotState(t, c, e, id))
			})
			t.Run(sc.name+" max-1 is incremented to max", func(t *testing.T) {
				c := testClient(t)
				e := newStreamEngine(t, c, uniqueName(t), func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
				op, id := sc.setup(t, c, e)
				c.GetClient().HSet(context.Background(), e.msgPrefix+id, sc.field, i64MaxMinus)
				if err := op(); err != nil {
					t.Fatalf("critical incrementable value must work: %v", err)
				}
				if got := c.GetClient().HGet(context.Background(), e.msgPrefix+id, sc.field).Val(); got != i64Max {
					t.Fatalf("field %s = %q, want %s", sc.field, got, i64Max)
				}
			})
		} else {
			t.Run(sc.name+" max is allowed (not incremented)", func(t *testing.T) {
				c := testClient(t)
				e := newStreamEngine(t, c, uniqueName(t), func(o *ReliableOptions) { o.LeaseDuration = 600 * time.Millisecond })
				op, id := sc.setup(t, c, e)
				c.GetClient().HSet(context.Background(), e.msgPrefix+id, sc.field, i64Max)
				if err := op(); err != nil {
					t.Fatalf("non-incremented field may be int64 max: %v", err)
				}
				if got := c.GetClient().HGet(context.Background(), e.msgPrefix+id, sc.field).Val(); sc.name != "retry r" && got != i64Max && sc.field != "r" {
					t.Fatalf("field %s changed to %q", sc.field, got)
				}
			})
		}
	}
}
