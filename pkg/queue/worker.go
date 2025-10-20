package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Worker 表示一个工作协程
type Worker struct {
	id        int
	ctx       context.Context
	cancel    context.CancelFunc
	wg        *sync.WaitGroup
	engine    engine.Engine
	processor Processor
	config    *Config
	closed    bool
	mu        sync.RWMutex
	stop      chan struct{}
}

// NewWorker 创建一个新的 Worker
func NewWorker(id int, ctx context.Context, engine engine.Engine, processor Processor, config *Config) *Worker {
	if engine == nil {
		panicError := common.NewPanicError(errors.New("engine cannot be nil"), string(debug.Stack()))
		panic(panicError.FormatAsSimple())
	}
	if processor == nil {
		panicError := common.NewPanicError(errors.New("processor cannot be nil"), string(debug.Stack()))
		panic(panicError.FormatAsSimple())
	}
	if config == nil {
		panicError := common.NewPanicError(errors.New("config cannot be nil"), string(debug.Stack()))
		panic(panicError.FormatAsSimple())
	}

	ctx, cancel := context.WithCancel(ctx)

	return &Worker{
		id:        id,
		ctx:       ctx,
		cancel:    cancel,
		wg:        &sync.WaitGroup{},
		engine:    engine,
		processor: processor,
		config:    config,
		closed:    false,
		stop:      make(chan struct{}),
		mu:        sync.RWMutex{},
	}
}

// Start 启动 Worker
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop 停止 Worker
func (w *Worker) Stop() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()

	// 先关闭stop channel，再取消context
	close(w.stop)
	w.cancel() // 取消context，强制停止goroutine

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Printf("Worker.Stop(): [Info] Worker[%d]队列, 成功停止处理。", w.id)
	case <-time.After(w.config.Timeout):
		log.Printf("Worker.Stop(): [Error] Worker[%d]队列, 停止处理超时。", w.id)
		// 即使超时，context已经被取消，goroutine会停止
	}
}

// run Worker 的主循环
func (w *Worker) run() {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
		w.wg.Done()
	}()

	for {
		select {
		case <-w.stop:
			log.Printf("Worker.run(): [Info] Worker[%d]队列, 收到停止信号, 停止处理。", w.id)
			return
		case <-w.ctx.Done():
			log.Printf("Worker.run(): [Info] Worker[%d]队列, 上下文context取消, 停止处理。", w.id)
			return
		default:
			if err := w.work(); err != nil {
				// 检查是否是context取消导致的错误
				if errors.Is(err, context.Canceled) {
					// context被取消，直接退出循环
					log.Printf("Worker.run(): [Info] Worker[%d]队列, context被取消, 停止处理。", w.id)
					return
				}
				// 其他错误记录日志，但继续运行
				log.Printf("Worker.run(): [Error] Worker[%d]队列, 处理数据错误(%v), 继续尝试处理。", w.id, err)
			}
		}
	}
}

// work 处理一条数据 (从处理中队列读取数据->处理数据->放入要么重试队列要么失败队列)
func (w *Worker) work() error {
	data, err := w.engine.PopBlocking(w.ctx, w.config.Processing)
	if err != nil {
		return err
	}

	item, err := FromJSON(data)
	if err != nil {
		log.Printf("Worker.work(): [Error] Worker[%d]队列, 解析数据错误(%v), 将数据(%s)移动到失败队列。", w.id, err, string(data))
		return PushToFailedQueueNonBlocking(w.ctx, w.engine, w.config, data)
	}

	// 处理数据
	err = w.processor.Process(w.ctx, item.Data)
	if err != nil {
		// 如果是重试错误，则放入重试队列
		if common.IsRetryableError(err) && item.ShouldRetry(w.config) {
			log.Printf("Worker.work(): [Error] Worker[%d]队列, 处理失败, 重试(item: %s), 将数据(%s)移动到重试队列。", w.id, item.ID, string(data))
			return HandleRetry(w.ctx, w.engine, w.config, item, data)
		}

		// 如果是明确的失败队列错误，则放入失败队列 (考虑到业务逻辑有可能导致超时，我们这里不处理超时错误)
		if common.IsErrorableError(err) {
			log.Printf("Worker.work(): [Error] Worker[%d]队列, 处理超时, 移动(item: %s)到失败队列。", w.id, item.ID)
			return PushToFailedQueueNonBlocking(w.ctx, w.engine, w.config, data)
		}

		// 如果是其他错误，则当前数据落盘，无需处理
		log.Printf("Worker.work(): [Error] Worker[%d]队列, 处理失败, 将数据(%s)落盘。", w.id, string(data))
		err := SaveFailedDataToFile(w.config, data)
		if err != nil {
			log.Printf("Worker.work(): [Error] Worker[%d]队列, 处理失败, 将数据(%s)落盘但失败(%v)。", w.id, string(data), err)
		}
	}

	return nil
}
