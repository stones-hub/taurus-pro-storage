package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
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
		return nil, fmt.Errorf("processor cannot be nil")
	}
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// 验证配置的合理性
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	var queueEngine engine.Engine

	switch config.EngineType {
	case engine.TypeRedis:
		queueEngine = engine.NewRedisEngine(redisx.Redis)
	case engine.TypeChannel:
		queueEngine = engine.NewChannelEngine()
	default:
		return nil, fmt.Errorf("unsupported queue engine type: %s", config.EngineType)
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
		return fmt.Errorf("manager is already running or stopping")
	}

	// 恢复处理中队列的未完成数据
	if err := m.recoverProcessingQueue(); err != nil {
		log.Printf("Warning: Failed to recover processing queue: %v", err)
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

	log.Printf("Queue manager started with %d readers and %d workers", m.config.ReaderCount, m.config.WorkerCount)
	return nil
}

// Stop 停止队列管理器
func (m *Manager) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32((*int32)(&m.status), int32(StatusRunning), int32(StatusStopping)) {
		return fmt.Errorf("manager is not running")
	}

	log.Printf("Stopping queue manager...")

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
			log.Printf("Error closing queue engine: %v", err)
		}
		atomic.StoreInt32((*int32)(&m.status), int32(StatusStopped))
		log.Printf("Queue manager stopped successfully")
		return nil
	case <-ctx.Done():
		log.Printf("Queue manager stop timeout: %v", ctx.Err())
		return ctx.Err()
	}
}

// AddData 添加数据到队列
func (m *Manager) AddData(ctx context.Context, data []byte) error {
	if atomic.LoadInt32((*int32)(&m.status)) != int32(StatusRunning) {
		return fmt.Errorf("manager is not running")
	}

	if len(data) == 0 {
		return fmt.Errorf("data cannot be empty")
	}

	item := &DataItem{
		Data: data,
	}

	itemData, err := item.ToJSON()
	if err != nil {
		return fmt.Errorf("marshal data item: %w", err)
	}

	if err := m.engine.Push(ctx, m.config.Source, itemData); err != nil {
		return fmt.Errorf("push to source queue: %w", err)
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
	ticker := time.NewTicker(m.config.FailedInterval)
	defer ticker.Stop()

	log.Printf("Failed queue processor started")

	for {
		select {
		case <-m.stopChan:
			log.Printf("Failed queue processor stopped")
			return
		case <-ticker.C:
			if err := m.processFailedQueueBatch(); err != nil {
				log.Printf("Error processing failed queue batch: %v", err)
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
		return fmt.Errorf("batch pop from failed queue: %w", err)
	}

	if len(items) == 0 {
		return nil
	}

	log.Printf("Processing %d items from failed queue", len(items))

	for _, data := range items {
		// 解析失败队列数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("[数据丢失] Error parsing failed queue data: %v, data: %s", err, string(data))
			continue
		}

		// 直接在这里处理一次，如果失败就记录日志落盘
		ctx, cancel := context.WithTimeout(context.Background(), m.config.WorkerTimeout)
		err = m.processor.Process(ctx, item.Data)
		cancel()

		if err != nil {
			// 处理失败，记录日志落盘
			log.Printf("[最终失败] Failed to process item %s after retries, discarding permanently. Error: %v, Data: %s",
				item.ID, err, string(item.Data))
		} else {
			// 处理成功
			log.Printf("[最终成功] Successfully processed item %s from failed queue", item.ID)
		}
	}

	return nil
}

// processRetryQueue 处理延迟重试队列
func (m *Manager) processRetryQueue() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.config.RetryInterval)
	defer ticker.Stop()

	log.Printf("Retry queue processor started")

	for {
		select {
		case <-m.stopChan:
			log.Printf("Retry queue processor stopped")
			return
		case <-ticker.C:
			if err := m.processRetryQueueBatch(); err != nil {
				log.Printf("读取重试队列失败: %v\n", err)
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

	log.Printf("Processing %d items from retry queue", len(items))

	for _, data := range items {
		// 解析数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("parse retry queue data error: %v", err)
			// 尝试丢到失败队列
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			pushErr := m.engine.Push(ctx, m.config.Failed, data)
			cancel()
			if pushErr != nil {
				log.Printf("[数据丢失] Error pushing failed item to failed queue: %v, data: %s, 数据丢弃。", pushErr, string(data))
			}
			continue
		}

		// 直接处理到期的数据
		if err := m.processRetryItem(item, data); err != nil {
			log.Printf("Error processing retry item: %v", err)
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
			log.Printf("Retry item %s processing timeout, moving to failed queue", item.ID)
			return moveToFailedQueue(m.engine, m.config, originalData)
		}

		if IsRetryableError(err) && item.ShouldRetry(m.config) {
			log.Printf("Retry item %s processing failed with retryable error, scheduling retry", item.ID)
			return handleRetry(m.engine, m.config, item, originalData)
		}

		// 重试次数已用完或不可重试的错误，移到失败队列
		log.Printf("Retry item %s processing failed with non-retryable error, moving to failed queue", item.ID)
		return moveToFailedQueue(m.engine, m.config, originalData)
	}

	// 处理成功，记录日志
	log.Printf("Retry queue: Successfully processed item %s after %d retries", item.ID, item.RetryCount)
	return nil
}

// recoverProcessingQueue 恢复处理中队列的未完成数据
func (m *Manager) recoverProcessingQueue() error {
	// 由于Processing队列现在是channel，无法获取所有数据
	// 在启动时，channel中的数据会自动被Worker消费
	// 所以这里不需要特殊的恢复逻辑
	log.Printf("Processing queue recovery not needed (using channel)")
	return nil
}
