package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-common/pkg/recovery"
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
	wg        sync.WaitGroup
	stopChan  chan struct{}
	status    Status
	mu        sync.RWMutex // 保护workers和readers切片的并发访问
}

// NewManager 创建一个新的队列管理器
func NewManager(processor Processor, config *Config) (*Manager, error) {
	if processor == nil {
		return nil, errors.New("manager.NewManager(): processor must been set")
	}
	if config == nil {
		return nil, errors.New("manager.NewManager(): config must been set")
	}

	// 验证配置的合理性
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("manager.NewManager(): invalid config: %w", err)
	}

	var queueEngine engine.Engine

	switch config.EngineType {
	case engine.TypeRedis:
		queueEngine = engine.NewRedisEngine(redisx.Redis)
	case engine.TypeChannel:
		queueEngine = engine.NewChannelEngine()
	default:
		return nil, fmt.Errorf("manager.NewManager(): unsupported queue engine type: %s", config.EngineType)
	}

	return &Manager{
		engine:    queueEngine,
		processor: processor,
		config:    config,
		stopChan:  make(chan struct{}),
		status:    StatusStopped,
	}, nil
}

// validateConfig 验证配置的合理性
func validateConfig(config *Config) error {
	if config.Source == "" {
		return fmt.Errorf("source queue name cannot be empty")
	}
	if config.Failed == "" {
		return fmt.Errorf("failed queue name cannot be empty")
	}
	if config.Processing == "" {
		return fmt.Errorf("processing queue name cannot be empty")
	}
	if config.Retry == "" {
		return fmt.Errorf("retry queue name cannot be empty")
	}
	if config.ReaderCount <= 0 {
		return fmt.Errorf("reader count must be positive")
	}
	if config.WorkerCount <= 0 {
		return fmt.Errorf("worker count must be positive")
	}
	if config.WorkerTimeout <= 0 {
		return fmt.Errorf("worker timeout must be positive")
	}
	if config.MaxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative")
	}
	if config.RetryDelay <= 0 {
		return fmt.Errorf("retry delay must be positive")
	}
	if config.MaxRetryDelay <= 0 {
		return fmt.Errorf("max retry delay must be positive")
	}
	if config.RetryFactor <= 0 {
		return fmt.Errorf("retry factor must be positive")
	}
	return nil
}

// Start 启动队列管理器
func (m *Manager) Start() error {
	if !atomic.CompareAndSwapInt32((*int32)(&m.status), int32(StatusStopped), int32(StatusRunning)) {
		return fmt.Errorf("manager.Start(): manager is already running or stopping")
	}

	// 恢复处理中队列的未完成数据
	if err := m.recoverProcessingQueue(); err != nil {
		log.Printf("manager.Start(): [warning] Failed to recover processing queue: %v", err)
	}

	// 启动读取协程
	m.mu.Lock()
	for i := 0; i < m.config.ReaderCount; i++ {
		reader := NewReader(i, m.engine, m.config, &m.wg)
		m.readers = append(m.readers, reader)
		reader.Start()
	}
	m.mu.Unlock()

	// 启动工作协程
	m.mu.Lock()
	for i := 0; i < m.config.WorkerCount; i++ {
		worker := NewWorker(i, m.engine, m.processor, m.config, &m.wg)
		m.workers = append(m.workers, worker)
		worker.Start()
	}
	m.mu.Unlock()

	// 如果启用了失败队列处理，启动失败队列处理协程
	if m.config.EnableFailedQueue {
		m.wg.Add(1)
		go m.processFailedQueue()
	}

	// 如果启用了重试队列处理，启动重试队列处理协程
	if m.config.EnableRetryQueue {
		m.wg.Add(1)
		go m.processRetryQueue()
	}

	log.Printf("manager.Start(): [Info] Queue manager(source: %s) started with %d readers and %d workers", m.config.Source, m.config.ReaderCount, m.config.WorkerCount)
	return nil
}

// Stop 停止队列管理器
func (m *Manager) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32((*int32)(&m.status), int32(StatusRunning), int32(StatusStopping)) {
		return fmt.Errorf("Manager.Stop(): manager is not running")
	}

	log.Printf("Manager.Stop(): [Info] Stopping queue manager(name: %s)...", m.config.Source)

	// 关闭停止通道
	close(m.stopChan)

	// 停止所有读取协程
	m.mu.RLock()
	readers := make([]*Reader, len(m.readers))
	copy(readers, m.readers)
	m.mu.RUnlock()

	for _, reader := range readers {
		reader.Stop()
	}

	// 停止所有工作协程
	m.mu.RLock()
	workers := make([]*Worker, len(m.workers))
	copy(workers, m.workers)
	m.mu.RUnlock()

	for _, worker := range workers {
		worker.Stop()
	}

	// 等待所有协程结束或上下文取消
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 关闭队列引擎
		if err := m.engine.Close(); err != nil {
			log.Printf("Manager.Stop(): [Error] Closing queue (name: %s) engine error : %v", m.config.Source, err)
		}
		atomic.StoreInt32((*int32)(&m.status), int32(StatusStopped))
		log.Printf("Manager.Stop(): [Info] Stopped queue (name: %s) successfully", m.config.Source)
		return nil
	case <-ctx.Done():
		log.Printf("Manager.Stop(): [Error] Stopping queue (name: %s) timeout: %v", m.config.Source, ctx.Err())
		return ctx.Err()
	}
}

// AddData 添加数据到队列
func (m *Manager) AddData(ctx context.Context, data []byte) error {
	if atomic.LoadInt32((*int32)(&m.status)) != int32(StatusRunning) {
		return fmt.Errorf("manager.AddData(): queue manager(source: %s) is not running", m.config.Source)
	}

	if len(data) == 0 {
		return fmt.Errorf("manager.AddData(): data must been set")
	}

	item := &DataItem{
		Data: data,
	}

	itemData, err := item.ToJSON()
	if err != nil {
		return fmt.Errorf("manager.AddData(): marshal data item: %w", err)
	}

	if err := m.engine.Push(ctx, m.config.Source, itemData); err != nil {
		return fmt.Errorf("manager.AddData(): push to source queue(source: %s) error: %v", m.config.Source, err)
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
func (m *Manager) processFailedQueue() {
	defer m.wg.Done()
	defer recovery.GlobalPanicRecovery.Recover("异步队列失败队列处理协程[taurus-pro-storage/pkg/queue/manager.processFailedQueue()]")

	ticker := time.NewTicker(m.config.FailedInterval)
	defer ticker.Stop()

	log.Printf("Manager.processFailedQueue(): [Info] failed_queue(name: %s) processor started", m.config.Failed)

	for {
		select {
		case <-m.stopChan:
			log.Printf("Manager.processFailedQueue(): [Info] failed_queue(name: %s) processor stopped", m.config.Failed)
			return
		case <-ticker.C:
			if err := m.processFailedQueueBatch(); err != nil {
				log.Printf("Manager.processFailedQueue(): [Error] failed_queue(name: %s) batch process error: %v", m.config.Failed, err)
			}
		}
	}
}

// processFailedQueueBatch 处理一批失败队列数据
func (m *Manager) processFailedQueueBatch() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	items, err := m.engine.BatchPop(ctx, m.config.Failed, m.config.FailedBatch, time.Second*5)
	if err != nil {
		return fmt.Errorf("manager.processFailedQueueBatch(): batch pop from failed_queue(name: %s) error: %v", m.config.Failed, err)
	}

	if len(items) == 0 {
		return nil
	}

	log.Printf("Manager.processFailedQueueBatch(): [Info] processing %d items from failed_queue(name: %s)", len(items), m.config.Failed)

	for _, data := range items {
		// 解析失败队列数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("Manager.processFailedQueueBatch(): [Error] parsing failed_queue data(name: %s) error: %v, data: %s", m.config.Failed, err, string(data))
			continue
		}

		// 直接在这里处理一次，如果失败就记录日志落盘
		ctx, cancel := context.WithTimeout(context.Background(), m.config.WorkerTimeout)
		err = m.processor.Process(ctx, item.Data)
		cancel()

		if err != nil {
			// 处理失败，记录日志落盘
			log.Printf("Manager.processFailedQueueBatch(): [Error] Failed to process item %s after retries, discarding permanently. Error: %v, Data: %s",
				item.ID, err, string(item.Data))
		} else {
			// 处理成功
			log.Printf("Manager.processFailedQueueBatch(): [Info] Successfully processed item %s from failed_queue(name: %s)", item.ID, m.config.Failed)
		}
	}

	return nil
}

// processRetryQueue 处理延迟重试队列
func (m *Manager) processRetryQueue() {
	defer m.wg.Done()
	defer recovery.GlobalPanicRecovery.Recover("异步队列重试队列处理协程[taurus-pro-storage/pkg/queue/manager.processRetryQueue()]")

	ticker := time.NewTicker(m.config.RetryInterval)
	defer ticker.Stop()

	log.Printf("Manager.processRetryQueue(): [Info] retry_queue(name: %s) processor started", m.config.Retry)

	for {
		select {
		case <-m.stopChan:
			log.Printf("Manager.processRetryQueue(): [Info] retry_queue(name: %s) processor stopped", m.config.Retry)
			return
		case <-ticker.C:
			if err := m.processRetryQueueBatch(); err != nil {
				log.Printf("Manager.processRetryQueue(): [Error] retry_queue(name: %s) batch process error: %v", m.config.Retry, err)
			}
		}
	}
}

// processRetryQueueBatch 处理一批重试队列数据
func (m *Manager) processRetryQueueBatch() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// 使用延迟队列的PopDelayed方法
	items, err := m.engine.PopDelayed(ctx, m.config.Retry, m.config.RetryBatch)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("pop delayed from retry queue: %w", err)
	}

	if len(items) == 0 {
		return nil
	}

	log.Printf("Manager.processRetryQueueBatch(): [Info] processing %d items from retry_queue(name: %s)", len(items), m.config.Retry)

	for _, data := range items {
		// 解析数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("Manager.processRetryQueueBatch(): [Error] parsing retry_queue data(name: %s) error: %v", m.config.Retry, err)
			// 尝试丢到失败队列
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			pushErr := m.engine.Push(ctx, m.config.Failed, data)
			cancel()
			if pushErr != nil {
				log.Printf("Manager.processRetryQueueBatch(): [Error] pushing failed item to failed_queue(name: %s) error: %v, data: %s, 数据丢弃。", m.config.Failed, pushErr, string(data))
			}
			continue
		}

		// 直接处理到期的数据
		if err := m.processRetryItem(item, data); err != nil {
			log.Printf("Manager.processRetryQueueBatch(): [Error] processing retry item(name: %s) error: %v", m.config.Retry, err)
		}
	}

	return nil
}

// processRetryItem 处理单个重试项
func (m *Manager) processRetryItem(item *DataItem, originalData []byte) error {
	// 创建带超时的上下文用于处理数据
	ctx, cancel := context.WithTimeout(context.Background(), m.config.WorkerTimeout)
	defer cancel()

	// 处理数据
	err := m.processor.Process(ctx, item.Data)

	if err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			log.Printf("Manager.processRetryItem(): [Error] retry item %s processing timeout, moving to failed_queue(name: %s)", item.ID, m.config.Failed)
			return moveToFailedQueue(m.engine, m.config, originalData)
		}

		if IsRetryableError(err) && item.ShouldRetry(m.config) {
			log.Printf("Manager.processRetryItem(): [Error] retry item %s processing failed with retryable error, scheduling retry", item.ID)
			return handleRetry(m.engine, m.config, item, originalData)
		}

		// 重试次数已用完或不可重试的错误，移到失败队列
		log.Printf("Manager.processRetryItem(): [Error] retry item %s processing failed with non-retryable error, moving to failed_queue(name: %s)", item.ID, m.config.Failed)
		return moveToFailedQueue(m.engine, m.config, originalData)
	}

	// 处理成功，记录日志
	log.Printf("Manager.processRetryItem(): [Info] Successfully processed item %s after %d retries", item.ID, item.RetryCount)
	return nil
}

// recoverProcessingQueue 恢复处理中队列的未完成数据
func (m *Manager) recoverProcessingQueue() error {
	// 由于Processing队列现在是channel，无法获取所有数据
	// 在启动时，channel中的数据会自动被Worker消费
	// 所以这里不需要特殊的恢复逻辑
	// log.Printf("Processing queue recovery not needed (using channel)")
	return nil
}
