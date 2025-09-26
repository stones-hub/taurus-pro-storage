package queue

import (
	"context"
	"log"
	"sync"

	"github.com/stones-hub/taurus-pro-common/pkg/recovery"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Reader 表示一个读取协程
type Reader struct {
	id       int
	engine   engine.Engine
	config   *Config
	stopChan chan struct{}
	wg       *sync.WaitGroup
}

// NewReader 创建一个新的 Reader
func NewReader(id int, engine engine.Engine, config *Config, wg *sync.WaitGroup) *Reader {
	if engine == nil {
		panic("engine cannot be nil")
	}
	if config == nil {
		panic("config cannot be nil")
	}

	return &Reader{
		id:       id,
		engine:   engine,
		config:   config,
		stopChan: make(chan struct{}),
		wg:       wg,
	}
}

// Start 启动 Reader
func (r *Reader) Start() {
	r.wg.Add(1)
	go r.run()
}

// Stop 停止 Reader
func (r *Reader) Stop() {
	close(r.stopChan)
}

// run Reader 的主循环
func (r *Reader) run() {
	defer r.wg.Done()
	defer recovery.GlobalPanicRecovery.Recover("异步队列读取器[taurus-pro-storage/pkg/queue/reader.run()]")

	for {
		select {
		case <-r.stopChan:
			log.Printf("Queue Reader %d: Stopped", r.id)
			return
		default:
			// 从源队列读取数据并放入处理中队列
			if err := r.readOne(); err != nil {
				// 判断是不是context超时错误
				if err == context.DeadlineExceeded || err == context.Canceled {
					log.Printf("Reader.run() warnning: Reader[%d]队列, 管道数据为空(%v), 读取超时, 请忽略。", r.id, err)
				} else {
					log.Printf("Reader.run() error: Reader[%d]队列, 读取数据错误(%v), 请及时检查队列是否正常。", r.id, err)
				}
			}
		}
	}
}

// readOne 从source队列中读取一条数据，并放入processing队列
func (r *Reader) readOne() error {
	// 创建上下文，用于数据获取
	ctx, cancel := context.WithTimeout(context.Background(), r.config.ReaderInterval)
	defer cancel()

	// 从源队列获取数据
	data, err := r.engine.Pop(ctx, r.config.Source, r.config.ReaderInterval)
	if err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			// 上下文超时或取消, 返回错误
			return err
		}
		return nil // 无数据，正常返回, 没数据的时候等待的超时时间到了
	}

	// 解析数据以获取ID
	item, err := FromJSON(data)
	if err != nil {
		log.Printf("Reader %d: Error parsing data: %v", r.id, err)
		// 格式错误的数据移到失败队列
		return moveToFailedQueue(r.engine, r.config, data)
	}

	// 立即放入处理中队列，防止数据丢失
	err = r.engine.Push(ctx, r.config.Processing, data)
	if err != nil {
		log.Printf("Reader %d: Error moving to processing queue: %v", r.id, err)
		return err
	}

	log.Printf("Reader %d: Successfully moved item %s to processing queue", r.id, item.ID)
	return nil
}
