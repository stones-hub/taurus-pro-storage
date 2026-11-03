package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// Status 表示管理器状态
type Status int32

const (
	StatusStopped Status = iota
	StatusRunning
	StatusStopping
)

const (
	// settleTimeout Ack/Retry/Fail/Renew 使用独立的限时 ctx，不受 Stop 取消影响
	settleTimeout = 5 * time.Second
	// stopGrace 到达排空期限并取消 Processor ctx 后，再等待 Processor 自行退出的时间
	stopGrace = 2 * time.Second
	// maxReasonLen 失败/重试原因的最大长度
	maxReasonLen = 1024
)

// FailedMessage 失败留存中的任务
type FailedMessage = engine.FailedMessage

// Manager 队列管理器
//
// 投递语义：至少一次。Redis 引擎下，AddData 返回 nil 只表示任务已被 Redis 接收，
// 不表示业务已完成，也不表示 Redis 本身不会丢数据（取决于部署的持久化/复制配置）；
// Processor 可能因崩溃恢复、租约到期被接管等原因重复收到同一任务，必须幂等。
// Manager 为单次生命周期：Stop 之后不能再次 Start。
type Manager struct {
	eng       engine.ReliableEngine
	processor Processor
	config    *Config

	mu     sync.RWMutex // 保护 status/used 及 context 字段
	status Status
	used   bool

	claimCtx    context.Context // 取消后停止领取新任务
	claimCancel context.CancelFunc
	procCtx     context.Context // 传给 Processor；仅在排空超时后取消
	procCancel  context.CancelFunc
	wg          sync.WaitGroup

	inflight atomic.Int64
}

// NewManager 创建一个新的队列管理器
func NewManager(processor Processor, config *Config) (*Manager, error) {
	if processor == nil {
		return nil, errors.New("processor cannot be nil")
	}
	if config == nil {
		return nil, errors.New("config cannot be nil")
	}
	// 在副本上合并，避免改写调用方传入的配置
	cfg := *config
	if err := cfg.MergeWithDefaults(); err != nil {
		return nil, fmt.Errorf("invalid queue config: %w", err)
	}
	cfg.warnDeprecated()

	opts := engine.ReliableOptions{
		Name:            cfg.Source,
		LeaseDuration:   cfg.LeaseDuration,
		DedupWindow:     cfg.DedupWindow,
		FailedRetention: cfg.FailedRetention,
		Capacity:        cfg.QueueSize,
	}

	var eng engine.ReliableEngine
	var err error
	switch cfg.EngineType {
	case engine.REDIS:
		client := cfg.RedisClient
		if client == nil {
			client = redisx.Redis
		}
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		defer cancel()
		eng, err = engine.NewRedisStreamEngine(ctx, client, opts)
	case engine.CHANNEL:
		eng, err = engine.NewMemoryEngine(opts)
	default:
		err = fmt.Errorf("unsupported queue engine type: %s", cfg.EngineType)
	}
	if err != nil {
		return nil, err
	}

	return &Manager{eng: eng, processor: processor, config: &cfg}, nil
}

// Start 启动队列管理器。Manager 只能启动一次：已在运行返回 ErrManagerAlreadyRunning，
// 已经停止过返回 ErrManagerClosed。
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status != StatusStopped {
		return common.ErrManagerAlreadyRunning
	}
	if m.used {
		return common.ErrManagerClosed
	}
	m.used = true
	m.claimCtx, m.claimCancel = context.WithCancel(context.Background())
	m.procCtx, m.procCancel = context.WithCancel(context.Background())
	m.status = StatusRunning

	for i := 0; i < m.config.WorkerCount; i++ {
		m.wg.Add(1)
		go m.workerLoop(i)
	}
	log.Printf("manager.Start(): [Info] Queue manager(source: %s, engine: %s) started with %d workers", m.config.Source, m.config.EngineType, m.config.WorkerCount)
	return nil
}

// Stop 停止队列管理器：立即停止领取新任务，在 min(ctx, Config.Timeout) 内等待在途任务完成。
// 超时则取消 Processor 的 ctx 并再给少量时间退出，仍返回 ErrStopTimeout；
// 未确认的任务保留租约，到期后可被恢复。Processor 不响应取消时其 goroutine 会继续运行，
// 这种情况下 Stop 如实返回错误，而不是假装已停止。
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.status != StatusRunning {
		m.mu.Unlock()
		return common.ErrManagerNotRunning
	}
	m.status = StatusStopping
	m.mu.Unlock()

	log.Printf("Manager.Stop(): [Info] Stopping queue manager(source: %s)...", m.config.Source)
	m.claimCancel()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(m.config.Timeout)
	defer timer.Stop()
	var stopErr error
	select {
	case <-done:
	case <-timer.C:
		stopErr = fmt.Errorf("%w: drain timeout %v", common.ErrStopTimeout, m.config.Timeout)
	case <-ctx.Done():
		stopErr = fmt.Errorf("%w: %v", common.ErrStopTimeout, ctx.Err())
	}

	if stopErr != nil {
		m.procCancel()
		grace := time.NewTimer(stopGrace)
		defer grace.Stop()
		select {
		case <-done:
		case <-grace.C:
			stopErr = fmt.Errorf("%w (%d tasks still running, leases will expire and be recovered)", stopErr, m.inflight.Load())
		}
	}
	m.procCancel()

	m.mu.Lock()
	m.status = StatusStopped
	m.mu.Unlock()

	select {
	case <-done:
		if err := m.eng.Close(); err != nil && stopErr == nil {
			stopErr = fmt.Errorf("close queue engine: %w", err)
		}
	default:
	}
	if stopErr != nil {
		log.Printf("Manager.Stop(): [Error] queue(source: %s): %v", m.config.Source, stopErr)
		return stopErr
	}
	log.Printf("Manager.Stop(): [Info] Stopped queue (source: %s) successfully", m.config.Source)
	return nil
}

// AddData 添加数据到队列，任务ID自动生成。
//
// 返回 nil 仅表示 Redis 已接收该任务，不表示业务完成。网络超时/断连时结果不确定
// （任务可能已入队）；此时重试 AddData 会产生新的任务，可能重复。需要安全重试请使用 AddDataWithID。
func (m *Manager) AddData(ctx context.Context, data []byte) error {
	return m.add(ctx, GenerateID(), data, false)
}

// AddDataWithID 使用调用方指定的稳定ID添加数据，可安全重试。
//
// 去重契约：同一个ID，若任务尚未完成（待处理/执行中/待重试）或处于失败留存，再次提交
// 相同内容视为重复并返回 nil，内容不同返回 common.ErrIDConflict；任务成功完成后，
// 该ID在 Config.DedupWindow 内仍按同样规则去重，窗口过后同一ID会被当作新任务。
// 未完成的任务不会因窗口过期而被覆盖。
func (m *Manager) AddDataWithID(ctx context.Context, id string, data []byte) error {
	if err := engine.ValidateID(id); err != nil {
		return fmt.Errorf("task id %q: %w", id, err)
	}
	return m.add(ctx, id, data, true)
}

func (m *Manager) add(ctx context.Context, id string, data []byte, dedup bool) error {
	if m.GetStatus() != StatusRunning {
		return common.ErrManagerNotRunning
	}
	if len(data) == 0 {
		return common.ErrItemEmpty
	}
	if _, err := m.eng.Enqueue(ctx, id, data, dedup); err != nil {
		return mapContextErr(err)
	}
	return nil
}

func mapContextErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", common.ErrContextCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", common.ErrContextTimeout, err)
	}
	return err
}

// ListFailed 返回失败留存中最早的至多 limit 条任务（1 <= limit <= 1000）。
// 不会自动重新处理；处理后用 ReplayFailed 重放。channel 引擎的失败留存同样只在进程内。
func (m *Manager) ListFailed(ctx context.Context, limit int) ([]FailedMessage, error) {
	return m.eng.ListFailed(ctx, limit)
}

// ReplayFailed 将失败任务以同一ID转回就绪队列，重试预算重置。
// 重放失败（如 Redis 出错、任务不在失败留存）时原记录保持不变。
func (m *Manager) ReplayFailed(ctx context.Context, id string) error {
	return m.eng.ReplayFailed(ctx, id)
}

// GetStatus 获取管理器状态
func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// GetStats 获取管理器统计信息
func (m *Manager) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"status":   m.GetStatus(),
		"workers":  m.config.WorkerCount,
		"inflight": m.inflight.Load(),
		"config":   *m.config,
	}
}

// ---------------------------------------------------------------------------
// worker

func (m *Manager) workerLoop(id int) {
	defer m.wg.Done()
	for m.claimCtx.Err() == nil {
		m.workOnce(id)
	}
}

// workOnce 领取并处理至多一个任务；任何 panic 都被隔离，worker 继续服务
func (m *Manager) workOnce(id int) {
	defer func() {
		if r := recover(); r != nil {
			pe := common.NewPanicError(r, string(debug.Stack()))
			log.Printf("Manager.worker[%d]: [Error] internal panic recovered: %s", id, pe.FormatAsSimple())
			m.sleep(time.Second)
		}
	}()

	// 在发出领取请求之前记录时刻：服务端租约从脚本执行时算起，晚于该时刻，
	// 所以以它作为本地租约起点是保守的（不会比服务端更晚判定过期）。
	receiveStart := time.Now()
	d, err := m.eng.Receive(m.claimCtx)
	switch {
	case err == nil:
		m.handle(id, d, receiveStart)
	case errors.Is(err, common.ErrQueueEmpty):
		m.sleep(m.config.PollInterval)
	case m.claimCtx.Err() != nil:
		// 正在停止
	default:
		log.Printf("Manager.worker[%d]: [Error] receive failed: %v", id, err)
		m.sleep(time.Second)
	}
}

func (m *Manager) sleep(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-m.claimCtx.Done():
	}
}

func (m *Manager) handle(id int, d *engine.Delivery, leaseStart time.Time) {
	m.inflight.Add(1)
	defer m.inflight.Add(-1)

	ctx, cancel := context.WithCancel(m.procCtx)
	defer cancel()

	var leaseLost atomic.Bool
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		m.renewLoop(ctx, cancel, d, leaseStart, &leaseLost)
	}()

	err := m.safeProcess(ctx, d)
	cancel()
	<-renewDone

	m.settle(id, d, err, leaseLost.Load())
}

// safeProcess 调用 Processor，panic 转为 *common.PanicError
func (m *Manager) safeProcess(ctx context.Context, d *engine.Delivery) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = common.NewPanicError(r, string(debug.Stack()))
		}
	}()
	return m.processor.Process(ctx, d.Data)
}

// renewLoop 在 Processor 运行期间自动续租。续租确认凭证失效，或持续无法确认以致本地认为
// 租约已过期时，取消 Processor 的 ctx 并标记 leaseLost，后续不再据此任务做失败/重试转移。
func (m *Manager) renewLoop(ctx context.Context, cancel context.CancelFunc, d *engine.Delivery, leaseStart time.Time, leaseLost *atomic.Bool) {
	lease := m.config.LeaseDuration
	interval := lease / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastOK := leaseStart
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sentAt := time.Now() // 续租请求发送前的时刻；成功后以它（而非响应到达时刻）作为新的本地租约起点
		rctx, rcancel := context.WithTimeout(context.Background(), interval)
		err := m.eng.Renew(rctx, d)
		rcancel()
		switch {
		case err == nil:
			lastOK = sentAt
		case errors.Is(err, common.ErrLeaseLost):
			log.Printf("Manager.renew: [Warning] task %s lease lost (taken over or finished elsewhere), cancelling processor", d.ID)
			leaseLost.Store(true)
			cancel()
			return
		default:
			log.Printf("Manager.renew: [Warning] task %s renew failed: %v", d.ID, err)
			if time.Since(lastOK) >= lease {
				log.Printf("Manager.renew: [Error] task %s lease presumed expired, cancelling processor", d.ID)
				leaseLost.Store(true)
				cancel()
				return
			}
		}
	}
}

// settle 依据 Processor 结果确认/重试/失败留存。所有转移使用独立限时 ctx。
func (m *Manager) settle(id int, d *engine.Delivery, procErr error, leaseLost bool) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()

	// 租约已失效（凭证被拒，或本地推定过期）：不论 Processor 返回 nil、错误还是 panic，
	// 都不再确认/重试/失败转移，任务留给持有当前凭证的实例或租约到期后的恢复。
	if leaseLost {
		log.Printf("Manager.worker[%d]: [Warning] task %s lease lost; skipping ack/retry/fail (processor result: %v)", id, d.ID, procErr)
		return
	}

	if procErr == nil {
		if err := m.eng.Ack(ctx, d); err != nil {
			log.Printf("Manager.worker[%d]: [Error] ack task %s failed: %v (task may be redelivered)", id, d.ID, err)
		}
		return
	}

	// 停止超时取消 ctx 导致的返回不是业务结论，保留任务等待恢复（panic 仍是明确结论）
	var pe *common.PanicError
	isPanic := errors.As(procErr, &pe)
	if !isPanic && m.procCtx.Err() != nil {
		log.Printf("Manager.worker[%d]: [Warning] task %s interrupted (%v); left for recovery", id, d.ID, procErr)
		return
	}

	reason := truncate(procErr.Error())
	retryable := isPanic || common.IsRetryableError(procErr)
	if isPanic {
		reason = truncate(fmt.Sprintf("panic: %v", pe.GetPanicInfo()["panic"]))
		log.Printf("Manager.worker[%d]: [Error] processor panic on task %s: %s", id, d.ID, pe.FormatAsSimple())
	}

	if retryable && d.Retries < m.config.MaxRetries {
		delay := retryDelay(m.config, d.Retries)
		if err := m.eng.Retry(ctx, d, delay, reason); err != nil {
			log.Printf("Manager.worker[%d]: [Error] retry task %s failed: %v (task will be recovered after lease expiry)", id, d.ID, err)
		}
		return
	}
	if retryable {
		reason = "retries exhausted: " + reason
	}
	if err := m.eng.Fail(ctx, d, reason); err != nil {
		log.Printf("Manager.worker[%d]: [Error] retain failed task %s failed: %v (task will be recovered after lease expiry)", id, d.ID, err)
	}
}

func truncate(s string) string {
	if len(s) > maxReasonLen {
		return s[:maxReasonLen]
	}
	return s
}

// retryDelay 第 retries+1 次重试的延迟：RetryDelay * RetryFactor^retries，上限 MaxRetryDelay
func retryDelay(cfg *Config, retries int) time.Duration {
	delay := float64(cfg.RetryDelay)
	maxDelay := float64(cfg.MaxRetryDelay)
	for i := 0; i < retries && delay < maxDelay; i++ {
		delay *= cfg.RetryFactor
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return time.Duration(delay)
}
