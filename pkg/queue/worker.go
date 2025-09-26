package queue

import (
	"context"
	"log"
	"sync"

	"github.com/stones-hub/taurus-pro-common/pkg/recovery"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Worker 表示一个工作协程
type Worker struct {
	id        int
	engine    engine.Engine
	processor Processor
	config    *Config
	stopChan  chan struct{}
	wg        *sync.WaitGroup
}

// NewWorker 创建一个新的 Worker
func NewWorker(id int, engine engine.Engine, processor Processor, config *Config, wg *sync.WaitGroup) *Worker {
	if engine == nil {
		panic("engine cannot be nil")
	}
	if processor == nil {
		panic("processor cannot be nil")
	}
	if config == nil {
		panic("config cannot be nil")
	}

	return &Worker{
		id:        id,
		engine:    engine,
		processor: processor,
		config:    config,
		stopChan:  make(chan struct{}),
		wg:        wg,
	}
}

// Start 启动 Worker
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop 停止 Worker
func (w *Worker) Stop() {
	close(w.stopChan)
}

// run Worker 的主循环
func (w *Worker) run() {
	defer w.wg.Done()
	defer recovery.GlobalPanicRecovery.Recover("异步队列处理协程[taurus-pro-storage/pkg/queue/worker.run()]")

	for {
		select {
		case <-w.stopChan:
			log.Printf("Queue Worker %d: Stopped", w.id)
			return
		default:
			// 从队列中取数据处理, 如果数据没有，会等待w.config.ReaderInterval
			if err := w.processOne(); err != nil {
				// 判断是不是context超时错误
				if err == context.DeadlineExceeded || err == context.Canceled {
					log.Fatalf("Worker.run() warnning: Worker[%d]队列, 管道数据为空(%v), 处理超时, 请忽略。", w.id, err)
				} else {
					log.Printf("Worker.run() error: Worker[%d]队列, 处理数据错误(%v), 请及时检查队列是否正常。", w.id, err)
				}
			}
		}
	}
}

// processOne 处理一条数据 (从处理中队列读取数据->处理数据->放入要么重试队列要么失败队列)
func (w *Worker) processOne() error {
	// 创建上下文，用于数据获取
	ctx, cancel := context.WithTimeout(context.Background(), w.config.ReaderInterval)
	defer cancel()

	// 从处理中队列获取一条数据, 获取后，数据会从processing队列移除
	data, err := w.engine.Pop(ctx, w.config.Processing, w.config.ReaderInterval)
	if err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			// 上下文超时或取消, 返回错误
			return err
		}
		return nil // 无数据，正常返回
	}

	// 解析数据以获取ID
	item, err := FromJSON(data)
	if err != nil {
		log.Printf("Worker %d: Error parsing data: %v", w.id, err)
		// 数据已经从处理中队列移除，直接移到失败队列
		return moveToFailedQueue(w.engine, w.config, data)
	}

	// 创建带超时的上下文用于处理数据
	pCtx, pCancel := context.WithTimeout(context.Background(), w.config.WorkerTimeout)
	defer pCancel()

	// 处理数据
	err = w.processor.Process(pCtx, item.Data)

	if err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			// 如果错误是上下文超时或取消，则将数据移动到失败队列
			log.Printf("Worker %d: Processing timeout for item %s, moving to failed queue", w.id, item.ID)
			return moveToFailedQueue(w.engine, w.config, data)
		}

		// 如果错误是可重试的错误，则进行重试
		if IsRetryableError(err) && item.ShouldRetry(w.config) {
			log.Printf("Worker %d: Processing failed with retryable error for item %s, scheduling retry", w.id, item.ID)
			return handleRetry(w.engine, w.config, item, data)
		}

		// 重试次数已用完或不可重试的错误，移到失败队列
		log.Printf("Worker %d: Processing failed with non-retryable error for item %s, moving to failed queue", w.id, item.ID)
		return moveToFailedQueue(w.engine, w.config, data)
	}

	// 处理成功，记录日志
	log.Printf("Worker %d: Successfully processed item %s", w.id, item.ID)
	return nil
}
