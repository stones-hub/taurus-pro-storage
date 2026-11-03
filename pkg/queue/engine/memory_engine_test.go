package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newMem(t *testing.T, mod func(*ReliableOptions)) (*MemoryEngine, *fakeClock) {
	t.Helper()
	o := ReliableOptions{Name: "q", LeaseDuration: time.Second, DedupWindow: time.Hour, Capacity: 100}
	if mod != nil {
		mod(&o)
	}
	e, err := NewMemoryEngine(o)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	e.now = clk.now
	return e, clk
}

func TestMemoryEngineLifecycle(t *testing.T) {
	e, clk := newMem(t, nil)
	ctx := context.Background()
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatal(err)
	}
	if dup, err := e.Enqueue(ctx, "a", []byte("1"), true); err != nil || dup {
		t.Fatal(dup, err)
	}
	if dup, _ := e.Enqueue(ctx, "a", []byte("1"), true); !dup {
		t.Fatal("expected duplicate")
	}
	if _, err := e.Enqueue(ctx, "a", []byte("2"), true); !errors.Is(err, common.ErrIDConflict) {
		t.Fatal(err)
	}
	d, _ := e.Receive(ctx)
	// 租约到期被接管，旧凭证失效
	clk.advance(2 * time.Second)
	d2, err := e.Receive(ctx)
	if err != nil || !d2.Recovered || d2.Attempt != 2 || d2.Token == d.Token {
		t.Fatalf("%+v %v", d2, err)
	}
	if err := e.Ack(ctx, d); !errors.Is(err, common.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := e.Retry(ctx, d2, time.Second, "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Receive(ctx); !errors.Is(err, common.ErrQueueEmpty) {
		t.Fatal("delayed too early")
	}
	clk.advance(1500 * time.Millisecond)
	d3, err := e.Receive(ctx)
	if err != nil || d3.Retries != 1 {
		t.Fatalf("%+v %v", d3, err)
	}
	if err := e.Fail(ctx, d3, "bad"); err != nil {
		t.Fatal(err)
	}
	f, _ := e.ListFailed(ctx, 5)
	if len(f) != 1 || f[0].Reason != "bad" {
		t.Fatal(f)
	}
	if err := e.ReplayFailed(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := e.ReplayFailed(ctx, "a"); !errors.Is(err, common.ErrFailedNotFound) {
		t.Fatal(err)
	}
	d4, _ := e.Receive(ctx)
	if err := e.Ack(ctx, d4); err != nil {
		t.Fatal(err)
	}
	// 去重窗口
	if dup, _ := e.Enqueue(ctx, "a", []byte("1"), true); !dup {
		t.Fatal("within window must dedup")
	}
	clk.advance(2 * time.Hour)
	if dup, _ := e.Enqueue(ctx, "a", []byte("1"), true); dup {
		t.Fatal("after window must be new")
	}
}

func TestMemoryEngineCapacityAndClose(t *testing.T) {
	e, _ := newMem(t, func(o *ReliableOptions) { o.Capacity = 2 })
	ctx := context.Background()
	_, _ = e.Enqueue(ctx, "a", []byte("1"), false)
	_, _ = e.Enqueue(ctx, "b", []byte("1"), false)
	if _, err := e.Enqueue(ctx, "c", []byte("1"), false); !errors.Is(err, common.ErrQueueFull) {
		t.Fatal(err)
	}
	_ = e.Close()
	if _, err := e.Enqueue(ctx, "d", []byte("1"), false); !errors.Is(err, common.ErrEngineClosed) {
		t.Fatal(err)
	}
}
