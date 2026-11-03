package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
)

type memState int

const (
	memReady memState = iota
	memInflight
	memDelayed
	memFailed
)

type memMsg struct {
	id         string
	data       []byte
	hash       string
	dedup      bool
	state      memState
	token      string
	deliveries int
	retries    int
	leaseUntil time.Time
	due        time.Time
	reason     string
	failedAt   time.Time
}

type memTombstone struct {
	hash  string
	until time.Time
}

// MemoryEngine 易失的进程内 ReliableEngine，语义与 RedisStreamEngine 一致
// （凭证、租约、延迟重试、失败留存、稳定ID去重），但进程退出后全部丢失，不提供崩溃恢复。
type MemoryEngine struct {
	mu     sync.Mutex
	opts   ReliableOptions
	msgs   map[string]*memMsg
	ready  []string // FIFO，元素为任务ID
	done   map[string]memTombstone
	closed bool
	now    func() time.Time
}

// NewMemoryEngine 创建内存引擎
func NewMemoryEngine(opts ReliableOptions) (*MemoryEngine, error) {
	if opts.LeaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive, got %v", opts.LeaseDuration)
	}
	if opts.Capacity < 0 || opts.DedupWindow < 0 || opts.FailedRetention < 0 {
		return nil, fmt.Errorf("capacity, dedup window and failed retention cannot be negative")
	}
	return &MemoryEngine{
		opts: opts,
		msgs: make(map[string]*memMsg),
		done: make(map[string]memTombstone),
		now:  time.Now,
	}, nil
}

// pruneLocked 清理过期的去重墓碑与失败留存
func (e *MemoryEngine) pruneLocked(now time.Time) {
	for id, t := range e.done {
		if !now.Before(t.until) {
			delete(e.done, id)
		}
	}
	if e.opts.FailedRetention > 0 {
		for id, m := range e.msgs {
			if m.state == memFailed && now.Sub(m.failedAt) >= e.opts.FailedRetention {
				delete(e.msgs, id)
			}
		}
	}
}

// Enqueue 实现 ReliableEngine
func (e *MemoryEngine) Enqueue(ctx context.Context, id string, data []byte, dedup bool) (bool, error) {
	if err := ValidateID(id); err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, common.ErrItemEmpty
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	h := contentHash(data)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false, common.ErrEngineClosed
	}
	now := e.now()
	e.pruneLocked(now)
	if m, ok := e.msgs[id]; ok {
		if m.hash == h {
			return true, nil
		}
		return false, fmt.Errorf("%w: id=%s", common.ErrIDConflict, id)
	}
	if dedup {
		if t, ok := e.done[id]; ok {
			if t.hash == h {
				return true, nil
			}
			return false, fmt.Errorf("%w: id=%s", common.ErrIDConflict, id)
		}
	}
	if e.opts.Capacity > 0 && len(e.msgs) >= e.opts.Capacity {
		return false, common.ErrQueueFull
	}
	e.msgs[id] = &memMsg{id: id, data: append([]byte(nil), data...), hash: h, dedup: dedup, state: memReady}
	e.ready = append(e.ready, id)
	return false, nil
}

// Receive 实现 ReliableEngine
func (e *MemoryEngine) Receive(ctx context.Context) (*Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, common.ErrEngineClosed
	}
	now := e.now()
	e.pruneLocked(now)

	for id, m := range e.msgs {
		if m.state == memDelayed && !now.Before(m.due) {
			m.state = memReady
			e.ready = append(e.ready, id)
		}
	}
	// 先恢复租约到期的任务（最早到期优先）
	var expired *memMsg
	for _, m := range e.msgs {
		if m.state == memInflight && !now.Before(m.leaseUntil) && (expired == nil || m.leaseUntil.Before(expired.leaseUntil)) {
			expired = m
		}
	}
	if expired != nil {
		return e.claimLocked(expired, now, true), nil
	}
	for len(e.ready) > 0 {
		id := e.ready[0]
		e.ready = e.ready[1:]
		if m, ok := e.msgs[id]; ok && m.state == memReady {
			return e.claimLocked(m, now, false), nil
		}
	}
	return nil, common.ErrQueueEmpty
}

func (e *MemoryEngine) claimLocked(m *memMsg, now time.Time, recovered bool) *Delivery {
	m.state = memInflight
	m.token = newToken()
	m.deliveries++
	m.leaseUntil = now.Add(e.opts.LeaseDuration)
	return &Delivery{ID: m.id, Data: append([]byte(nil), m.data...), Token: m.token, Attempt: m.deliveries, Retries: m.retries, Recovered: recovered}
}

// current 返回仍持有有效凭证的任务；调用方需持锁
func (e *MemoryEngine) current(d *Delivery) *memMsg {
	m, ok := e.msgs[d.ID]
	if !ok || m.state != memInflight || m.token != d.Token {
		return nil
	}
	return m
}

// Ack 实现 ReliableEngine
func (e *MemoryEngine) Ack(ctx context.Context, d *Delivery) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.msgs[d.ID]
	if !ok {
		return nil
	}
	if e.current(d) == nil {
		return common.ErrLeaseLost
	}
	if m.dedup && e.opts.DedupWindow > 0 {
		e.done[m.id] = memTombstone{hash: m.hash, until: e.now().Add(e.opts.DedupWindow)}
	}
	delete(e.msgs, d.ID)
	return nil
}

// Renew 实现 ReliableEngine
func (e *MemoryEngine) Renew(ctx context.Context, d *Delivery) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.current(d)
	if m == nil {
		return common.ErrLeaseLost
	}
	m.leaseUntil = e.now().Add(e.opts.LeaseDuration)
	return nil
}

// Retry 实现 ReliableEngine
func (e *MemoryEngine) Retry(ctx context.Context, d *Delivery, delay time.Duration, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.current(d)
	if m == nil {
		return common.ErrLeaseLost
	}
	if delay < 0 {
		delay = 0
	}
	m.state, m.token, m.reason = memDelayed, "", reason
	m.retries++
	m.due = e.now().Add(delay)
	return nil
}

// Fail 实现 ReliableEngine
func (e *MemoryEngine) Fail(ctx context.Context, d *Delivery, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.current(d)
	if m == nil {
		return common.ErrLeaseLost
	}
	m.state, m.token, m.reason = memFailed, "", reason
	m.failedAt = e.now()
	return nil
}

// ListFailed 实现 ReliableEngine
func (e *MemoryEngine) ListFailed(ctx context.Context, limit int) ([]FailedMessage, error) {
	if limit < 1 || limit > MaxListFailed {
		return nil, fmt.Errorf("limit must be in [1, %d], got %d", MaxListFailed, limit)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var all []*memMsg
	for _, m := range e.msgs {
		if m.state == memFailed {
			all = append(all, m)
		}
	}
	// 按失败时间升序（与 Redis 引擎一致），选择最早的 limit 条
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && (all[j].failedAt.Before(all[j-1].failedAt) ||
			(all[j].failedAt.Equal(all[j-1].failedAt) && all[j].id < all[j-1].id)); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]FailedMessage, 0, len(all))
	for _, m := range all {
		out = append(out, FailedMessage{ID: m.id, Data: append([]byte(nil), m.data...), Attempts: m.deliveries, Reason: m.reason, FailedAt: m.failedAt})
	}
	return out, nil
}

// ReplayFailed 实现 ReliableEngine
func (e *MemoryEngine) ReplayFailed(ctx context.Context, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.msgs[id]
	if !ok || m.state != memFailed {
		return common.ErrFailedNotFound
	}
	m.state, m.retries = memReady, 0
	e.ready = append(e.ready, id)
	return nil
}

// Close 实现 ReliableEngine
func (e *MemoryEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}
