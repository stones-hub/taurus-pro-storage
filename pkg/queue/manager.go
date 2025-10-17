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

// Manager 队列管理器
type Manager struct {
	engine    engine.Engine
	processor Processor
	config    *Config
	workers   []*Worker
	readers   []*Reader
	wg        *sync.WaitGroup
	stop      chan struct{}
	status    Status
	mu        sync.RWMutex // 保护workers和readers切片的并发访问
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewManager 创建一个新的队列管理器
func NewManager(processor Processor, config *Config) (*Manager, error) {
	if processor == nil {
		panicError := common.NewPanicError(errors.New("processor cannot be nil"), string(debug.Stack()))
		return nil, errors.New(panicError.FormatAsSimple())
	}
	if config == nil {
		panicError := common.NewPanicError(errors.New("config cannot be nil"), string(debug.Stack()))
		return nil, errors.New(panicError.FormatAsSimple())
	}

	err := config.MergeWithDefaults()
	if err != nil {
		panicError := common.NewPanicError(err, string(debug.Stack()))
		return nil, errors.New(panicError.FormatAsSimple())
	}

	var queueEngine engine.Engine

	switch config.EngineType {
	case engine.REDIS:
		queueEngine = engine.NewRedisEngine(redisx.Redis)
	case engine.CHANNEL:
		queueEngine = engine.NewChannelEngine(
			config.QueueSize,
			config.Source,
			config.Failed,
			config.Processing,
		)
	default:
		panicError := common.NewPanicError(fmt.Errorf("unsupported queue engine type: %s", config.EngineType), string(debug.Stack()))
		return nil, errors.New(panicError.FormatAsSimple())
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Manager{
		engine:    queueEngine,
		processor: processor,
		config:    config,
		stop:      make(chan struct{}),
		status:    StatusStopped,
		workers:   make([]*Worker, config.WorkerCount),
		readers:   make([]*Reader, config.ReaderCount),
		wg:        &sync.WaitGroup{},
		mu:        sync.RWMutex{},
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// Start 启动队列管理器
func (m *Manager) Start() error {
	if !atomic.CompareAndSwapInt32((*int32)(&m.status), int32(StatusStopped), int32(StatusRunning)) {
		return common.ErrManagerAlreadyRunning
	}

	// 恢复之前的数据
	m.recoverLastData()

	// 启动读取协程
	m.mu.Lock()
	for i := 0; i < m.config.ReaderCount; i++ {
		reader := NewReader(i, m.ctx, m.engine, m.config)
		m.readers = append(m.readers, reader)
		reader.Start()
	}
	m.mu.Unlock()

	// 启动工作协程
	m.mu.Lock()
	for i := 0; i < m.config.WorkerCount; i++ {
		worker := NewWorker(i, m.ctx, m.engine, m.processor, m.config)
		m.workers = append(m.workers, worker)
		worker.Start()
	}
	m.mu.Unlock()

	// 如果启用了失败队列处理，启动失败队列处理协程
	if m.config.EnableFailedQueue {
		m.wg.Add(1)
		go m.failedQueueProcessor()
	}

	// 如果启用了重试队列处理，启动重试队列处理协程
	if m.config.EnableRetryQueue {
		m.wg.Add(1)
		go m.retryQueueProcessor()
	}

	log.Printf("manager.Start(): [Info] Queue manager(source: %s) started with %d readers and %d workers", m.config.Source, m.config.ReaderCount, m.config.WorkerCount)
	return nil
}

// Stop 停止队列管理器
func (m *Manager) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32((*int32)(&m.status), int32(StatusRunning), int32(StatusStopping)) {
		return common.ErrManagerNotRunning
	}

	log.Printf("Manager.Stop(): [Info] Stopping queue manager(name: %s)...", m.config.Source)

	// 1. 发送停止信号
	m.cancel()
	close(m.stop)

	// 2. 并行停止所有组件
	m.mu.RLock()
	readers := make([]*Reader, len(m.readers))
	copy(readers, m.readers)
	workers := make([]*Worker, len(m.workers))
	copy(workers, m.workers)
	m.mu.RUnlock()

	// 并行停止 readers 和 workers
	readerDone := make(chan struct{})
	workerDone := make(chan struct{})

	go func() {
		defer close(readerDone)
		for _, reader := range readers {
			reader.Stop()
		}
	}()

	go func() {
		defer close(workerDone)
		for _, worker := range workers {
			worker.Stop()
		}
	}()

	// 3. 等待组件停止完成
	componentTimeout := time.After(m.config.Timeout * 2)
	m.waitForComponents("readers", readerDone, componentTimeout, ctx)
	m.waitForComponents("workers", workerDone, componentTimeout, ctx)

	// 4. 等待其他协程结束
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		m.wg.Wait()
	}()

	otherTimeout := time.After(m.config.Timeout)
	m.waitForComponents("other goroutines", otherDone, otherTimeout, ctx)

	// 5. 清理资源
	if err := m.engine.Close(); err != nil {
		log.Printf("Manager.Stop(): [Error] Closing queue engine error: %v", err)
	}

	atomic.StoreInt32((*int32)(&m.status), int32(StatusStopped))
	log.Printf("Manager.Stop(): [Info] Stopped queue (name: %s) successfully", m.config.Source)
	return nil
}

// waitForComponents 等待组件停止完成
func (m *Manager) waitForComponents(name string, done <-chan struct{}, timeout <-chan time.Time, ctx context.Context) {
	select {
	case <-done:
		log.Printf("Manager.Stop(): [Info] %s stopped", name)
	case <-timeout:
		log.Printf("Manager.Stop(): [Warning] %s stop timeout", name)
	case <-ctx.Done():
		log.Printf("Manager.Stop(): [Warning] %s stop cancelled: %v", name, ctx.Err())
	}
}

// AddData 添加数据到队列
func (m *Manager) AddData(ctx context.Context, data []byte) error {
	if atomic.LoadInt32((*int32)(&m.status)) != int32(StatusRunning) {
		return common.ErrManagerNotRunning
	}

	if len(data) == 0 {
		return common.ErrItemEmpty
	}

	item := &DataItem{
		Data: data,
	}

	itemData, err := item.ToJSON()
	if err != nil {
		return common.ErrItemJsonMarshalFailed
	}

	// 推送到源队列
	if err := m.engine.PushBlocking(ctx, m.config.Source, itemData); err != nil {
		if errors.Is(err, context.Canceled) {
			return common.ErrContextCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return common.ErrContextTimeout
		}
		return err
	}

	return nil
}

// GetStatus 获取管理器状态
func (m *Manager) GetStatus() Status {
	return Status(atomic.LoadInt32((*int32)(&m.status)))
}

// GetStats 获取管理器统计信息
func (m *Manager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"status":  m.GetStatus(),
		"readers": len(m.readers),
		"workers": len(m.workers),
		"config":  m.config,
	}
}

// processFailedQueue 处理失败队列
func (m *Manager) failedQueueProcessor() {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
		m.wg.Done()
	}()

	ticker := time.NewTicker(m.config.FailedInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			log.Printf("Manager.failedQueueProcessor(): [Info] 失败队列(name: %s) 收到停止信号, 停止处理。", m.config.Failed)
			return
		case <-m.ctx.Done():
			log.Printf("Manager.failedQueueProcessor(): [Info] 失败队列(name: %s) 上下文context取消, 停止处理。", m.config.Failed)
			return
		case <-ticker.C:
			// 执行失败队列处理
			if err := HandleFailedProcessorBatch(m.ctx, m.engine, m.processor, m.config); err != nil {
				if errors.Is(err, context.Canceled) {
					log.Printf("Manager.failedQueueProcessor(): [Info] 失败队列(name: %s) 上下文context取消, 停止处理。", m.config.Failed)
					return
				}
			}

			// 重置定时器，确保下次执行时间稳定
			ticker.Reset(m.config.FailedInterval)
		}
	}
}

// processRetryQueue 处理延迟重试队列
func (m *Manager) retryQueueProcessor() {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
		m.wg.Done()
	}()

	ticker := time.NewTicker(m.config.RetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			log.Printf("Manager.retryQueueProcessor(): [Info] 重试队列(name: %s) 收到停止信号, 停止处理。", m.config.Retry)
			return
		case <-m.ctx.Done():
			log.Printf("Manager.retryQueueProcessor(): [Info] 重试队列(name: %s) 上下文context取消, 停止处理。", m.config.Retry)
			return
		case <-ticker.C:
			// 执行重试队列处理
			if err := HandleRetryProcessorBatch(m.ctx, m.engine, m.processor, m.config); err != nil {
				if errors.Is(err, context.Canceled) {
					log.Printf("Manager.retryQueueProcessor(): [Info] 重试队列(name: %s) 上下文context取消, 停止处理。", m.config.Retry)
					return
				}
			}

			// 重置定时器，确保下次执行时间稳定
			ticker.Reset(m.config.RetryInterval)
		}
	}
}

// TODO : 恢复之前的数据, 后面在实现, 现在不实现。
func (m *Manager) recoverLastData() error {
	// 注意持久化的队列数据是无需恢复的，只是channel的队列数据需要恢复， 但是恢复的条件是在程序退出时，所有数据需要落盘

	return nil
}
